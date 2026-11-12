package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// schedDispatch dispatches one command on the test engine, failing the test on
// the first refusal.
func schedDispatch(t *testing.T, eng *domain.Engine, ctx context.Context, cmd contracts.Command) {
	t.Helper()
	if err := eng.Dispatch(ctx, cmd); err != nil {
		t.Fatalf("dispatch %s: %v", cmd.Type, err)
	}
}

// createThread makes a fresh thread on the fixture's engine.
func createThread(t *testing.T, eng *domain.Engine, ctx context.Context, id contracts.ThreadID) {
	t.Helper()
	schedDispatch(t, eng, ctx, contracts.Command{ID: "c-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id})
}

// settleTrigger arms t1's schedule on the given dependencies and discharges it,
// the way the runtime does for a triggered thread: t1 comes out of it with the
// dependencies in its TriggeredFrom set.
func settleTrigger(t *testing.T, eng *domain.Engine, ctx context.Context, deps ...contracts.ThreadID) {
	t.Helper()
	schedDispatch(t, eng, ctx, contracts.Command{
		ID:       "sched",
		Type:     contracts.CmdThreadSchedule,
		ThreadID: "t1",
		Schedule: &contracts.ScheduleSpec{DependsOn: deps, Prompt: "run once your dependencies finish"},
	})
	schedDispatch(t, eng, ctx, contracts.Command{ID: "fire", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "run the scheduled work"})
}

func relaunchCall(target, prompt string) toolbox.Call {
	return toolbox.Call{Kind: KindThreadRelaunch, AgentID: "agent-orchestrator", Thread: target, Text: prompt}
}

func TestRelaunchRefusesForeignThread(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	settleTrigger(t, eng, ctx, "t2")
	createThread(t, eng, ctx, "t3")

	err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t3", "go again"))
	if !errors.Is(err, ErrThreadNotYours) {
		t.Fatalf("err = %v, want ErrThreadNotYours", err)
	}
	if target, _ := eng.State(ctx, "t3"); target.Turn != 0 || target.ControlledBy != "" {
		t.Fatalf("a refused relaunch left a trace: %+v", target)
	}

	// The refusal also surfaces as tool error text when the call rides the
	// inbox drain: the agent that asked must see why it did not run.
	r.tools.Post("t1", relaunchCall("t3", "go again"))
	New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	found := false
	for _, e := range r.logs.Entries("t1") {
		if e.Kind == contracts.LogError && strings.HasPrefix(e.Text, "tool thread-relaunch failed:") && strings.Contains(e.Text, "not one this thread was triggered by") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no refusal line in the caller's log: %+v", r.logs.Entries("t1"))
	}
}

func TestRelaunchHappyPathRecordsControlAndTurn(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	schedDispatch(t, eng, ctx, contracts.Command{
		ID: "name", Type: contracts.CmdThreadRename, ThreadID: "t2", Title: "Stabilize the parser",
	})
	settleTrigger(t, eng, ctx, "t2")

	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "go deeper and check the parser tests")); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	st, _ := eng.State(ctx, "t2")
	if st.ControlledBy != "t1" {
		t.Fatalf("controlledBy = %q, want the caller t1", st.ControlledBy)
	}
	if st.Turn != 1 || len(st.Turns) != 1 {
		t.Fatalf("turn = %d over %d tracked turns, want a fresh turn opened", st.Turn, len(st.Turns))
	}
	if len(st.Messages) != 1 {
		t.Fatalf("messages = %+v, want the relaunch opener", st.Messages)
	}
	msg := st.Messages[0]
	if msg.Author != "t1" {
		t.Fatalf("relaunch message author = %q, want the controller t1 (never user)", msg.Author)
	}
	if !strings.HasPrefix(msg.Text, "go deeper and check the parser tests") {
		t.Fatalf("relaunch message text = %q, want it to carry the prompt", msg.Text)
	}
	if !strings.Contains(msg.Text, "relaunched by thread t1") {
		t.Fatalf("relaunch message text = %q, want the report-back instruction for the relaunched agent", msg.Text)
	}
	// The receipt reaches the caller's log, so the agent that ran the tool
	// sees that the relaunch landed.
	found := false
	for _, e := range r.logs.Entries("t1") {
		if strings.Contains(e.Text, "relaunched t2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no relaunch receipt in the caller's log: %+v", r.logs.Entries("t1"))
	}
}

func TestRelaunchRefusesBusyTarget(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	schedDispatch(t, eng, ctx, contracts.Command{
		ID:       "d",
		Type:     contracts.CmdTaskDispatch,
		ThreadID: "t2",
		Dispatch: &contracts.DispatchTask{AgentID: "agent-t2-1", Name: "work", Role: contracts.RoleExecutor, Task: "run"},
	})
	schedDispatch(t, eng, ctx, contracts.Command{
		ID:       "s",
		Type:     contracts.CmdAgentStatusSet,
		ThreadID: "t2",
		Status:   &contracts.AgentStatusChanged{AgentID: "agent-t2-1", Status: contracts.AgentRunning},
	})
	settleTrigger(t, eng, ctx, "t2")

	err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "go again"))
	if !errors.Is(err, ErrThreadBusy) || !strings.Contains(err.Error(), "agent-t2-1") {
		t.Fatalf("err = %v, want a busy refusal naming the running agent", err)
	}
	if st, _ := eng.State(ctx, "t2"); st.ControlledBy != "" || st.Turn != 0 {
		t.Fatalf("a busy refusal left a trace: %+v", st)
	}
}

func TestRelaunchRefusesUnknownTargetAndSelf(t *testing.T) {
	r, _, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)

	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("ghost", "go")); !errors.Is(err, domain.ErrThreadNotFound) {
		t.Fatalf("err = %v, want ErrThreadNotFound", err)
	}
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t1", "go")); err == nil ||
		!strings.Contains(err.Error(), "cannot drive itself") {
		t.Fatalf("err = %v, want a self-drive refusal", err)
	}
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t1", "  ")); err == nil ||
		!strings.Contains(err.Error(), "prompt") {
		t.Fatalf("err = %v, want an empty-prompt refusal", err)
	}
}

// A thread stays able to relaunch what it already controls even after a later
// trigger overwrites its TriggeredFrom record.
func TestRelaunchAcceptsControlledThreadAcrossTriggers(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	createThread(t, eng, ctx, "t3")
	settleTrigger(t, eng, ctx, "t2")

	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "first relaunch")); err != nil {
		t.Fatalf("first relaunch: %v", err)
	}
	// A new cycle of the schedule: the trigger record now points at t3 only.
	settleTrigger(t, eng, ctx, "t3")

	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "second relaunch")); err != nil {
		t.Fatalf("relaunch of a controlled thread: %v", err)
	}
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t3", "fresh dep")); err != nil {
		t.Fatalf("relaunch of the latest dependency: %v", err)
	}
	// t2 was relaunched twice — once as the triggered dependency, once as a
	// controlled thread — so it carries two turns; t3 once.
	st2, _ := eng.State(ctx, "t2")
	if st2.Turn != 2 || len(st2.Messages) != 2 {
		t.Fatalf("t2: turn %d with %d openers, want two relaunches", st2.Turn, len(st2.Messages))
	}
	st3, _ := eng.State(ctx, "t3")
	if st3.Turn != 1 || len(st3.Messages) != 1 {
		t.Fatalf("t3: turn %d with %d openers, want one relaunch", st3.Turn, len(st3.Messages))
	}
}

// Two relaunches of different threads with the same prompt are not each other's
// echo; an echoed relaunch of the same thread still lands once.
func TestDrainedRelaunchesAreKeyedByTargetThread(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	createThread(t, eng, ctx, "t3")
	settleTrigger(t, eng, ctx, "t2", "t3")

	r.tools.Post("t1", relaunchCall("t2", "go again"))
	r.tools.Post("t1", relaunchCall("t2", "go again")) // the printed echo
	r.tools.Post("t1", relaunchCall("t3", "go again"))
	New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)

	for _, id := range []contracts.ThreadID{"t2", "t3"} {
		st, _ := eng.State(ctx, id)
		if st.Turn != 1 || len(st.Messages) != 1 {
			t.Fatalf("%s: turn %d with %d openers, want exactly one relaunch", id, st.Turn, len(st.Messages))
		}
	}
}

func TestThreadResultsReturnsDepData(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	schedDispatch(t, eng, ctx, contracts.Command{
		ID: "name", Type: contracts.CmdThreadRename, ThreadID: "t2", Title: "Stabilize the parser",
	})
	schedDispatch(t, eng, ctx, contracts.Command{
		ID: "wt", Type: contracts.CmdWorktreeAttach, ThreadID: "t2",
		Worktree: &contracts.WorktreeRef{Path: "/repo.worktrees/t2-abc123", Branch: "agenttunnel/t2-abc123"},
	})
	schedDispatch(t, eng, ctx, contracts.Command{
		ID: "m1", Type: contracts.CmdMessageAppend, ThreadID: "t2",
		Message: &contracts.MessageSent{MessageID: "m1", Author: "user", Text: "fix the parser crash"},
	})
	schedDispatch(t, eng, ctx, contracts.Command{
		ID: "m2", Type: contracts.CmdMessageAppend, ThreadID: "t2",
		Message: &contracts.MessageSent{MessageID: "m2", Author: "orchestrator", Text: "parser rewired, all 42 tests green"},
	})
	settleTrigger(t, eng, ctx, "t2")

	text, err := r.ThreadResults(ctx, "t1", "agent-orchestrator", toolbox.Call{Kind: KindThreadResults, Thread: "t2"})
	if err != nil {
		t.Fatalf("results: %v", err)
	}
	for _, want := range []string{"Stabilize the parser", "/repo.worktrees/t2-abc123", "agenttunnel/t2-abc123", "- user: fix the parser crash", "- orchestrator: parser rewired, all 42 tests green"} {
		if !strings.Contains(text, want) {
			t.Fatalf("thread-results missing %q:\n%s", want, text)
		}
	}

	// Through the inbox drain the result reaches the caller's log instead, so
	// an agent that queued the call mid-pass still reads the answer.
	r.tools.Post("t1", toolbox.Call{Kind: KindThreadResults, AgentID: "agent-orchestrator", Thread: "t2"})
	New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	found := false
	for _, e := range r.logs.Entries("t1") {
		if e.Kind == contracts.LogTool && strings.Contains(e.Text, "all 42 tests green") {
			found = true
		}
	}
	if !found {
		t.Fatalf("thread-results answer never reached the caller's log: %+v", r.logs.Entries("t1"))
	}
}

func TestThreadResultsRefusesNonDepIds(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	createThread(t, eng, ctx, "t3")
	settleTrigger(t, eng, ctx, "t2")

	if _, err := r.ThreadResults(ctx, "t1", "agent-orchestrator", toolbox.Call{Kind: KindThreadResults, Thread: "t3"}); !errors.Is(err, ErrThreadNotYours) {
		t.Fatalf("err = %v, want ErrThreadNotYours", err)
	}
	if _, err := r.ThreadResults(ctx, "t1", "agent-orchestrator", toolbox.Call{Kind: KindThreadResults, Thread: "ghost"}); !errors.Is(err, domain.ErrThreadNotFound) {
		t.Fatalf("err = %v, want ErrThreadNotFound", err)
	}
	if _, err := r.ThreadResults(ctx, "t1", "agent-orchestrator", toolbox.Call{Kind: KindThreadResults, Thread: "t1"}); err == nil ||
		!strings.Contains(err.Error(), "cannot drive itself") {
		t.Fatalf("err = %v, want a self-read refusal", err)
	}
}

// A thread the caller already controls stays readable across triggers, like it
// stays relaunchable.
func TestThreadResultsAcceptsControlledThread(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	createThread(t, eng, ctx, "t3")
	settleTrigger(t, eng, ctx, "t2")
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "go again")); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	settleTrigger(t, eng, ctx, "t3")

	if _, err := r.ThreadResults(ctx, "t1", "agent-orchestrator", toolbox.Call{Kind: KindThreadResults, Thread: "t2"}); err != nil {
		t.Fatalf("reading a controlled thread: %v", err)
	}
}

// The lead's prompt must offer the scheduling tools once the thread has
// dependencies to drive, or no agent would know they exist.
func TestDependencyThreadsPromptOffer(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	schedDispatch(t, eng, ctx, contracts.Command{
		ID: "name", Type: contracts.CmdThreadRename, ThreadID: "t2", Title: "Stabilize the parser",
	})
	settleTrigger(t, eng, ctx, "t2")

	st, _ := eng.State(ctx, "t1")
	deps := r.RenderDependencyThreads(ctx, st)
	if !strings.Contains(deps, "t2") || !strings.Contains(deps, "Stabilize the parser") {
		t.Fatalf("dependency listing = %q", deps)
	}

	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleOrchestrator)
	prompt := BuildOrchestratorPrompt(OrchestratorPrompt{
		Role:              role,
		UserMessage:       "check your dependencies",
		DependencyThreads: deps,
	})
	for _, want := range []string{"tool thread-results --thread", "tool thread-relaunch --thread", "Stabilize the parser", "controlled by this one"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}

	// A thread with no trigger history gets no section.
	if got := r.RenderDependencyThreads(ctx, &domain.ThreadState{}); got != "" {
		t.Fatalf("untriggered thread offered the tools: %q", got)
	}
}

// The whole flow through one real lead pass: the scheduled thread's lead runs,
// its relaunch call lands mid-pass, and the drain executes it — control is
// claimed and the target's turn starts, observed through typed events.
func TestRelaunchFlowsThroughALeadPass(t *testing.T) {
	var r *Runner
	var posted sync.Once
	r, eng, ctx := newHarnessInvoking(t, func(providers.SessionSpec) (providers.CommandSpec, error) {
		posted.Do(func() {
			r.tools.Post("t1", relaunchCall("t2", "go deeper"))
		})
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", `printf '{"type":"text","text":"checked the dependencies"}\n'`}, PromptVia: "stdin"}, nil
	})
	createThread(t, eng, ctx, "t2")
	settleTrigger(t, eng, ctx, "t2")

	events, stop := eng.SubscribeAll()
	defer stop()
	if err := New(r).RunTurn(ctx, "t1", "check your dependencies"); err != nil {
		t.Fatalf("turn: %v", err)
	}

	var controlled, relaunched bool
	for {
		select {
		case ev := <-events:
			if ev.ThreadID != "t2" {
				continue
			}
			if c, ok := ev.Payload.(contracts.ThreadControlled); ok && c.Controller == "t1" {
				controlled = true
			}
			if m, ok := ev.Payload.(contracts.MessageSent); ok && m.Author == "t1" {
				relaunched = true
			}
		default:
			goto drained
		}
	}
drained:
	if !controlled || !relaunched {
		t.Fatalf("controlled=%v relaunched=%v, want the relaunch to have landed during the turn", controlled, relaunched)
	}
	st, _ := eng.State(ctx, "t2")
	if st.ControlledBy != "t1" || st.Turn != 1 {
		t.Fatalf("target after the turn: controlledBy=%q turn=%d", st.ControlledBy, st.Turn)
	}
}

// A relaunched thread's opener is authored by its controller, so the turn's
// own prompt is prior history, not history the pass repeats back to itself.
func TestPriorMessagesExcludesControllerAuthoredOpener(t *testing.T) {
	state := &domain.ThreadState{
		ControlledBy: "t9",
		Messages: []domain.Message{
			{Author: "user", Text: "first"},
			{Author: "t9", Text: "again"},
		},
	}
	got := priorMessages(state, "again")
	if len(got) != 1 || got[0].Text != "first" {
		t.Fatalf("prior = %+v, want only the message before the relaunch opener", got)
	}

	free := &domain.ThreadState{
		Messages: []domain.Message{
			{Author: "user", Text: "first"},
			{Author: "t9", Text: "again"},
		},
	}
	if got := priorMessages(free, "again"); len(got) != 2 {
		t.Fatalf("a controller-authored message on an uncontrolled thread must not close a turn: %+v", got)
	}
}
