package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/steering"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// newScheduleFixture builds the light dispatcher: a real engine with threads
// t-a, t-b and t-c. No harness, so only the scheduling surface (validation,
// arming, listing) can be exercised; turn-bearing cases use
// newScheduleTurnFixture below.
func newScheduleFixture(t *testing.T) (*domain.Engine, *Dispatcher) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:  eng,
		Logs:    logstore.New(t.TempDir()),
		Mailbox: contextstore.NewMailbox(),
	})
	for _, id := range []contracts.ThreadID{"t-a", "t-b", "t-c"} {
		if err := eng.Dispatch(ctx, contracts.Command{
			ID: "c-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id, Title: string(id),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return eng, NewDispatcher(DispatcherDeps{Engine: eng, Runner: runner})
}

func scheduleRPC(t *testing.T, disp *Dispatcher, threadID string, deps []string, prompt string) (map[string]any, error) {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"threadId": threadID, "dependsOn": deps, "prompt": prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := disp.Handle(context.Background(), Request{Method: "thread.schedule", Params: params})
	if err != nil {
		return nil, err
	}
	res, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("thread.schedule result = %T, want a map", out)
	}
	return res, nil
}

func unscheduleRPC(t *testing.T, disp *Dispatcher, threadID string) (map[string]any, error) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"threadId": threadID})
	if err != nil {
		t.Fatal(err)
	}
	out, err := disp.Handle(context.Background(), Request{Method: "thread.unschedule", Params: params})
	if err != nil {
		return nil, err
	}
	res, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("thread.unschedule result = %T, want a map", out)
	}
	return res, nil
}

// armViaEngine schedules through the engine, bypassing the RPC kick, for tests
// that need an armed thread without the immediate evaluation.
func armViaEngine(t *testing.T, eng *domain.Engine, threadID string, deps ...string) {
	t.Helper()
	cmd := contracts.Command{
		ID: "s-" + threadID, Type: contracts.CmdThreadSchedule, ThreadID: contracts.ThreadID(threadID),
		Schedule: &contracts.ScheduleSpec{DependsOn: toThreadIDs(deps), Prompt: "after " + strings.Join(deps, "+")},
	}
	if err := eng.Dispatch(context.Background(), cmd); err != nil {
		t.Fatalf("engine schedule %s: %v", threadID, err)
	}
}

func toThreadIDs(ids []string) []contracts.ThreadID {
	out := make([]contracts.ThreadID, len(ids))
	for i, id := range ids {
		out[i] = contracts.ThreadID(id)
	}
	return out
}

// finishThread drives a thread to a finished state through raw engine
// commands: a recorded turn, a terminal lead agent, and a final message. No
// harness runs, so no git or subprocess is involved.
func finishThread(t *testing.T, eng *domain.Engine, id, finalText string) {
	t.Helper()
	ctx := context.Background()
	dispatch := func(cmd contracts.Command) {
		t.Helper()
		if err := eng.Dispatch(ctx, cmd); err != nil {
			t.Fatalf("dispatch %s on %s: %v", cmd.Type, id, err)
		}
	}
	dispatch(contracts.Command{ID: "m-" + id, Type: contracts.CmdTurnStart, ThreadID: contracts.ThreadID(id), Text: "run " + id})
	dispatch(contracts.Command{
		ID:       "d-" + id,
		Type:     contracts.CmdTaskDispatch,
		ThreadID: contracts.ThreadID(id),
		Dispatch: &contracts.DispatchTask{AgentID: "agent-orchestrator", Role: contracts.RoleOrchestrator, Task: "lead"},
	})
	dispatch(contracts.Command{
		ID:       "st-" + id,
		Type:     contracts.CmdAgentStatusSet,
		ThreadID: contracts.ThreadID(id),
		Status:   &contracts.AgentStatusChanged{AgentID: "agent-orchestrator", Status: contracts.AgentSucceeded},
	})
	dispatch(contracts.Command{
		ID:       "f-" + id,
		Type:     contracts.CmdMessageAppend,
		ThreadID: contracts.ThreadID(id),
		Message:  &contracts.MessageSent{MessageID: "fm-" + id, Author: "orchestrator", Text: finalText},
	})
}

func TestScheduleRPCArmsThreadAndListReportsIt(t *testing.T) {
	eng, disp := newScheduleFixture(t)

	res, err := scheduleRPC(t, disp, "t-a", []string{"t-b", "t-c"}, "integrate both")
	if err != nil {
		t.Fatalf("thread.schedule: %v", err)
	}
	if len(res) != 1 || res["ok"] != true {
		t.Fatalf("reply = %v, want exactly {ok:true}", res)
	}

	st, _ := eng.State(context.Background(), "t-a")
	if st.Waiting == nil || st.Waiting.Prompt != "integrate both" ||
		len(st.Waiting.DependsOn) != 2 || st.Waiting.DependsOn[0] != "t-b" || st.Waiting.DependsOn[1] != "t-c" {
		t.Fatalf("waiting = %+v, want the armed spec", st.Waiting)
	}

	out, err := disp.Handle(context.Background(), Request{Method: "thread.list"})
	if err != nil {
		t.Fatalf("thread.list: %v", err)
	}
	var row *ThreadSummary
	for _, s := range out.([]ThreadSummary) {
		if s.ID == "t-a" {
			row = &s
		}
	}
	if row == nil {
		t.Fatal("t-a missing from thread.list")
	}
	if row.Waiting == nil || len(row.Waiting.DependsOn) != 2 || row.Waiting.Prompt != "integrate both" {
		t.Fatalf("summary waiting = %+v, want the armed spec", row.Waiting)
	}
	if row.TriggeredFrom != nil || row.ControlledBy != "" {
		t.Fatalf("fresh schedule row = %+v, want no trigger or controller", row)
	}
}

func TestScheduleRPCRefusesUnknownTargetAndDependencies(t *testing.T) {
	_, disp := newScheduleFixture(t)

	if _, err := scheduleRPC(t, disp, "t-missing", []string{"t-b"}, "go"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown thread err = %v, want does not exist", err)
	}
	if _, err := scheduleRPC(t, disp, "t-a", []string{"t-b", "t-ghost"}, "go"); err == nil || !strings.Contains(err.Error(), "unknown dependency thread \"t-ghost\"") {
		t.Fatalf("unknown dependency err = %v, want unknown dependency", err)
	}

	// A refused schedule arms nothing.
	st := threadState(t, disp, "t-a")
	if st.Waiting != nil {
		t.Fatalf("refused schedule armed the thread: %+v", st.Waiting)
	}
}

func threadState(t *testing.T, disp *Dispatcher, id contracts.ThreadID) *domain.ThreadState {
	t.Helper()
	st, err := disp.engine.State(context.Background(), id)
	if err != nil {
		t.Fatalf("state %s: %v", id, err)
	}
	return st
}

func TestScheduleRPCRefusesWaitingChains(t *testing.T) {
	eng, disp := newScheduleFixture(t)
	ctx := context.Background()

	// A direct cycle: t-b already waits on t-a, so t-a cannot wait on t-b.
	armViaEngine(t, eng, "t-b", "t-a")
	if _, err := scheduleRPC(t, disp, "t-a", []string{"t-b"}, "go"); err == nil || !strings.Contains(err.Error(), "never fire") {
		t.Fatalf("direct cycle err = %v, want never fire", err)
	}

	// A transitive one: t-c waits on t-b, which waits on t-a.
	armViaEngine(t, eng, "t-c", "t-b")
	if _, err := scheduleRPC(t, disp, "t-a", []string{"t-c"}, "go"); err == nil || !strings.Contains(err.Error(), "never fire") {
		t.Fatalf("transitive cycle err = %v, want never fire", err)
	}

	// The decider-side rules surface as clean RPC errors too.
	if _, err := scheduleRPC(t, disp, "t-b", []string{"t-b"}, "go"); err == nil || !strings.Contains(err.Error(), "cannot wait on itself") {
		t.Fatalf("self dependency err = %v, want self refusal", err)
	}
	if _, err := scheduleRPC(t, disp, "t-b", []string{"t-a", "t-a"}, "go"); err == nil || !strings.Contains(err.Error(), "duplicate dependency") {
		t.Fatalf("duplicate dependency err = %v, want duplicate refusal", err)
	}
	if _, err := scheduleRPC(t, disp, "t-a", []string{}, "go"); err == nil || !strings.Contains(err.Error(), "at least one thread") {
		t.Fatalf("empty deps err = %v, want empty refusal", err)
	}

	// A diamond is not a cycle: t-c may wait on both t-a and t-b.
	stA, _ := eng.State(ctx, "t-a")
	if stA.Waiting != nil {
		t.Fatalf("waiting chains left t-a armed: %+v", stA.Waiting)
	}
}

// TestScheduleDepFinishedPredicate pins the finished rule directly, so each
// arm is explicit rather than only reachable through a full turn flow: a
// deleted dep is finished, a thread that never ran or whose turn is still
// open is not, and a turn with only non-terminal agents is not.
func TestScheduleDepFinishedPredicate(t *testing.T) {
	deleted := domain.NewThreadState("x")
	if !scheduleDepFinished(deleted) {
		t.Fatal("deleted dep read as unfinished")
	}
	neverRun := domain.NewThreadState("x")
	neverRun.Exists = true
	if scheduleDepFinished(neverRun) {
		t.Fatal("never-run dep read as finished")
	}
	startedNotEnded := domain.NewThreadState("x")
	startedNotEnded.Exists = true
	startedNotEnded.Turn = 1
	startedNotEnded.Turns = append(startedNotEnded.Turns, domain.TurnUsage{Turn: 1})
	if scheduleDepFinished(startedNotEnded) {
		t.Fatal("turn without an end read as finished")
	}
	agentsLive := domain.NewThreadState("x")
	agentsLive.Exists = true
	agentsLive.Turn = 1
	agentsLive.Agents["a1"] = contracts.Agent{ID: "a1", Status: contracts.AgentRunning}
	agentsLive.Turns = append(agentsLive.Turns, domain.TurnUsage{Turn: 1, EndedAt: time.Now()})
	if scheduleDepFinished(agentsLive) {
		t.Fatal("turn with a live agent read as finished")
	}
	ended := domain.NewThreadState("x")
	ended.Exists = true
	ended.Turn = 1
	ended.Agents["a1"] = contracts.Agent{ID: "a1", Status: contracts.AgentSucceeded}
	ended.Turns = append(ended.Turns, domain.TurnUsage{Turn: 1, EndedAt: time.Now()})
	if !scheduleDepFinished(ended) {
		t.Fatal("ended turn with settled agents read as unfinished")
	}
}

func TestUnscheduleRPCDisarmsThread(t *testing.T) {
	eng, disp := newScheduleFixture(t)

	if _, err := scheduleRPC(t, disp, "t-a", []string{"t-b"}, "later"); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	res, err := unscheduleRPC(t, disp, "t-a")
	if err != nil {
		t.Fatalf("unschedule: %v", err)
	}
	if len(res) != 1 || res["ok"] != true {
		t.Fatalf("reply = %v, want exactly {ok:true}", res)
	}
	st, _ := eng.State(context.Background(), "t-a")
	if st.Waiting != nil {
		t.Fatalf("waiting = %+v, want disarmed", st.Waiting)
	}
	if _, err := unscheduleRPC(t, disp, "t-a"); err == nil || !strings.Contains(err.Error(), "no schedule to cancel") {
		t.Fatalf("second unschedule err = %v, want no schedule to cancel", err)
	}
}

// scheduleTurnFixture is the full turn pipeline over a scripted sh harness:
// engine, runner, orchestrator, dispatcher. The driver records every prompt
// it is handed, keyed by thread, so tests can assert exactly what a triggered
// schedule's lead saw.
type scheduleTurnFixture struct {
	eng  *domain.Engine
	disp *Dispatcher

	promptsMu sync.Mutex
	prompts   map[contracts.ThreadID][]string
}

func newScheduleTurnFixture(t *testing.T) *scheduleTurnFixture {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	f := &scheduleTurnFixture{eng: eng, prompts: map[contracts.ThreadID][]string{}}
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		f.recordPrompt(spec.ThreadID, spec.Prompt)
		script := `printf '{"type":"text","text":"report-%s"}\n' "$AT_THREAD_ID"`
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, "sh", spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"), t.TempDir())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:    eng,
		Registry:  providers.NewRegistry(driver),
		Roles:     contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs:      logstore.New(t.TempDir()),
		Ledger:    usage.NewLedger(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: contextstore.NewInheritedContext(t.TempDir()),
		Settings:  store,
		Tools:     toolbox.New(),
		Steering:  steering.New(),
		Cfg:       orchestrator.Config{MaxPasses: 1, MaxConcurrency: 2},
	})
	for _, id := range []contracts.ThreadID{"t-a", "t-b", "t-c"} {
		if err := eng.Dispatch(ctx, contracts.Command{
			ID: "c-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id, Title: string(id),
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.disp = NewDispatcher(DispatcherDeps{
		Engine: eng, Runner: runner, Orchestrator: orchestrator.New(runner),
		Registry: providers.NewRegistry(driver), Roles: contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs: logstore.New(t.TempDir()), Ledger: usage.NewLedger(), Settings: store,
		BaseCtx: ctx,
	})
	return f
}

func (f *scheduleTurnFixture) recordPrompt(id contracts.ThreadID, prompt string) {
	f.promptsMu.Lock()
	f.prompts[id] = append(f.prompts[id], prompt)
	f.promptsMu.Unlock()
}

func (f *scheduleTurnFixture) promptFor(t *testing.T, id contracts.ThreadID) string {
	t.Helper()
	f.promptsMu.Lock()
	defer f.promptsMu.Unlock()
	if len(f.prompts[id]) == 0 {
		t.Fatalf("no harness prompt was recorded for %s", id)
	}
	return f.prompts[id][0]
}

// eventRecorder turns an engine-wide subscription into a waitable, ordered
// log. Subscribe before the actions under test so nothing can slip past.
type eventRecorder struct {
	ch   <-chan contracts.Event
	stop func()
	mu   sync.Mutex
	read []contracts.Event
}

func (f *scheduleTurnFixture) watch() *eventRecorder {
	ch, stop := f.eng.SubscribeAll()
	return &eventRecorder{ch: ch, stop: stop}
}

// wait blocks until an event matching pred arrives, recording every event it
// reads on the way. Timeouts bound failure only; a passing run never waits.
func (r *eventRecorder) wait(t *testing.T, desc string, pred func(contracts.Event) bool) contracts.Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", desc)
		case ev := <-r.ch:
			r.mu.Lock()
			r.read = append(r.read, ev)
			r.mu.Unlock()
			if pred(ev) {
				return ev
			}
		}
	}
}

// index returns the read position of the first event matching pred, or -1.
func (r *eventRecorder) index(pred func(contracts.Event) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, ev := range r.read {
		if pred(ev) {
			return i
		}
	}
	return -1
}

// snapshot copies the events read so far.
func (r *eventRecorder) snapshot() []contracts.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contracts.Event(nil), r.read...)
}

func (r *eventRecorder) close() { r.stop() }

// turnStarted drives a foreground user turn over the dispatcher, the way
// thread.turn.start does, and blocks until it finishes.
func turnStarted(t *testing.T, disp *Dispatcher, threadID, text string) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"threadId": threadID, "text": text})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disp.Handle(context.Background(), Request{Method: "thread.turn.start", Params: params}); err != nil {
		t.Fatalf("turn on %s: %v", threadID, err)
	}
}

func TestScheduleFiresImmediatelyWhenDependenciesAlreadyFinished(t *testing.T) {
	f := newScheduleTurnFixture(t)
	watch := f.watch()
	t.Cleanup(watch.close)

	finishThread(t, f.eng, "t-b", "b shipped the thing")
	if _, err := scheduleRPC(t, f.disp, "t-a", []string{"t-b"}, "integrate b"); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	watch.wait(t, "t-a trigger", func(ev contracts.Event) bool {
		_, ok := ev.Payload.(contracts.ThreadTriggered)
		return ok && ev.ThreadID == "t-a"
	})
	watch.wait(t, "t-a turn end", func(ev contracts.Event) bool {
		p, ok := ev.Payload.(contracts.MessageSent)
		return ok && ev.ThreadID == "t-a" && p.Author == "orchestrator"
	})

	st, _ := f.eng.State(context.Background(), "t-a")
	if len(st.Messages) != 3 {
		t.Fatalf("messages = %+v, want user prompt, scheduler results, orchestrator summary", st.Messages)
	}
	if st.Messages[0].Author != "user" || st.Messages[0].Text != "integrate b" {
		t.Fatalf("first message = %+v, want the user's scheduled prompt", st.Messages[0])
	}
	if st.Messages[1].Author != AuthorScheduler {
		t.Fatalf("results message author = %q, want %q", st.Messages[1].Author, AuthorScheduler)
	}
	if !strings.Contains(st.Messages[1].Text, "b shipped the thing") {
		t.Fatalf("results message = %q, want the dependency's final message", st.Messages[1].Text)
	}
	if st.Messages[2].Author != "orchestrator" {
		t.Fatalf("final message author = %q, want orchestrator", st.Messages[2].Author)
	}
	// Exactly one turn: the trigger's own. The scheduler note must not have
	// inflated the count, and the turn must be closed (usage can land on it).
	if st.Turn != 1 || len(st.Turns) != 1 || st.Turns[0].EndedAt.IsZero() {
		t.Fatalf("turn = %d, turns = %+v, want one closed turn", st.Turn, st.Turns)
	}

	prompt := f.promptFor(t, "t-a")
	for _, want := range []string{"integrate b", "RESULTS", "t-b", "Worktree:", "b shipped the thing"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("triggered prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestSchedulerTriggersWhenEveryDependencyTurnEnds(t *testing.T) {
	f := newScheduleTurnFixture(t)
	stop := f.disp.StartScheduler(context.Background())
	t.Cleanup(stop)
	watch := f.watch()
	t.Cleanup(watch.close)

	if _, err := scheduleRPC(t, f.disp, "t-a", []string{"t-b", "t-c"}, "integrate b and c"); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	// One dependency finishing is not enough.
	turnStarted(t, f.disp, "t-b", "build b")
	st, _ := f.eng.State(context.Background(), "t-a")
	if st.Waiting == nil || len(st.Messages) != 0 {
		t.Fatalf("after b only: waiting=%+v messages=%d, want still armed and silent", st.Waiting, len(st.Messages))
	}

	turnStarted(t, f.disp, "t-c", "build c")
	trig := watch.wait(t, "t-a trigger", func(ev contracts.Event) bool {
		_, ok := ev.Payload.(contracts.ThreadTriggered)
		return ok && ev.ThreadID == "t-a"
	})
	if p, ok := trig.Payload.(contracts.ThreadTriggered); !ok || len(p.DependsOn) != 2 {
		t.Fatalf("trigger = %#v, want both dependencies recorded", trig.Payload)
	}
	watch.wait(t, "t-a turn end", func(ev contracts.Event) bool {
		p, ok := ev.Payload.(contracts.MessageSent)
		return ok && ev.ThreadID == "t-a" && p.Author == "orchestrator"
	})

	// The trigger strictly follows the last dependency's lead going terminal:
	// it fired because c ended, never because b alone had.
	cEnded := watch.index(func(ev contracts.Event) bool {
		p, ok := ev.Payload.(contracts.AgentStatusChanged)
		return ok && ev.ThreadID == "t-c" && p.Status.Terminal()
	})
	if cEnded == -1 || cEnded >= watch.index(func(ev contracts.Event) bool {
		_, ok := ev.Payload.(contracts.ThreadTriggered)
		return ok && ev.ThreadID == "t-a"
	}) {
		t.Fatalf("trigger did not follow the last dependency: c-terminal idx=%d", cEnded)
	}

	// The recorded results note: it was read while waiting past it, so look
	// for it in the log rather than waiting for a second copy.
	resultsIdx := watch.index(func(ev contracts.Event) bool {
		p, ok := ev.Payload.(contracts.MessageSent)
		return ok && ev.ThreadID == "t-a" && p.Author == AuthorScheduler
	})
	if resultsIdx == -1 {
		t.Fatal("no scheduler results message was recorded")
	}
	var results contracts.MessageSent
	for _, ev := range watch.snapshot() {
		if p, ok := ev.Payload.(contracts.MessageSent); ok && ev.ThreadID == "t-a" && p.Author == AuthorScheduler {
			results = p
		}
	}
	for _, dep := range []string{"t-b", "t-c"} {
		if !strings.Contains(results.Text, string(dep)) {
			t.Fatalf("results = %q, want a section for %s", results.Text, dep)
		}
	}
	prompt := f.promptFor(t, "t-a")
	if !strings.Contains(prompt, "integrate b and c") || !strings.Contains(prompt, "RESULTS") {
		t.Fatalf("prompt missing scheduled text or results:\n%s", prompt)
	}
}

func TestSchedulerRecoversArmedSchedulesAtStart(t *testing.T) {
	f := newScheduleTurnFixture(t)
	// The scheduler is NOT running yet: the engine-side arm below records an
	// armed thread whose dependency already finished — the state a crash
	// between a dependency finishing and the trigger leaves behind.
	finishThread(t, f.eng, "t-b", "b finished before the crash")
	armViaEngine(t, f.eng, "t-a", "t-b")

	watch := f.watch()
	t.Cleanup(watch.close)
	stop := f.disp.StartScheduler(context.Background())
	defer stop()

	watch.wait(t, "t-a trigger", func(ev contracts.Event) bool {
		_, ok := ev.Payload.(contracts.ThreadTriggered)
		return ok && ev.ThreadID == "t-a"
	})
	watch.wait(t, "t-a turn end", func(ev contracts.Event) bool {
		p, ok := ev.Payload.(contracts.MessageSent)
		return ok && ev.ThreadID == "t-a" && p.Author == "orchestrator"
	})
	prompt := f.promptFor(t, "t-a")
	if !strings.Contains(prompt, "b finished before the crash") {
		t.Fatalf("recovered prompt missing the dep's results:\n%s", prompt)
	}
}
