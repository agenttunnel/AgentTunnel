package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// enablePeerMailbox turns both gates on: the durable thread flag the decider
// enforces, and the runtime box the daemon keeps enabled.
func enablePeerMailbox(t *testing.T, r *Runner, ctx context.Context) {
	t.Helper()
	on := true
	if err := r.engine.Dispatch(ctx, contracts.Command{
		ID: "gate", Type: contracts.CmdPeerMailboxSet, ThreadID: "t1", PeerMailbox: &on,
	}); err != nil {
		t.Fatalf("enable peer mailbox: %v", err)
	}
	r.Mailbox().SetEnabled(true)
}

// disablePeerMailbox turns the durable gate off. New threads start with peer
// messaging on, so gate-off behavior is asserted by opting out explicitly —
// exactly what the desktop "Peer chat" toggle does.
func disablePeerMailbox(t *testing.T, r *Runner, ctx context.Context) {
	t.Helper()
	off := false
	if err := r.engine.Dispatch(ctx, contracts.Command{
		ID: "ungate", Type: contracts.CmdPeerMailboxSet, ThreadID: "t1", PeerMailbox: &off,
	}); err != nil {
		t.Fatalf("disable peer mailbox: %v", err)
	}
}

// newPromptHarness is newHarness with the prompt passed as the script's first
// argument instead of stdin, so the fake harness can knowingly assert on what
// the pass prompt contains.
func newPromptHarness(t *testing.T, script string) (*Runner, *domain.Engine, context.Context) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.SetClock(fixedClock)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, "sh", spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	inherited := contextstore.NewInheritedContext(t.TempDir())
	if err := inherited.Ensure(); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(Deps{
		Engine:    eng,
		Registry:  providers.NewRegistry(driver),
		Roles:     contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs:      logstore.New(t.TempDir()),
		Ledger:    usage.NewLedger(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: inherited,
		Settings:  settings.New(t.TempDir()),
		Tools:     toolbox.New(),
		Clock:     fixedClock,
		Cfg:       Config{MaxPasses: 2, MaxConcurrency: 2},
	})
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	return r, eng, ctx
}

func TestPostMessageRecordsAndDelivers(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	if err := r.Dispatch(ctx, "t1", []contracts.DispatchTask{{
		AgentID: "agent-researcher", Name: "researcher", Role: contracts.RoleExecutor, Task: "research the flaky test",
	}}); err != nil {
		t.Fatalf("dispatch researcher: %v", err)
	}
	enablePeerMailbox(t, r, ctx)

	if err := r.PostMessage(ctx, "t1", "agent-orchestrator", "researcher", "watch the flaky test"); err != nil {
		t.Fatalf("post message: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
	got := state.Mailbox[0]
	if got.From != "agent-orchestrator" || got.To != "agent-researcher" ||
		got.Kind != "message" || got.Body != "watch the flaky test" {
		t.Fatalf("entry = %+v", got)
	}
	if !got.At.Equal(fixedClock()) {
		t.Fatalf("entry carries the event time, got %v", got.At)
	}

	inbox := r.Mailbox().Inbox("t1", "agent-researcher")
	if len(inbox) != 1 || inbox[0].Body != "watch the flaky test" {
		t.Fatalf("runtime inbox = %+v", inbox)
	}

	// The sender's log shows where the message went.
	sawTrail := false
	for _, e := range r.Logs().Entries("t1") {
		if e.Kind == contracts.LogTool && e.Text == "→ researcher: watch the flaky test" {
			sawTrail = true
		}
	}
	if !sawTrail {
		t.Fatalf("no tool log for the sent message: %+v", r.Logs().Entries("t1"))
	}

	// "orchestrator" resolves to the team lead, and a raw agent id works too.
	if err := r.PostMessage(ctx, "t1", "agent-researcher", "orchestrator", "found it"); err != nil {
		t.Fatalf("post to orchestrator: %v", err)
	}
	if err := r.PostMessage(ctx, "t1", "agent-researcher", "agent-researcher", "note to self"); err != nil {
		t.Fatalf("post by agent id: %v", err)
	}
	state, _ = eng.State(ctx, "t1")
	if len(state.Mailbox) != 3 || state.Mailbox[1].To != "agent-orchestrator" || state.Mailbox[2].To != "agent-researcher" {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
}

func TestPostMessageRejectsCleanly(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)

	// Gate off (the thread's toggle, not the create default): rejected with
	// nothing recorded.
	disablePeerMailbox(t, r, ctx)
	err := r.PostMessage(ctx, "t1", "agent-orchestrator", "researcher", "psst")
	if !errors.Is(err, ErrPeerMailboxDisabled) {
		t.Fatalf("want ErrPeerMailboxDisabled, got %v", err)
	}
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 0 {
		t.Fatalf("rejected message was recorded: %+v", state.Mailbox)
	}

	// Unknown recipient: rejected with nothing recorded.
	enablePeerMailbox(t, r, ctx)
	err = r.PostMessage(ctx, "t1", "agent-orchestrator", "ghost", "anyone there?")
	if !errors.Is(err, ErrUnknownRecipient) || err.Error() != "unknown agent ghost" {
		t.Fatalf("want unknown agent ghost, got %v", err)
	}
	state, _ = eng.State(ctx, "t1")
	if len(state.Mailbox) != 0 {
		t.Fatalf("rejected message was recorded: %+v", state.Mailbox)
	}
}

func TestRunTurnDeliversPeerMessagesIntoPassPrompt(t *testing.T) {
	script := `fail=0
case "$1" in *"## Peer messages"*) ;; *) fail=1 ;; esac
case "$1" in *"from agent-researcher: the flaky test is payment.e2e"*) ;; *) fail=1 ;; esac
case "$1" in *"for researcher from agent-orchestrator: run it twice"*) ;; *) fail=1 ;; esac
if [ "$fail" = 1 ]; then
  printf '{"type":"error","text":"pass prompt lacked peer messages"}\n'
  exit 1
fi
printf '{"type":"text","text":"pass prompt carried the messages"}\n'`

	r, eng, ctx := newPromptHarness(t, script)
	if err := r.Dispatch(ctx, "t1", []contracts.DispatchTask{{
		AgentID: "agent-researcher", ParentAgentID: "agent-orchestrator",
		Name: "researcher", Role: contracts.RoleExecutor, Task: "research",
	}}); err != nil {
		t.Fatalf("dispatch researcher: %v", err)
	}
	// A dispatch alone leaves the agent pending — the state of one waiting on
	// a dependency, whose messages belong in its own first prompt, not the
	// lead's. The relay under test is for a RUNNING teammate, so the fixture
	// starts it.
	running := contracts.AgentRunning
	if err := eng.Dispatch(ctx, contracts.Command{ID: "run", Type: contracts.CmdAgentStatusSet, ThreadID: "t1", Status: &contracts.AgentStatusChanged{AgentID: "agent-researcher", Status: running}}); err != nil {
		t.Fatalf("start researcher: %v", err)
	}
	enablePeerMailbox(t, r, ctx)

	// Messages a running subagent posted before the next pass starts: one to
	// the lead, one to a subagent that must be relayed, not lost.
	if err := r.PostMessage(ctx, "t1", "agent-researcher", "orchestrator", "the flaky test is payment.e2e"); err != nil {
		t.Fatalf("post to lead: %v", err)
	}
	if err := r.PostMessage(ctx, "t1", "agent-orchestrator", "researcher", "run it twice"); err != nil {
		t.Fatalf("post to subagent: %v", err)
	}

	if err := New(r).RunTurn(ctx, "t1", "please check the flaky test"); err != nil {
		for _, e := range r.Logs().Entries("t1") {
			t.Logf("orchestrator %s: %s", e.Kind, e.Text)
		}
		t.Fatalf("run turn: %v", err)
	}

	// The drain consumed everything: no inbox re-delivers, and the durable
	// log holds exactly the two messages posted.
	for _, agent := range []contracts.AgentID{"agent-orchestrator", "agent-researcher"} {
		if inbox := r.Mailbox().Inbox("t1", agent); len(inbox) != 0 {
			t.Fatalf("%s inbox should be empty after the turn: %+v", agent, inbox)
		}
	}
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 2 {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
}

func TestPostMessageAbsorbsEchoedDuplicate(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	if err := r.Dispatch(ctx, "t1", []contracts.DispatchTask{{
		AgentID: "agent-researcher", Name: "researcher", Role: contracts.RoleExecutor, Task: "research the flaky test",
	}}); err != nil {
		t.Fatalf("dispatch researcher: %v", err)
	}
	enablePeerMailbox(t, r, ctx)

	// The mid-pass HTTP delivery lands first; the printed-line drain then
	// offers the same message again — a harness that executes the command
	// and echoes the line. One record, one runtime delivery.
	if err := r.PostMessage(ctx, "t1", "agent-orchestrator", "researcher", "watch the flaky test"); err != nil {
		t.Fatalf("post message: %v", err)
	}
	if err := r.PostMessage(ctx, "t1", "agent-orchestrator", "researcher", "watch the flaky test"); err != nil {
		t.Fatalf("echoed duplicate should succeed as a no-op: %v", err)
	}
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
	if inbox := r.Mailbox().Inbox("t1", "agent-researcher"); len(inbox) != 1 {
		t.Fatalf("runtime inbox = %+v", inbox)
	}

	// A genuinely new message in the same turn still lands.
	if err := r.PostMessage(ctx, "t1", "agent-orchestrator", "researcher", "it was the retry loop"); err != nil {
		t.Fatalf("post second message: %v", err)
	}
	state, _ = eng.State(ctx, "t1")
	if len(state.Mailbox) != 2 {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
}

func TestDrainPeerMessagesSkipsPendingAgents(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	enablePeerMailbox(t, r, ctx)
	// Dispatched but never run: the agent is pending, the way one waiting on
	// a dependency is between waves. Its messages must stay in its inbox for
	// its first prompt, not relay to the lead's pass.
	if err := r.Dispatch(ctx, "t1", []contracts.DispatchTask{{
		AgentID: "agent-waiter", Name: "waiter", Role: contracts.RoleExecutor, Task: "wait for work",
	}}); err != nil {
		t.Fatalf("dispatch waiter: %v", err)
	}
	if err := r.Mailbox().Post("t1", contracts.MailboxPosted{From: "agent-orchestrator", To: "agent-waiter", Kind: "message", Body: "start when ready"}); err != nil {
		t.Fatalf("post to runtime inbox: %v", err)
	}

	state, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	o := New(r)
	if got := o.drainPeerMessages(state, orchestratorAgentID); got != "" {
		t.Fatalf("a pending agent's inbox must not drain into the lead's pass: %q", got)
	}
	if inbox := r.Mailbox().Inbox("t1", "agent-waiter"); len(inbox) != 1 {
		t.Fatalf("message left the pending agent's inbox: %+v", inbox)
	}

	// Once started, the agent's messages relay like any running teammate's.
	running := contracts.AgentRunning
	if err := eng.Dispatch(ctx, contracts.Command{ID: "s", Type: contracts.CmdAgentStatusSet, ThreadID: "t1", Status: &contracts.AgentStatusChanged{AgentID: "agent-waiter", Status: running}}); err != nil {
		t.Fatalf("start agent: %v", err)
	}
	state, _ = eng.State(ctx, "t1")
	if got := o.drainPeerMessages(state, orchestratorAgentID); !strings.Contains(got, "for waiter from agent-orchestrator: start when ready") {
		t.Fatalf("a started agent's message should relay to the lead: %q", got)
	}
}

func TestRunTurnPrintedMessageToolReachesMailbox(t *testing.T) {
	// Each pass prints only its tool line: line-oriented harness output is
	// merged without separators before command-line parsing, so the bare
	// command must stand alone to stay recognizable.
	script := `case "$AT_ROLE:$AT_PASS" in
  orchestrator:0) printf '%s\n' 'tool dispatch --role executor --name researcher --task "map the repo"' ;;
  orchestrator:*) printf '%s\n' 'tool message --to researcher --text "watch the retry logic"' ;;
  *) printf '{"type":"text","text":"mapped"}\n' ;;
esac`

	r, eng, ctx := newHarness(t, script)
	enablePeerMailbox(t, r, ctx)

	if err := New(r).RunTurn(ctx, "t1", "map the repository"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	// Exactly one durable mailbox event, addressed to the researcher.
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
	var researcher contracts.AgentID
	for _, id := range state.AgentOrder {
		if state.Agents[id].Name == "researcher" {
			researcher = id
		}
	}
	msg := state.Mailbox[0]
	if msg.From != "agent-orchestrator" || msg.To != researcher ||
		msg.Kind != "message" || msg.Body != "watch the retry logic" {
		t.Fatalf("message = %+v (researcher = %s)", msg, researcher)
	}

	// The message arrived after the last pass prompt, so it surfaces once in
	// the orchestrator's final summary instead of being lost.
	var final string
	for _, m := range state.Messages {
		if m.Author == "orchestrator" {
			final = m.Text
		}
	}
	if strings.Count(final, "Peer messages received after the final pass") != 1 ||
		!strings.Contains(final, "for researcher from agent-orchestrator: watch the retry logic") {
		t.Fatalf("final message = %q", final)
	}
}

func TestRunTurnMessageReachesWaitingAgent(t *testing.T) {
	// Pass 0 dispatches two agents — the second waits on the first — and then
	// messages the second by name. The message must land in that agent's own
	// prompt when its work starts, not be relayed back to the lead.
	// The commands ride JSON text events with trailing newlines: consecutive
	// plain lines are merged without separators before command-line parsing.
	script := `case "$AT_ROLE" in
orchestrator)
  case "$AT_PASS" in
    0)
      printf '{"type":"text","text":"tool dispatch --role executor --name first --task \\"do the first thing\\"\\n"}\n'
      printf '{"type":"text","text":"tool dispatch --role executor --name second --depends-on first --task \\"do the second thing\\"\\n"}\n'
      printf '{"type":"text","text":"tool message --to second --text \\"use the config from the first step\\"\\n"}\n'
      printf '{"type":"text","text":"dispatched two agents"}\n'
      ;;
    *)
      case "$1" in *"for second from agent-orchestrator"*)
        printf '{"type":"error","text":"teammate message was relayed to the lead twice: %s"}\n' "$1"
        exit 1 ;;
      esac
      printf '{"type":"text","text":"all done"}\n'
      ;;
  esac ;;
*)
  case "$AT_TASK" in
    *first*) printf '{"type":"text","text":"first finished"}\n' ;;
    *second*)
      fail=0
      case "$1" in *"## Messages from teammates"*) ;; *) fail=1 ;; esac
      case "$1" in *"from agent-orchestrator: use the config from the first step"*) ;; *) fail=1 ;; esac
      if [ "$fail" = 1 ]; then
        printf '{"type":"error","text":"second prompt lacked teammate messages: %s"}\n' "$1"
        exit 1
      fi
      printf '{"type":"text","text":"second finished"}\n'
      ;;
  esac ;;
esac`

	r, eng, ctx := newPromptHarness(t, script)
	enablePeerMailbox(t, r, ctx)

	if err := New(r).RunTurn(ctx, "t1", "map the repository"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 {
		t.Fatalf("mailbox = %+v, agents = %+v", state.Mailbox, state.Agents)
	}
	msg := state.Mailbox[0]
	if msg.From != "agent-orchestrator" || msg.Kind != "message" || msg.Body != "use the config from the first step" {
		t.Fatalf("message = %+v", msg)
	}
	if state.Agents[msg.To].Name != "second" {
		t.Fatalf("message went to %s (%+v), want the second agent", msg.To, state.Agents[msg.To])
	}
	if state.Agents[msg.To].Status != contracts.AgentSucceeded {
		t.Fatalf("second agent status = %s (%+v)", state.Agents[msg.To].Status, state.Agents[msg.To])
	}

	// The waiting agent consumed its inbox: nothing re-delivers.
	if inbox := r.Mailbox().Inbox("t1", msg.To); len(inbox) != 0 {
		t.Fatalf("second inbox should be empty after the turn: %+v", inbox)
	}
}

func TestPostMessageRejectsEmptyBody(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	enablePeerMailbox(t, r, ctx)

	if err := r.PostMessage(ctx, "t1", "agent-orchestrator", "orchestrator", "   "); !errors.Is(err, ErrEmptyMessage) {
		t.Fatalf("want ErrEmptyMessage, got %v", err)
	}
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 0 {
		t.Fatalf("empty message was recorded: %+v", state.Mailbox)
	}
}

func TestResolveRecipientPrefersNonTerminalAgent(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	enablePeerMailbox(t, r, ctx)
	for _, id := range []contracts.AgentID{"agent-r1", "agent-r2"} {
		if err := r.Dispatch(ctx, "t1", []contracts.DispatchTask{{
			AgentID: id, Name: "researcher", Role: contracts.RoleExecutor, Task: "research",
		}}); err != nil {
			t.Fatalf("dispatch %s: %v", id, err)
		}
	}
	r.setStatus(ctx, "t1", "agent-r2", contracts.AgentSucceeded, "")

	// Two agents share the name; the one still running is addressed.
	if err := r.PostMessage(ctx, "t1", "agent-orchestrator", "researcher", "first"); err != nil {
		t.Fatalf("post: %v", err)
	}
	state, _ := eng.State(ctx, "t1")
	if state.Mailbox[0].To != "agent-r1" {
		t.Fatalf("resolved to %s, want the non-terminal agent-r1: %+v", state.Mailbox[0].To, state.Mailbox)
	}

	// With all of them finished, the newest with that name wins.
	r.setStatus(ctx, "t1", "agent-r1", contracts.AgentSucceeded, "")
	if err := r.PostMessage(ctx, "t1", "agent-orchestrator", "researcher", "second"); err != nil {
		t.Fatalf("post: %v", err)
	}
	state, _ = eng.State(ctx, "t1")
	if state.Mailbox[1].To != "agent-r2" {
		t.Fatalf("resolved to %s, want the newest agent-r2: %+v", state.Mailbox[1].To, state.Mailbox)
	}
}

func TestSubagentPrintedMessageRoutesThroughToolInbox(t *testing.T) {
	// The tool line is printed, not executed, and is followed by more output:
	// the two must stay separate lines or the message body is corrupted.
	script := `case "$AT_ROLE" in
orchestrator) printf '{"type":"text","text":"leading"}\n' ;;
*)
  printf '%s\n' 'tool message --to orchestrator --text "the build is broken"'
  printf '%s\n' 'summary line follows'
  ;;
esac`

	r, eng, ctx := newHarness(t, script)
	enablePeerMailbox(t, r, ctx)
	tasks := []contracts.DispatchTask{{AgentID: "agent-worker", Name: "worker", Role: contracts.RoleExecutor, Task: "work"}}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}

	// The printed line became a message call attributed to the subagent.
	calls := r.DrainTools("t1")
	if len(calls) != 1 ||
		calls[0].Kind != toolbox.KindMessage || calls[0].AgentID != "agent-worker" ||
		calls[0].To != "orchestrator" || calls[0].Text != "the build is broken" {
		t.Fatalf("inbox calls = %+v", calls)
	}
	// Findings kept the prose, not the tool line.
	result := r.Notebook("t1").Get("result_worker")
	if len(result) != 1 || result[0].Value != "summary line follows" {
		t.Fatalf("worker result = %+v", result)
	}

	// The drain delivers it as a durable message from the subagent. The box
	// was drained for the assertion above, so put the call back.
	r.tools.Post("t1", calls[0])
	if tasks := New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil); len(tasks) != 0 {
		t.Fatalf("a message is not a task: %+v", tasks)
	}
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 ||
		state.Mailbox[0].From != "agent-worker" || state.Mailbox[0].To != "agent-orchestrator" ||
		state.Mailbox[0].Body != "the build is broken" {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
}

func TestSubagentProtocolMessageFrameRoutesThroughToolInbox(t *testing.T) {
	script := `case "$AT_ROLE" in
orchestrator) printf '{"type":"text","text":"leading"}\n' ;;
*) printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"message","to":"orchestrator","body":"protocol ping"}' ;;
esac`

	r, eng, ctx := newHarness(t, script)
	enablePeerMailbox(t, r, ctx)
	tasks := []contracts.DispatchTask{{AgentID: "agent-worker", Name: "worker", Role: contracts.RoleExecutor, Task: "work"}}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}

	calls := r.DrainTools("t1")
	if len(calls) != 1 ||
		calls[0].Kind != toolbox.KindMessage || calls[0].AgentID != "agent-worker" ||
		calls[0].To != "orchestrator" || calls[0].Text != "protocol ping" {
		t.Fatalf("inbox calls = %+v", calls)
	}
	r.tools.Post("t1", calls[0])
	New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 || state.Mailbox[0].Body != "protocol ping" || state.Mailbox[0].From != "agent-worker" {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
}

func TestOrchestratorProtocolMessageReachesSamePassDispatch(t *testing.T) {
	// Pass 0 dispatches an agent over the protocol and messages it in the same
	// breath: the dispatch has not reached the thread state yet, so the message
	// must resolve against the pass's pending dispatch.
	script := `case "$AT_ROLE" in
orchestrator)
  if [ "$AT_PASS" = "0" ]; then
    printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"worker","task":"work","role":"executor"}'
    printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"message","to":"worker","body":"bring the config"}'
  fi
  printf '{"type":"text","text":"pass-%s"}\n' "$AT_PASS"
  ;;
*) printf '{"type":"text","text":"worked"}\n' ;;
esac`

	r, eng, ctx := newHarness(t, script)
	enablePeerMailbox(t, r, ctx)

	if err := New(r).RunTurn(ctx, "t1", "run the job"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
	msg := state.Mailbox[0]
	if msg.From != "agent-orchestrator" || msg.To != "agent-worker" || msg.Body != "bring the config" {
		t.Fatalf("message = %+v", msg)
	}
}

func TestRunTurnProtocolDispatchRunsOnce(t *testing.T) {
	// A protocol dispatch and a box-minted one must not both end up in the
	// task list, or the agent runs twice. The worker reports usage per run,
	// so a double run doubles the ledger.
	script := `case "$AT_ROLE" in
orchestrator)
  if [ "$AT_PASS" = "0" ]; then
    printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"worker","task":"work","role":"executor"}'
  fi
  printf '{"type":"text","text":"done"}\n' ;;
*)
  printf '{"type":"text","text":"worked"}\n'
  printf '{"usage":{"input_tokens":3,"output_tokens":2}}\n' ;;
esac`

	r, eng, ctx := newHarness(t, script)
	if err := New(r).RunTurn(ctx, "t1", "run the job"); err != nil {
		t.Fatalf("run turn: %v", err)
	}
	state, _ := eng.State(ctx, "t1")
	if state.Agents["agent-worker"].Status != contracts.AgentSucceeded {
		t.Fatalf("worker status = %s", state.Agents["agent-worker"].Status)
	}
	if got := state.Usage["agent-worker"].Usage.InputTokens; got != 3 {
		t.Fatalf("worker ran %d time(s)-worth of tokens, want exactly one run", got/3)
	}
}

func TestPeerMailboxDoesNotLeakAcrossThreads(t *testing.T) {
	// Every thread's lead shares the fixed agent-orchestrator id, and turns
	// run concurrently per thread over one Runner-wide runtime mailbox. A
	// drain in t1's turn must not consume t2's lead inbox.
	script := `fail=0
case "$1" in *"## Peer messages"*) ;; *) fail=1 ;; esac
case "$1" in *"from agent-researcher: t1 only"*) ;; *) fail=1 ;; esac
case "$1" in *"belongs to t2"*) fail=1 ;; esac
if [ "$fail" = 1 ]; then
  printf '{"type":"error","text":"pass prompt carried the wrong peer messages"}\n'
  exit 1
fi
printf '{"type":"text","text":"done"}\n'`
	r, eng, ctx := newPromptHarness(t, script)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatalf("create t2: %v", err)
	}
	r.Mailbox().SetEnabled(true)

	if err := r.PostMessage(ctx, "t1", "agent-researcher", "orchestrator", "t1 only"); err != nil {
		t.Fatalf("post t1: %v", err)
	}
	if err := r.PostMessage(ctx, "t2", "agent-researcher", "orchestrator", "belongs to t2"); err != nil {
		t.Fatalf("post t2: %v", err)
	}

	if err := New(r).RunTurn(ctx, "t1", "go"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	if inbox := r.Mailbox().Inbox("t1", "agent-orchestrator"); len(inbox) != 0 {
		t.Fatalf("t1 lead inbox should be drained by its turn: %+v", inbox)
	}
	if inbox := r.Mailbox().Inbox("t2", "agent-orchestrator"); len(inbox) != 1 || inbox[0].Body != "belongs to t2" {
		t.Fatalf("t2 lead inbox must survive t1's turn: %+v", inbox)
	}
	state, _ := eng.State(ctx, "t2")
	if len(state.Mailbox) != 1 || state.Mailbox[0].Body != "belongs to t2" {
		t.Fatalf("t2 durable mailbox = %+v", state.Mailbox)
	}
}
