package domain

import (
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
