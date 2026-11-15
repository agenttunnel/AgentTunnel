package domain

import (
	"context"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func setRoleRuntime(s *ThreadState, role contracts.RoleID, rt contracts.LeadRuntime) ([]contracts.EventPayload, error) {
	return Decide(contracts.Command{
		Type: contracts.CmdRoleRuntimeSet, ThreadID: "t1",
		RoleRuntime: &contracts.RoleRuntimeSet{Role: role, Runtime: rt},
	}, s)
}

func TestDecideRoleRuntimeSetValidatesRoleAndHarness(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true

	if _, err := setRoleRuntime(s, "", contracts.LeadRuntime{}); err == nil {
		t.Fatal("an empty role should be rejected")
	}
	if _, err := setRoleRuntime(s, "not-a-role", contracts.LeadRuntime{Model: "m"}); err == nil {
		t.Fatal("an unknown role should be rejected")
	}
	if _, err := setRoleRuntime(s, contracts.RoleExecutor, contracts.LeadRuntime{Harness: "not-a-harness"}); err == nil {
		t.Fatal("an unknown harness should be rejected")
	}
	if _, err := Decide(contracts.Command{Type: contracts.CmdRoleRuntimeSet, ThreadID: "t1"}, s); err == nil {
		t.Fatal("a role runtime command without a payload should be rejected")
	}

	events, err := setRoleRuntime(s, contracts.RoleExecutor, contracts.LeadRuntime{Model: "m"})
	if err != nil {
		t.Fatalf("a builtin role with an inherit-harness override should be accepted: %v", err)
	}
	got, ok := events[0].(contracts.RoleRuntimeSet)
	if !ok || got.ThreadID != "t1" || got.Role != contracts.RoleExecutor || got.Runtime.Model != "m" {
		t.Fatalf("event = %+v", events[0])
	}

	missing := NewThreadState("t2")
	if _, err := setRoleRuntime(missing, contracts.RoleExecutor, contracts.LeadRuntime{}); err != ErrThreadNotFound {
		t.Fatalf("err = %v, want ErrThreadNotFound", err)
	}
}

func TestProjectRoleRuntimeSetStoresByRole(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	want := contracts.LeadRuntime{Harness: contracts.HarnessCodex, Model: "gpt-x", Variant: "high"}
	if err := Project(s, contracts.Event{Payload: contracts.RoleRuntimeSet{
		ThreadID: "t1", Role: contracts.RoleExecutor, Runtime: want,
	}}); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.RoleRuntimeFor(contracts.RoleExecutor); !ok || got != want {
		t.Fatalf("executor runtime = %+v (ok=%v), want %+v", got, ok, want)
	}
	if _, ok := s.RoleRuntimeFor(contracts.RoleTester); ok {
		t.Fatal("tester should still inherit")
	}
}

// Role overrides are per thread and survive a projection restart; a snapshot
// taken before a change keeps the value it read, so concurrent turns cannot
// observe each other's runtimes.
func TestRoleRuntimeStaysWithItsThread(t *testing.T) {
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
			Type: contracts.CmdRoleRuntimeSet, ThreadID: id,
			RoleRuntime: &contracts.RoleRuntimeSet{
				Role: contracts.RoleExecutor, Runtime: contracts.LeadRuntime{Model: model},
			},
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
	if got, _ := before.RoleRuntimeFor(contracts.RoleExecutor); got.Model != "model-a" {
		t.Fatalf("snapshot taken before the change now reads %q, want model-a", got.Model)
	}
	t1, _ := eng.State(ctx, "t1")
	if got, _ := t1.RoleRuntimeFor(contracts.RoleExecutor); got.Model != "model-b" {
		t.Fatalf("t1 executor model = %q, want model-b", got.Model)
	}
	t2, _ := eng.State(ctx, "t2")
	if _, ok := t2.RoleRuntimeFor(contracts.RoleExecutor); ok {
		t.Fatal("t2 inherited t1's role override")
	}
}
