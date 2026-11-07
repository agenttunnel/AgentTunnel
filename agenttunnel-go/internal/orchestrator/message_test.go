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
		Notebook:  contextstore.NewNotebook(),
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

	inbox := r.Mailbox().Inbox("agent-researcher")
	if len(inbox) != 1 || inbox[0].Body != "watch the flaky test" {
		t.Fatalf("runtime inbox = %+v", inbox)
	}

	// The sender's log shows where the message went.
	sawTrail := false
	for _, e := range r.Logs().Entries("agent-orchestrator") {
		if e.Kind == contracts.LogTool && e.Text == "→ researcher: watch the flaky test" {
			sawTrail = true
		}
	}
	if !sawTrail {
		t.Fatalf("no tool log for the sent message: %+v", r.Logs().Entries("agent-orchestrator"))
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
		for _, e := range r.Logs().Entries("agent-orchestrator") {
			t.Logf("orchestrator %s: %s", e.Kind, e.Text)
		}
		t.Fatalf("run turn: %v", err)
	}

	// The drain consumed everything: no inbox re-delivers, and the durable
	// log holds exactly the two messages posted.
	for _, agent := range []contracts.AgentID{"agent-orchestrator", "agent-researcher"} {
		if inbox := r.Mailbox().Inbox(agent); len(inbox) != 0 {
			t.Fatalf("%s inbox should be empty after the turn: %+v", agent, inbox)
		}
	}
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 2 {
		t.Fatalf("mailbox = %+v", state.Mailbox)
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
	if inbox := r.Mailbox().Inbox(msg.To); len(inbox) != 0 {
		t.Fatalf("second inbox should be empty after the turn: %+v", inbox)
	}
}
