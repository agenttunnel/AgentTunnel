package domain

import (
	"context"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func setLeadRuntime(s *ThreadState, index int, rt contracts.LeadRuntime) ([]contracts.EventPayload, error) {
	return Decide(contracts.Command{
		Type: contracts.CmdLeadRuntimeSet, ThreadID: "t1",
		LeadRuntime: &contracts.LeadRuntimeSet{Index: index, Runtime: rt},
	}, s)
}

func TestDecideLeadRuntimeSetRejectsBadIndexAndHarness(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	// The default team has one lead, so index 0 is lead 1 and index 1 is the
	// append slot; anything past that needs a larger team first.
	for _, idx := range []int{-1, 2} {
		if _, err := setLeadRuntime(s, idx, contracts.LeadRuntime{}); err == nil {
			t.Fatalf("index %d should be rejected on a one-lead team", idx)
		}
	}
	s.Leads = 5
	if _, err := setLeadRuntime(s, 5, contracts.LeadRuntime{Model: "m"}); err != nil {
		t.Fatalf("append slot on a five-lead team should be accepted: %v", err)
	}
	if _, err := setLeadRuntime(s, 6, contracts.LeadRuntime{Model: "m"}); err == nil {
		t.Fatal("index past the append slot should be rejected")
	}
	s.Leads = 0
	if _, err := setLeadRuntime(s, 0, contracts.LeadRuntime{Harness: "not-a-harness"}); err == nil {
		t.Fatal("an unknown harness should be rejected")
	}
	if _, err := Decide(contracts.Command{Type: contracts.CmdLeadRuntimeSet, ThreadID: "t1"}, s); err == nil {
		t.Fatal("a lead runtime command without a payload should be rejected")
	}
	// An empty harness is valid: it means inherit the orchestrator role.
	if _, err := setLeadRuntime(s, 1, contracts.LeadRuntime{Model: "m"}); err != nil {
		t.Fatalf("inherit-harness override should be accepted: %v", err)
	}
}

func TestProjectLeadRuntimeSetStoresPerLead(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	want := contracts.LeadRuntime{Harness: contracts.HarnessOpenCode, Model: "m", Variant: "v"}
	if err := Project(s, contracts.Event{Payload: contracts.LeadRuntimeSet{Index: 2, Runtime: want}}); err != nil {
		t.Fatal(err)
	}
	if got := s.LeadRuntimeFor(2); got != want {
		t.Fatalf("lead 2 = %+v, want %+v", got, want)
	}
	if got := s.LeadRuntimeFor(0); got != (contracts.LeadRuntime{}) {
		t.Fatalf("unset lead should inherit, got %+v", got)
	}
	if got := s.LeadRuntimeFor(9); got != (contracts.LeadRuntime{}) {
		t.Fatalf("out-of-range lead should inherit, got %+v", got)
	}
}

// A lead's model is per thread: setting it on one thread leaves another thread's
// lead on its own runtime, and a snapshot taken before the change keeps the value
// it was read with. The engine's snapshot must not share the live lead slice.
func TestLeadRuntimeStaysWithItsThread(t *testing.T) {
	ctx := context.Background()
	eng := NewEngine(NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	for _, id := range []contracts.ThreadID{"t1", "t2"} {
		if err := eng.Dispatch(ctx, contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: id}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	set := func(id contracts.ThreadID, model string) {
		t.Helper()
		if err := eng.Dispatch(ctx, contracts.Command{
			Type: contracts.CmdLeadRuntimeSet, ThreadID: id,
			LeadRuntime: &contracts.LeadRuntimeSet{Index: 0, Runtime: contracts.LeadRuntime{Model: model}},
		}); err != nil {
			t.Fatalf("set model on %s: %v", id, err)
		}
	}

	set("t1", "model-a")
	before, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}

	set("t1", "model-b")
	if got := before.LeadRuntimeFor(0).Model; got != "model-a" {
		t.Fatalf("snapshot taken before the change now reads %q, want model-a", got)
	}
	t1, _ := eng.State(ctx, "t1")
	if got := t1.LeadRuntimeFor(0).Model; got != "model-b" {
		t.Fatalf("t1 lead model = %q, want model-b", got)
	}
	t2, _ := eng.State(ctx, "t2")
	if got := t2.LeadRuntimeFor(0); got != (contracts.LeadRuntime{}) {
		t.Fatalf("t2 lead inherited t1's override: %+v", got)
	}
}

// Task statuses are per thread too: a snapshot keeps the status it was read with
// when the live task list changes afterwards.
func TestSnapshotDoesNotShareTaskList(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	apply := func(p contracts.EventPayload) {
		t.Helper()
		if err := Project(s, contracts.Event{ThreadID: "t1", Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	apply(contracts.TaskAdded{Name: "a", Title: "A"})
	snap := s.Clone()
	apply(contracts.TaskStatusChanged{Name: "a", Status: contracts.TaskDone})
	if got := snap.Tasks[0].Status; got != contracts.TaskTodo {
		t.Fatalf("snapshot task status = %v, want todo", got)
	}
}
