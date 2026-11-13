package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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

// relaunchMarks collects what one relaunched thread's turn left on the event
// stream: the controller's claim, the controller-authored opener, the turn's
// final orchestrator message, and the control release that ends the relaunch.
type relaunchMarks struct {
	claimed bool
	opener  string
	summary bool
}

// waitRelaunchSettled waits until the target's relaunch turn has fully
// unwound — through its final orchestrator message to the control release
// that follows it — and reports the marks seen on the way. The relaunch turn
// is asynchronous (the caller's turn does not wait for it), so tests watch it
// settle rather than reading a state snapshot that can race it.
func waitRelaunchSettled(t *testing.T, events <-chan contracts.Event, target, controller contracts.ThreadID) relaunchMarks {
	t.Helper()
	var m relaunchMarks
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("relaunch turn on %s never settled: claimed=%v opener=%q summary=%v", target, m.claimed, m.opener, m.summary)
		case ev := <-events:
			if ev.ThreadID != target {
				continue
			}
			switch p := ev.Payload.(type) {
			case contracts.ThreadControlled:
				if p.Controller == "" {
					return m
				}
				m.claimed = true
			case contracts.MessageSent:
				if p.Author == string(controller) {
					m.opener = p.Text
				}
				if p.Author == "orchestrator" {
					m.summary = true
				}
			}
		}
	}
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

	events, stop := eng.SubscribeAll()
	defer stop()
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "go deeper and check the parser tests")); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	m := waitRelaunchSettled(t, events, "t2", "t1")

	if !m.claimed || !m.summary {
		t.Fatalf("claimed=%v summary=%v, want the control claim and a completed relaunch turn", m.claimed, m.summary)
	}
	st, _ := eng.State(ctx, "t2")
	// Control is scoped to the relaunched turn: the claim landed, the turn
	// ran and ended, so the claim was released with it.
	if st.ControlledBy != "" {
		t.Fatalf("controlledBy = %q after the relaunch turn ended, want control released", st.ControlledBy)
	}
	if st.Turn != 1 || len(st.Turns) != 1 {
		t.Fatalf("turn = %d over %d tracked turns, want a fresh turn opened", st.Turn, len(st.Turns))
	}
	// The relaunch opener plus the turn's own summary.
	if len(st.Messages) != 2 {
		t.Fatalf("messages = %+v, want the relaunch opener and the final summary", st.Messages)
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
	if st.Messages[1].Author != "orchestrator" {
		t.Fatalf("final message author = %q, want the turn's own summary", st.Messages[1].Author)
	}
	// The receipt reaches the caller's log, so the agent that ran the tool
	// sees that the relaunch landed.
	found := false
	for _, e := range r.Logs().Entries("t1") {
		if strings.Contains(e.Text, "relaunched t2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no relaunch receipt in the caller's log: %+v", r.Logs().Entries("t1"))
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

// Control is scoped to the turn it governs: once a relaunch turn has ended,
// the caller keeps its permission to relaunch through the TriggeredFrom
// record, which is never cleared — until a later trigger overwrites it and
// moves the permission to the new dependencies.
func TestRelaunchPermissionRidesTriggeredFrom(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	createThread(t, eng, ctx, "t3")
	settleTrigger(t, eng, ctx, "t2")

	events, stop := eng.SubscribeAll()
	defer stop()
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "first relaunch")); err != nil {
		t.Fatalf("first relaunch: %v", err)
	}
	waitRelaunchSettled(t, events, "t2", "t1")

	// The first relaunch's turn ended and its control was released, but the
	// trigger record still names t2, so the caller may relaunch it again on
	// that permission alone.
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "second relaunch")); err != nil {
		t.Fatalf("relaunch of a released dependency: %v", err)
	}
	waitRelaunchSettled(t, events, "t2", "t1")

	// A new cycle of the schedule: the trigger record now points at t3 only,
	// so the permission to drive t2 moves with it.
	settleTrigger(t, eng, ctx, "t3")
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "third relaunch")); !errors.Is(err, ErrThreadNotYours) {
		t.Fatalf("relaunch of a dropped dependency err = %v, want ErrThreadNotYours", err)
	}
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t3", "fresh dep")); err != nil {
		t.Fatalf("relaunch of the latest dependency: %v", err)
	}
	waitRelaunchSettled(t, events, "t3", "t1")

	// t2 was relaunched twice and t3 once, each turn fully run.
	st2, _ := eng.State(ctx, "t2")
	if st2.Turn != 2 || len(st2.Messages) != 4 {
		t.Fatalf("t2: turn %d with %d messages, want two relaunches", st2.Turn, len(st2.Messages))
	}
	st3, _ := eng.State(ctx, "t3")
	if st3.Turn != 1 || len(st3.Messages) != 2 {
		t.Fatalf("t3: turn %d with %d messages, want one relaunch", st3.Turn, len(st3.Messages))
	}
}

// Two relaunches of different threads with the same prompt are not each other's
// echo; an echoed relaunch of the same thread still lands once.
func TestDrainedRelaunchesAreKeyedByTargetThread(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	createThread(t, eng, ctx, "t3")
	settleTrigger(t, eng, ctx, "t2", "t3")

	events, stop := eng.SubscribeAll()
	defer stop()
	r.tools.Post("t1", relaunchCall("t2", "go again"))
	r.tools.Post("t1", relaunchCall("t2", "go again")) // the printed echo
	r.tools.Post("t1", relaunchCall("t3", "go again"))
	New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)

	// Both targets' turns run concurrently; one loop watches both to the
	// control release. Each lands exactly one opener (the echo was
	// deduplicated), so each transcript holds the opener and the turn's own
	// summary.
	released := map[contracts.ThreadID]bool{}
	deadline := time.After(20 * time.Second)
	for !released["t2"] || !released["t3"] {
		select {
		case <-deadline:
			t.Fatalf("relaunch turns never settled: %v", released)
		case ev := <-events:
			if ev.ThreadID != "t2" && ev.ThreadID != "t3" {
				continue
			}
			if c, ok := ev.Payload.(contracts.ThreadControlled); ok && c.Controller == "" {
				released[ev.ThreadID] = true
			}
		}
	}
	for _, id := range []contracts.ThreadID{"t2", "t3"} {
		st, _ := eng.State(ctx, id)
		if st.Turn != 1 || len(st.Messages) != 2 {
			t.Fatalf("%s: turn %d with %d messages, want exactly one relaunch turn", id, st.Turn, len(st.Messages))
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

// A dependency stays readable across its own relaunch — the read rides the
// same TriggeredFrom permission the relaunch does, not the transient control
// claim — and the permission moves when a later trigger overwrites the
// record.
func TestThreadResultsRideTriggeredFrom(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	createThread(t, eng, ctx, "t2")
	createThread(t, eng, ctx, "t3")
	settleTrigger(t, eng, ctx, "t2")

	events, stop := eng.SubscribeAll()
	defer stop()
	if err := r.RelaunchThread(ctx, "t1", "agent-orchestrator", relaunchCall("t2", "go again")); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	// While the relaunch turn runs — control held — the target stays
	// readable; after it settles — control released — it stays readable
	// through the trigger record.
	if _, err := r.ThreadResults(ctx, "t1", "agent-orchestrator", toolbox.Call{Kind: KindThreadResults, Thread: "t2"}); err != nil {
		t.Fatalf("reading a relaunched dependency mid-turn: %v", err)
	}
	waitRelaunchSettled(t, events, "t2", "t1")
	if _, err := r.ThreadResults(ctx, "t1", "agent-orchestrator", toolbox.Call{Kind: KindThreadResults, Thread: "t2"}); err != nil {
		t.Fatalf("reading a relaunched dependency after its turn: %v", err)
	}

	// A new trigger moves the permission: t2 is no longer one of this
	// thread's dependencies, t3 is.
	settleTrigger(t, eng, ctx, "t3")
	if _, err := r.ThreadResults(ctx, "t1", "agent-orchestrator", toolbox.Call{Kind: KindThreadResults, Thread: "t3"}); err != nil {
		t.Fatalf("reading the latest dependency: %v", err)
	}
	if _, err := r.ThreadResults(ctx, "t1", "agent-orchestrator", toolbox.Call{Kind: KindThreadResults, Thread: "t2"}); !errors.Is(err, ErrThreadNotYours) {
		t.Fatalf("reading a dropped dependency err = %v, want ErrThreadNotYours", err)
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

	// The relaunch's turn runs after the caller's turn returns; watched to
	// the control release, the marks it must leave are the claim, the
	// controller-authored opener, and its own summary.
	m := waitRelaunchSettled(t, events, "t2", "t1")
	if !m.claimed || m.opener == "" || !m.summary {
		t.Fatalf("claimed=%v opener=%q summary=%v, want the relaunch to have landed during the turn", m.claimed, m.opener, m.summary)
	}
	st, _ := eng.State(ctx, "t2")
	// The turn ended, so its control was released with it.
	if st.ControlledBy != "" || st.Turn != 1 {
		t.Fatalf("target after the turn: controlledBy=%q turn=%d, want one completed relaunch turn with control released", st.ControlledBy, st.Turn)
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
