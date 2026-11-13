package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// A daemon-wide event recorder: subscribe before the actions under test so
// no event can slip past, then wait on typed payloads. Timeouts bound failure
// only — a passing run never waits on one.
type daemonEventLog struct {
	ch   <-chan contracts.Event
	stop func()
	mu   sync.Mutex
	read []contracts.Event
}

func watchEngine(d *Daemon) *daemonEventLog {
	ch, stop := d.Engine.SubscribeAll()
	return &daemonEventLog{ch: ch, stop: stop}
}

func (r *daemonEventLog) wait(t *testing.T, desc string, pred func(contracts.Event) bool) contracts.Event {
	t.Helper()
	deadline := time.After(20 * time.Second)
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

func (r *daemonEventLog) index(pred func(contracts.Event) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, ev := range r.read {
		if pred(ev) {
			return i
		}
	}
	return -1
}

func (r *daemonEventLog) close() { r.stop() }

// scheduleDaemonFixture wraps a daemon whose sh harness records every prompt
// it is handed, keyed by thread, so a test can assert exactly what a triggered
// schedule's lead saw. Threads in `parked` run a script that blocks until the
// test writes their release file.
type scheduleDaemonFixture struct {
	*Daemon
	promptsMu sync.Mutex
	prompts   map[contracts.ThreadID][]string
	parkMu    sync.Mutex
	parked    map[contracts.ThreadID]bool
	dir       string
}

func newScheduleDaemon(t *testing.T, stateDir string) *scheduleDaemonFixture {
	t.Helper()
	f := &scheduleDaemonFixture{
		prompts: map[contracts.ThreadID][]string{},
		parked:  map[contracts.ThreadID]bool{},
		dir:     t.TempDir(),
	}
	// Non-AT environment flows to harness children by design; the release
	// gate rides on it. Children spawn per turn, so setting it before any
	// turn starts is enough.
	t.Setenv("SCHED_RELEASE_DIR", f.dir)
	runScript := `printf '{"type":"text","text":"report-%s"}\n' "$AT_THREAD_ID"`
	parkScript := `while [ ! -e "$SCHED_RELEASE_DIR/release-$AT_THREAD_ID" ]; do sleep 0.05; done
printf '{"type":"text","text":"report-%s"}\n' "$AT_THREAD_ID"`
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		f.promptsMu.Lock()
		f.prompts[spec.ThreadID] = append(f.prompts[spec.ThreadID], spec.Prompt)
		f.promptsMu.Unlock()
		script := runScript
		f.parkMu.Lock()
		if f.parked[spec.ThreadID] {
			script = parkScript
		}
		f.parkMu.Unlock()
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, "sh", spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: stateDir,
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		Config:   orchestrator.Config{MaxPasses: 1, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	f.Daemon = d
	t.Cleanup(func() {
		f.releaseAll()
		d.Close()
	})
	return f
}

func (f *scheduleDaemonFixture) promptFor(t *testing.T, id contracts.ThreadID) string {
	t.Helper()
	f.promptsMu.Lock()
	defer f.promptsMu.Unlock()
	if len(f.prompts[id]) == 0 {
		t.Fatalf("no harness prompt was recorded for %s", id)
	}
	return f.prompts[id][0]
}

func (f *scheduleDaemonFixture) park(id contracts.ThreadID) {
	f.parkMu.Lock()
	f.parked[id] = true
	f.parkMu.Unlock()
}

func (f *scheduleDaemonFixture) releaseAll() {
	f.parkMu.Lock()
	ids := make([]contracts.ThreadID, 0, len(f.parked))
	for id := range f.parked {
		ids = append(ids, id)
	}
	f.parkMu.Unlock()
	for _, id := range ids {
		f.release(id)
	}
}

func (f *scheduleDaemonFixture) release(id contracts.ThreadID) {
	path := filepath.Join(f.dir, "release-"+string(id))
	_ = os.WriteFile(path, nil, 0o600)
}

// createThread opens a thread over the socket and returns its id.
func createThread(t *testing.T, c *websocket.Conn, title string) contracts.ThreadID {
	t.Helper()
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "t-"+title, "thread.create", map[string]any{"title": title}), &created)
	if created.ThreadID == "" {
		t.Fatalf("thread.create returned no id for %q", title)
	}
	return created.ThreadID
}

// waitForTurnEnd returns a predicate matching the thread's lead reaching a
// terminal status — the durable marker that a turn finished. The turn's
// summary is recorded before its lead settles, so this implies the transcript
// is complete too.
func waitForTurnEnd(id contracts.ThreadID) func(contracts.Event) bool {
	return func(ev contracts.Event) bool {
		p, ok := ev.Payload.(contracts.AgentStatusChanged)
		return ok && ev.ThreadID == id && p.AgentID == "agent-orchestrator" && p.Status.Terminal()
	}
}

func waitForTrigger(id contracts.ThreadID) func(contracts.Event) bool {
	return func(ev contracts.Event) bool {
		_, ok := ev.Payload.(contracts.ThreadTriggered)
		return ok && ev.ThreadID == id
	}
}

func TestDaemonScheduleFiresWaitingThreadWithDependencyResults(t *testing.T) {
	f := newScheduleDaemon(t, t.TempDir())
	c, _ := dialWS(t, f.Daemon)
	watch := watchEngine(f.Daemon)
	t.Cleanup(watch.close)

	a := createThread(t, c, "Alpha")
	b := createThread(t, c, "Bravo")
	dep := createThread(t, c, "Charlie")

	res := call(t, c, "1", "thread.schedule", map[string]any{
		"threadId": a, "dependsOn": []contracts.ThreadID{b, dep}, "prompt": "integrate the work",
	})
	if string(res) != `{"ok":true}` {
		t.Fatalf("thread.schedule reply = %s, want {\"ok\":true}", res)
	}

	// Only Bravo settling is not enough: Charlie has not run at all.
	call(t, c, "2", "thread.turn.start", map[string]any{"threadId": b, "text": "build bravo"})
	st, err := f.Engine.State(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if st.Waiting == nil || len(st.Messages) != 0 {
		t.Fatalf("after bravo only: waiting=%+v messages=%d, want armed and silent", st.Waiting, len(st.Messages))
	}

	call(t, c, "3", "thread.turn.start", map[string]any{"threadId": dep, "text": "build charlie"})
	watch.wait(t, "alpha trigger", waitForTrigger(a))
	watch.wait(t, "alpha turn end", waitForTurnEnd(a))

	// The trigger strictly follows the last dependency's lead going terminal.
	terminal := func(id contracts.ThreadID) int {
		return watch.index(func(ev contracts.Event) bool {
			p, ok := ev.Payload.(contracts.AgentStatusChanged)
			return ok && ev.ThreadID == id && p.Status.Terminal()
		})
	}
	triggerIdx := watch.index(waitForTrigger(a))
	if triggerIdx == -1 || terminal(dep) == -1 || terminal(dep) >= triggerIdx {
		t.Fatalf("trigger did not follow charlie: trigger=%d charlie-terminal=%d", triggerIdx, terminal(dep))
	}

	// The lead saw the scheduled prompt followed by the compiled results: the
	// deps' ids, titles, worktree paths, and final messages.
	prompt := f.promptFor(t, a)
	for _, want := range []string{
		"integrate the work", "RESULTS",
		string(b), "Bravo", "report-" + string(b),
		string(dep), "Charlie", "report-" + string(dep),
		"Worktree:",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("triggered prompt missing %q:\n%s", want, prompt)
		}
	}

	// The transcript keeps the accounting honest: the user's own scheduled
	// prompt opens the turn, the scheduler's results join it without opening
	// another, and the turn is closed so spend can land on it.
	st, err = f.Engine.State(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	authors := make([]string, len(st.Messages))
	for i, m := range st.Messages {
		authors[i] = m.Author
	}
	if strings.Join(authors, ",") != "user,scheduler,orchestrator" {
		t.Fatalf("message authors = %v, want user, scheduler, orchestrator", authors)
	}
	if st.Turn != 1 || len(st.Turns) != 1 || st.Turns[0].EndedAt.IsZero() {
		t.Fatalf("turn = %d turns = %+v, want one closed turn", st.Turn, st.Turns)
	}

	// The list row the board shows: no longer waiting, carrying the trigger.
	var list []struct {
		ID            contracts.ThreadID   `json:"id"`
		Waiting       *json.RawMessage     `json:"waiting,omitempty"`
		TriggeredFrom []contracts.ThreadID `json:"triggeredFrom"`
	}
	json.Unmarshal(call(t, c, "4", "thread.list", nil), &list)
	for _, row := range list {
		if row.ID != a {
			continue
		}
		if row.Waiting != nil {
			t.Fatalf("row still waiting: %+v", row.Waiting)
		}
		if len(row.TriggeredFrom) != 2 || row.TriggeredFrom[0] != b || row.TriggeredFrom[1] != dep {
			t.Fatalf("triggeredFrom = %v, want both deps", row.TriggeredFrom)
		}
	}
}

func TestDaemonScheduleWaitsForEveryDependency(t *testing.T) {
	f := newScheduleDaemon(t, t.TempDir())
	c, _ := dialWS(t, f.Daemon)
	watch := watchEngine(f.Daemon)
	t.Cleanup(watch.close)

	a := createThread(t, c, "Alpha")
	b := createThread(t, c, "Bravo")
	slow := createThread(t, c, "Slow")

	call(t, c, "1", "thread.schedule", map[string]any{
		"threadId": a, "dependsOn": []contracts.ThreadID{b, slow}, "prompt": "integrate",
	})

	// The slow dependency parks mid-turn; its release file is what ends it.
	f.park(slow)
	call(t, c, "2", "thread.turn.start", map[string]any{"threadId": slow, "text": "long work", "detach": true})
	watch.wait(t, "slow lead dispatched", func(ev contracts.Event) bool {
		_, ok := ev.Payload.(contracts.TaskDispatched)
		return ok && ev.ThreadID == slow
	})

	// Bravo's turn runs to completion while slow stays unfinished.
	call(t, c, "3", "thread.turn.start", map[string]any{"threadId": b, "text": "build bravo"})
	st, err := f.Engine.State(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if st.Waiting == nil || len(st.Messages) != 0 {
		t.Fatalf("with bravo done but slow parked: waiting=%+v messages=%d, want armed and silent", st.Waiting, len(st.Messages))
	}

	f.release(slow)
	watch.wait(t, "slow turn end", waitForTurnEnd(slow))
	watch.wait(t, "alpha trigger", waitForTrigger(a))
	watch.wait(t, "alpha turn end", waitForTurnEnd(a))

	prompt := f.promptFor(t, a)
	if !strings.Contains(prompt, "integrate") || !strings.Contains(prompt, "RESULTS") {
		t.Fatalf("prompt = %q, want the scheduled prompt with results", prompt)
	}
	if !strings.Contains(prompt, "report-"+string(slow)) {
		t.Fatalf("prompt missing the slow dep's results:\n%s", prompt)
	}
}

func TestDaemonFiresScheduleImmediatelyWhenDependenciesAlreadyFinished(t *testing.T) {
	f := newScheduleDaemon(t, t.TempDir())
	c, _ := dialWS(t, f.Daemon)
	watch := watchEngine(f.Daemon)
	t.Cleanup(watch.close)

	a := createThread(t, c, "Alpha")
	b := createThread(t, c, "Bravo")

	call(t, c, "1", "thread.turn.start", map[string]any{"threadId": b, "text": "build bravo"})
	call(t, c, "2", "thread.schedule", map[string]any{
		"threadId": a, "dependsOn": []contracts.ThreadID{b}, "prompt": "follow up on bravo",
	})

	watch.wait(t, "alpha trigger", waitForTrigger(a))
	watch.wait(t, "alpha turn end", waitForTurnEnd(a))
	prompt := f.promptFor(t, a)
	if !strings.Contains(prompt, "follow up on bravo") || !strings.Contains(prompt, "report-"+string(b)) {
		t.Fatalf("immediate trigger prompt = %q, want scheduled prompt with the dep's results", prompt)
	}
}

func TestDaemonScheduleRPCRefusals(t *testing.T) {
	f := newScheduleDaemon(t, t.TempDir())
	c, _ := dialWS(t, f.Daemon)

	a := createThread(t, c, "Alpha")
	b := createThread(t, c, "Bravo")

	_, rpcErr := rawCall(t, c, "1", "thread.schedule", map[string]any{
		"threadId": a, "dependsOn": []contracts.ThreadID{"t_missing"}, "prompt": "go",
	})
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "unknown dependency thread") {
		t.Fatalf("unknown dep err = %v, want unknown dependency thread", rpcErr)
	}

	// Arm bravo on alpha, then alpha on bravo: the second arm is a cycle.
	if _, rpcErr := rawCall(t, c, "2", "thread.schedule", map[string]any{
		"threadId": b, "dependsOn": []contracts.ThreadID{a}, "prompt": "wait for alpha",
	}); rpcErr != nil {
		t.Fatalf("arm bravo: %v", rpcErr)
	}
	_, rpcErr = rawCall(t, c, "3", "thread.schedule", map[string]any{
		"threadId": a, "dependsOn": []contracts.ThreadID{b}, "prompt": "wait for bravo",
	})
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "never fire") {
		t.Fatalf("cycle err = %v, want never fire", rpcErr)
	}

	// Cancelling a cycle that was never drawn is a clean error, and a live
	// schedule cancels cleanly with the exact reply the desktop expects.
	if _, rpcErr := rawCall(t, c, "4", "thread.unschedule", map[string]any{"threadId": a}); rpcErr == nil || !strings.Contains(rpcErr.Message, "no schedule to cancel") {
		t.Fatalf("unschedule without schedule err = %v, want no schedule to cancel", rpcErr)
	}
	res, rpcErr := rawCall(t, c, "5", "thread.unschedule", map[string]any{"threadId": b})
	if rpcErr != nil {
		t.Fatalf("unschedule: %v", rpcErr)
	}
	if string(res) != `{"ok":true}` {
		t.Fatalf("thread.unschedule reply = %s, want {\"ok\":true}", res)
	}
	st, err := f.Engine.State(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if st.Waiting != nil {
		t.Fatalf("waiting = %+v, want disarmed", st.Waiting)
	}
}

// TestDaemonRecoversArmedSchedulesOnRestart covers the crash window between a
// dependency finishing and the trigger: the durable state on disk holds a
// waiting thread whose dependencies are all done, and the rebooted daemon
// starts its turn.
func TestDaemonRecoversArmedSchedulesOnRestart(t *testing.T) {
	stateDir := t.TempDir()
	ctx := context.Background()

	// Build the crash-time state directly: a finished dependency and an armed
	// thread, durable in the event log no live scheduler ever saw.
	store, err := domain.NewFileStore(filepath.Join(stateDir, "threads"))
	if err != nil {
		t.Fatal(err)
	}
	eng := domain.NewEngine(store, nil)
	eng.Start(ctx)
	for _, id := range []contracts.ThreadID{"restart-b", "restart-a"} {
		if err := eng.Dispatch(ctx, contracts.Command{
			ID: "c-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id, Title: string(id),
		}); err != nil {
			t.Fatal(err)
		}
	}
	dispatch := func(cmd contracts.Command) {
		t.Helper()
		if err := eng.Dispatch(ctx, cmd); err != nil {
			t.Fatalf("dispatch %s: %v", cmd.Type, err)
		}
	}
	dispatch(contracts.Command{ID: "m", Type: contracts.CmdTurnStart, ThreadID: "restart-b", Text: "run bravo"})
	dispatch(contracts.Command{
		ID: "d", Type: contracts.CmdTaskDispatch, ThreadID: "restart-b",
		Dispatch: &contracts.DispatchTask{AgentID: "agent-orchestrator", Role: contracts.RoleOrchestrator, Task: "lead"},
	})
	dispatch(contracts.Command{
		ID: "s", Type: contracts.CmdAgentStatusSet, ThreadID: "restart-b",
		Status: &contracts.AgentStatusChanged{AgentID: "agent-orchestrator", Status: contracts.AgentSucceeded},
	})
	dispatch(contracts.Command{
		ID: "f", Type: contracts.CmdMessageAppend, ThreadID: "restart-b",
		Message: &contracts.MessageSent{MessageID: "fm", Author: "orchestrator", Text: "bravo finished before the crash"},
	})
	dispatch(contracts.Command{
		ID: "arm", Type: contracts.CmdThreadSchedule, ThreadID: "restart-a",
		Schedule: &contracts.ScheduleSpec{DependsOn: []contracts.ThreadID{"restart-b"}, Prompt: "run after the crash"},
	})
	eng.Close()

	f := newScheduleDaemon(t, stateDir)
	watch := watchEngine(f.Daemon)
	t.Cleanup(watch.close)

	watch.wait(t, "restart trigger", waitForTrigger("restart-a"))
	watch.wait(t, "restart turn end", waitForTurnEnd("restart-a"))

	prompt := f.promptFor(t, "restart-a")
	for _, want := range []string{"run after the crash", "RESULTS", "bravo finished before the crash"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("recovered prompt missing %q:\n%s", want, prompt)
		}
	}
	st, err := f.Engine.State(context.Background(), "restart-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.TriggeredFrom) != 1 || st.TriggeredFrom[0] != "restart-b" {
		t.Fatalf("triggeredFrom = %v, want the restarted dep", st.TriggeredFrom)
	}
}
