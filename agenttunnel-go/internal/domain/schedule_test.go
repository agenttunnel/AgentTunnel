package domain

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func scheduleCmd(id contracts.ThreadID, deps ...contracts.ThreadID) contracts.Command {
	return contracts.Command{
		Type:     contracts.CmdThreadSchedule,
		ThreadID: id,
		Schedule: &contracts.ScheduleSpec{DependsOn: deps, Prompt: "after them"},
	}
}

func liveThread(id contracts.ThreadID) *ThreadState {
	s := NewThreadState(id)
	s.Exists = true
	return s
}

func waitingThread(id contracts.ThreadID, deps ...contracts.ThreadID) *ThreadState {
	s := liveThread(id)
	s.Waiting = &contracts.ScheduleSpec{DependsOn: deps, Prompt: "queued"}
	return s
}

func TestDecideThreadScheduleArmsTheThread(t *testing.T) {
	evs, err := Decide(scheduleCmd("t1", "t2", "t3"), liveThread("t1"))
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	sch, ok := evs[0].(contracts.ThreadScheduled)
	if !ok {
		t.Fatalf("want ThreadScheduled, got %T", evs[0])
	}
	if sch.Prompt != "after them" || len(sch.DependsOn) != 2 || sch.DependsOn[0] != "t2" || sch.DependsOn[1] != "t3" {
		t.Fatalf("scheduled = %+v, want the spec as given", sch)
	}
}

// TestDecideThreadScheduleRefusals covers each rule the decider can check on
// one thread at a time. Dependency existence and cycles span threads, so they
// belong to the scheduler layer that arms the command, not here.
func TestDecideThreadScheduleRefusals(t *testing.T) {
	busy := liveThread("t1")
	busy.Agents["a1"] = contracts.Agent{ID: "a1", Status: contracts.AgentRunning}
	pending := liveThread("t1")
	pending.Agents["a1"] = contracts.Agent{ID: "a1", Status: contracts.AgentPending}
	idle := liveThread("t1")
	idle.Agents["a1"] = contracts.Agent{ID: "a1", Status: contracts.AgentSucceeded}
	sub := liveThread("t1")
	sub.ParentThreadID = "t0"
	deleted := NewThreadState("t1")
	deleted.Deleted = true
	noPayload := scheduleCmd("t1", "t2")
	noPayload.Schedule = nil
	noPrompt := scheduleCmd("t1", "t2")
	noPrompt.Schedule = &contracts.ScheduleSpec{DependsOn: []contracts.ThreadID{"t2"}}

	cases := []struct {
		name string
		cmd  contracts.Command
		s    *ThreadState
		want error
	}{
		{"missing thread", scheduleCmd("t1", "t2"), NewThreadState("t1"), ErrThreadNotFound},
		{"deleted thread", scheduleCmd("t1", "t2"), deleted, ErrThreadNotFound},
		{"missing payload", noPayload, liveThread("t1"), ErrInvalidCommand},
		{"empty prompt", noPrompt, liveThread("t1"), ErrInvalidCommand},
		{"no dependencies", scheduleCmd("t1"), liveThread("t1"), ErrInvalidCommand},
		{"self dependency", scheduleCmd("t1", "t2", "t1"), liveThread("t1"), ErrInvalidCommand},
		{"duplicate dependency", scheduleCmd("t1", "t2", "t2"), liveThread("t1"), ErrInvalidCommand},
		{"already waiting", scheduleCmd("t1", "t2"), waitingThread("t1", "t2"), ErrInvalidCommand},
		{"subthread", scheduleCmd("t1", "t2"), sub, ErrInvalidCommand},
		{"busy running agent", scheduleCmd("t1", "t2"), busy, ErrInvalidCommand},
		{"busy pending agent", scheduleCmd("t1", "t2"), pending, ErrInvalidCommand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evs, err := Decide(tc.cmd, tc.s)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(evs) != 0 {
				t.Fatalf("a refused schedule recorded %#v", evs)
			}
		})
	}

	// A finished agent does not hold the thread back.
	if _, err := Decide(scheduleCmd("t1", "t2"), idle); err != nil {
		t.Fatalf("idle thread refused: %v", err)
	}
}

func TestDecideThreadScheduleCancel(t *testing.T) {
	evs, err := Decide(contracts.Command{Type: contracts.CmdThreadScheduleCancel, ThreadID: "t1"}, waitingThread("t1", "t2", "t3"))
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cancelled, ok := evs[0].(contracts.ThreadScheduleCancelled)
	if !ok || len(cancelled.DependsOn) != 2 || cancelled.DependsOn[0] != "t2" || cancelled.DependsOn[1] != "t3" {
		t.Fatalf("want ThreadScheduleCancelled with the waiting deps, got %#v", evs)
	}

	for _, tc := range []struct {
		name string
		cmd  contracts.Command
		s    *ThreadState
		want error
	}{
		{"not waiting", contracts.Command{Type: contracts.CmdThreadScheduleCancel, ThreadID: "t1"}, liveThread("t1"), ErrInvalidCommand},
		{"missing thread", contracts.Command{Type: contracts.CmdThreadScheduleCancel, ThreadID: "t1"}, NewThreadState("t1"), ErrThreadNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decide(tc.cmd, tc.s); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDecideThreadControl(t *testing.T) {
	evs, err := Decide(contracts.Command{Type: contracts.CmdThreadControl, ThreadID: "t1", Controller: "t2"}, liveThread("t1"))
	if err != nil {
		t.Fatalf("set control: %v", err)
	}
	if ctrl, ok := evs[0].(contracts.ThreadControlled); !ok || ctrl.Controller != "t2" {
		t.Fatalf("want ThreadControlled{t2}, got %#v", evs)
	}

	controlled := liveThread("t1")
	controlled.ControlledBy = "t2"
	evs, err = Decide(contracts.Command{Type: contracts.CmdThreadControl, ThreadID: "t1"}, controlled)
	if err != nil {
		t.Fatalf("release control: %v", err)
	}
	if ctrl, ok := evs[0].(contracts.ThreadControlled); !ok || ctrl.Controller != "" {
		t.Fatalf("want ThreadControlled{released}, got %#v", evs)
	}

	for _, tc := range []struct {
		name       string
		controller contracts.ThreadID
		s          *ThreadState
	}{
		{"self control", "t1", liveThread("t1")},
		{"release when not controlled", "", liveThread("t1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decide(contracts.Command{Type: contracts.CmdThreadControl, ThreadID: "t1", Controller: tc.controller}, tc.s)
			if !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("err = %v, want ErrInvalidCommand", err)
			}
		})
	}

	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadControl, ThreadID: "t1", Controller: "t2"}, NewThreadState("t1")); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("unknown thread: err = %v, want ErrThreadNotFound", err)
	}
}

// The turn that runs a waiting thread is its trigger: the dependency record is
// written before the prompt, in the same durable write.
func TestDecideTurnStartOnWaitingThreadTriggersFirst(t *testing.T) {
	evs, err := Decide(contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "go"}, waitingThread("t1", "t2"))
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("payloads = %#v, want trigger then message", evs)
	}
	if trig, ok := evs[0].(contracts.ThreadTriggered); !ok || len(trig.DependsOn) != 1 || trig.DependsOn[0] != "t2" {
		t.Fatalf("first payload = %#v, want ThreadTriggered{[t2]}", evs[0])
	}
	if _, ok := evs[1].(contracts.MessageSent); !ok {
		t.Fatalf("second payload = %T, want MessageSent", evs[1])
	}

	waitingDraft := waitingThread("t1", "t2")
	waitingDraft.Draft = true
	evs, err = Decide(contracts.Command{ID: "m2", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "go"}, waitingDraft)
	if err != nil {
		t.Fatalf("turn on a waiting draft: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("payloads = %#v, want trigger, message, draft-started", evs)
	}
	if _, ok := evs[0].(contracts.ThreadTriggered); !ok {
		t.Fatalf("first payload = %T, want ThreadTriggered", evs[0])
	}
	if _, ok := evs[1].(contracts.MessageSent); !ok {
		t.Fatalf("second payload = %T, want MessageSent", evs[1])
	}
	if _, ok := evs[2].(contracts.DraftStarted); !ok {
		t.Fatalf("third payload = %T, want DraftStarted", evs[2])
	}
}

func TestProjectScheduleLifecycle(t *testing.T) {
	s := NewThreadState("t1")
	apply := func(p contracts.EventPayload) {
		t.Helper()
		if err := Project(s, contracts.Event{Seq: 1, ThreadID: "t1", Payload: p}); err != nil {
			t.Fatalf("project %T: %v", p, err)
		}
	}

	apply(contracts.ThreadScheduled{DependsOn: []contracts.ThreadID{"t2", "t3"}, Prompt: "after them"})
	if s.Waiting == nil || s.Waiting.Prompt != "after them" || len(s.Waiting.DependsOn) != 2 {
		t.Fatalf("after schedule: waiting = %+v", s.Waiting)
	}

	apply(contracts.ThreadTriggered{DependsOn: []contracts.ThreadID{"t2", "t3"}})
	if s.Waiting != nil {
		t.Fatal("trigger left the thread waiting")
	}
	if len(s.TriggeredFrom) != 2 || s.TriggeredFrom[0] != "t2" || s.TriggeredFrom[1] != "t3" {
		t.Fatalf("triggeredFrom = %v, want [t2 t3]", s.TriggeredFrom)
	}

	// A later schedule overwrites the trigger record when it fires.
	apply(contracts.ThreadScheduled{DependsOn: []contracts.ThreadID{"t4"}, Prompt: "after t4"})
	apply(contracts.ThreadTriggered{DependsOn: []contracts.ThreadID{"t4"}})
	if s.Waiting != nil || len(s.TriggeredFrom) != 1 || s.TriggeredFrom[0] != "t4" {
		t.Fatalf("after second trigger: waiting=%+v triggeredFrom=%v", s.Waiting, s.TriggeredFrom)
	}

	// Cancelling disarms without running, and leaves the trigger record alone.
	apply(contracts.ThreadScheduled{DependsOn: []contracts.ThreadID{"t5"}, Prompt: "after t5"})
	apply(contracts.ThreadScheduleCancelled{DependsOn: []contracts.ThreadID{"t5"}})
	if s.Waiting != nil {
		t.Fatal("cancel left the thread waiting")
	}
	if len(s.TriggeredFrom) != 1 || s.TriggeredFrom[0] != "t4" {
		t.Fatalf("cancel rewrote the trigger record: %v", s.TriggeredFrom)
	}

	apply(contracts.ThreadControlled{Controller: "t9"})
	if s.ControlledBy != "t9" {
		t.Fatalf("controlledBy = %q, want t9", s.ControlledBy)
	}
	apply(contracts.ThreadControlled{})
	if s.ControlledBy != "" {
		t.Fatalf("controlledBy = %q, want empty", s.ControlledBy)
	}
}

func TestScheduleCloneIsolation(t *testing.T) {
	s := NewThreadState("t1")
	s.Waiting = &contracts.ScheduleSpec{DependsOn: []contracts.ThreadID{"t2"}, Prompt: "after t2"}
	s.TriggeredFrom = []contracts.ThreadID{"t2"}
	s.ControlledBy = "t9"

	c := s.Clone()
	c.Waiting.Prompt = "mutated"
	c.Waiting.DependsOn[0] = "mutated"
	c.TriggeredFrom[0] = "mutated"
	c.ControlledBy = "mutated"

	if s.Waiting.Prompt != "after t2" || s.Waiting.DependsOn[0] != "t2" {
		t.Fatalf("clone shares the waiting spec: %+v", s.Waiting)
	}
	if s.TriggeredFrom[0] != "t2" {
		t.Fatalf("clone shares the trigger record: %v", s.TriggeredFrom)
	}
	if s.ControlledBy != "t9" {
		t.Fatalf("clone changed the controller: %q", s.ControlledBy)
	}
}

// A scheduled thread has not run yet: creation plus the schedule can be its
// whole durable log, so it must survive both the boot sweep and a restart,
// while a thread that was only created still goes.
func TestScheduledThreadIsDurableAndKeptBySweep(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "threads")

	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return testClock() }
	if _, err := store.Append(ctx, "later", []contracts.EventPayload{
		contracts.ThreadCreated{Title: "queued"},
		contracts.PeerMailboxSet{Enabled: true},
		contracts.ThreadScheduled{DependsOn: []contracts.ThreadID{"t2", "t3"}, Prompt: "after them"},
	}, clock); err != nil {
		t.Fatalf("append scheduled: %v", err)
	}
	if _, err := store.Append(ctx, "ghost", []contracts.EventPayload{
		contracts.ThreadCreated{Title: "unused"},
		contracts.PeerMailboxSet{Enabled: true},
	}, clock); err != nil {
		t.Fatalf("append ghost: %v", err)
	}

	removed, err := store.DropEmptyThreads()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(removed) != 1 || removed[0] != "ghost" {
		t.Fatalf("removed = %v, want only the ghost", removed)
	}

	// Reopen and replay: the waiting spec comes back from the log alone.
	reopened, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(reopened, nil)
	e.Start(ctx)
	t.Cleanup(e.Close)
	st, err := e.State(ctx, "later")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.Waiting == nil || st.Waiting.Prompt != "after them" || len(st.Waiting.DependsOn) != 2 {
		t.Fatalf("after restart: exists=%v waiting=%+v, want the armed schedule", st.Exists, st.Waiting)
	}
}

// Scheduling through the engine goes through the decider and the projector:
// the armed schedule is durable, and the turn that discharges it records what
// it waited on.
func TestScheduleThroughEngineTriggersAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "threads")

	store1, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e1 := NewEngine(store1, nil)
	e1.Start(ctx)
	mustDispatch(t, e1, ctx, contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "Follow-up"})
	mustDispatch(t, e1, ctx, scheduleCmd("t1", "t2", "t3"))
	e1.Close()

	store2, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(store2, nil)
	e2.Start(ctx)
	t.Cleanup(e2.Close)
	st, err := e2.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.Waiting == nil || st.Waiting.Prompt != "after them" {
		t.Fatalf("after restart: exists=%v waiting=%+v, want the armed schedule", st.Exists, st.Waiting)
	}

	// The turn discharges the schedule and keeps the record of what fired it.
	mustDispatch(t, e2, ctx, contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "go"})
	st, err = e2.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Waiting != nil || len(st.TriggeredFrom) != 2 || st.TriggeredFrom[0] != "t2" || st.TriggeredFrom[1] != "t3" {
		t.Fatalf("after turn: waiting=%+v triggeredFrom=%v, want nil waiting and [t2 t3]", st.Waiting, st.TriggeredFrom)
	}
}
