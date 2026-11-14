package domain

import (
	"errors"
	"reflect"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func setSoloRuntime(s *ThreadState, rt contracts.LeadRuntime) ([]contracts.EventPayload, error) {
	return Decide(contracts.Command{
		Type: contracts.CmdSoloRuntimeSet, ThreadID: "t1",
		SoloRuntime: &contracts.SoloRuntimeSet{Runtime: rt},
	}, s)
}

func TestDecideSoloRuntimeSetAndClear(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	want := contracts.LeadRuntime{Harness: contracts.HarnessClaude, Model: "m", Variant: "v"}
	evs, err := setSoloRuntime(s, want)
	if err != nil {
		t.Fatalf("setting a solo runtime on an existing thread should be accepted: %v", err)
	}
	if len(evs) != 1 || evs[0].(contracts.SoloRuntimeSet).Runtime != want {
		t.Fatalf("want one SoloRuntimeSet, got %#v", evs)
	}
	if err := Project(s, contracts.Event{Payload: evs[0]}); err != nil {
		t.Fatal(err)
	}
	if !s.Solo() || s.SoloRuntime == nil || *s.SoloRuntime != want {
		t.Fatalf("projected solo runtime = %+v, want %+v", s.SoloRuntime, want)
	}

	// An empty harness clears: the projector nils the field.
	evs, err = setSoloRuntime(s, contracts.LeadRuntime{})
	if err != nil {
		t.Fatalf("clearing a solo runtime should be accepted: %v", err)
	}
	if len(evs) != 1 || evs[0].(contracts.SoloRuntimeSet).Runtime != (contracts.LeadRuntime{}) {
		t.Fatalf("want one empty SoloRuntimeSet, got %#v", evs)
	}
	if err := Project(s, contracts.Event{Payload: evs[0]}); err != nil {
		t.Fatal(err)
	}
	if s.Solo() {
		t.Fatal("an empty runtime should clear solo mode")
	}
}

func TestDecideSoloRuntimeSetRejectsBadPayloadAndThread(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	if _, err := setSoloRuntime(s, contracts.LeadRuntime{Harness: "not-a-harness"}); err == nil {
		t.Fatal("an unknown harness should be rejected")
	}
	if _, err := Decide(contracts.Command{Type: contracts.CmdSoloRuntimeSet, ThreadID: "t1"}, s); err == nil {
		t.Fatal("a solo runtime command without a payload should be rejected")
	}
	_, err := Decide(contracts.Command{
		Type: contracts.CmdSoloRuntimeSet, ThreadID: "missing",
		SoloRuntime: &contracts.SoloRuntimeSet{Runtime: contracts.LeadRuntime{Harness: contracts.HarnessClaude}},
	}, NewThreadState("missing"))
	if !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("unknown thread: err = %v, want ErrThreadNotFound", err)
	}
}

// Solo runs exactly one agent: setting it on a multi-lead thread first
// normalizes the team to one lead, with the LeadsSet payload landing before
// the SoloRuntimeSet one.
func TestDecideSoloRuntimeSetNormalizesMultiLeadTeam(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	s.Leads = 3
	evs, err := setSoloRuntime(s, contracts.LeadRuntime{Harness: contracts.HarnessClaude, Model: "m"})
	if err != nil {
		t.Fatalf("setting solo on a three-lead team should be accepted: %v", err)
	}
	if len(evs) != 2 || evs[0].(contracts.LeadsSet).Count != 1 {
		t.Fatalf("want LeadsSet{1} then SoloRuntimeSet, got %#v", evs)
	}
	if _, ok := evs[1].(contracts.SoloRuntimeSet); !ok {
		t.Fatalf("second payload should be SoloRuntimeSet, got %#v", evs[1])
	}
	for _, ev := range evs {
		if err := Project(s, contracts.Event{Payload: ev}); err != nil {
			t.Fatal(err)
		}
	}
	if s.Leads != 1 || s.LeadCount() != 1 {
		t.Fatalf("projected leads = %d, want 1", s.LeadCount())
	}
	if !s.Solo() {
		t.Fatal("projected state should be solo")
	}
}

func TestDecideLeadsSetRefusedOnSoloThread(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	s.SoloRuntime = &contracts.LeadRuntime{Harness: contracts.HarnessClaude, Model: "m"}
	if _, err := setLeads(t, s, 3); err == nil {
		t.Fatal("widening the team on a solo thread should be rejected")
	}
	if _, err := setLeads(t, s, 1); err != nil {
		t.Fatalf("a one-lead team is solo-compatible: %v", err)
	}
	// Clearing solo re-allows a team.
	s.SoloRuntime = nil
	if _, err := setLeads(t, s, 3); err != nil {
		t.Fatalf("widening the team after solo is cleared should be accepted: %v", err)
	}
}

// A snapshot keeps the solo runtime it was read with: mutating the copy must
// not reach the live state, and vice versa.
func TestSnapshotDoesNotShareSoloRuntime(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	if err := Project(s, contracts.Event{Payload: contracts.SoloRuntimeSet{
		Runtime: contracts.LeadRuntime{Harness: contracts.HarnessClaude, Model: "m"},
	}}); err != nil {
		t.Fatal(err)
	}
	snap := s.Clone()
	snap.SoloRuntime.Model = "mutated"
	if got := s.SoloRuntime.Model; got != "m" {
		t.Fatalf("live solo model = %q, want m", got)
	}
	s.SoloRuntime.Model = "changed"
	if got := snap.SoloRuntime.Model; got != "mutated" {
		t.Fatalf("snapshot solo model = %q, want mutated", got)
	}
}

// Replaying the persisted log of set, turn, clear and set again must rebuild
// the live state exactly.
func TestSoloRuntimeReplayMatchesLive(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	mustDispatch(t, e, ctx, contracts.Command{
		Type: contracts.CmdSoloRuntimeSet, ThreadID: "t1",
		SoloRuntime: &contracts.SoloRuntimeSet{Runtime: contracts.LeadRuntime{Harness: contracts.HarnessClaude, Model: "m"}},
	})
	mustDispatch(t, e, ctx, contracts.Command{Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "hello"})
	mustDispatch(t, e, ctx, contracts.Command{
		Type: contracts.CmdSoloRuntimeSet, ThreadID: "t1",
		SoloRuntime: &contracts.SoloRuntimeSet{Runtime: contracts.LeadRuntime{}},
	})
	mustDispatch(t, e, ctx, contracts.Command{
		Type: contracts.CmdSoloRuntimeSet, ThreadID: "t1",
		SoloRuntime: &contracts.SoloRuntimeSet{Runtime: contracts.LeadRuntime{Harness: contracts.HarnessOpenCode, Model: "other"}},
	})

	live, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	events, err := e.Events(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	replayed := NewThreadState("t1")
	for _, ev := range events {
		if err := Project(replayed, ev); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(live.SoloRuntime, replayed.SoloRuntime) {
		t.Fatalf("replayed solo runtime = %+v, want %+v", replayed.SoloRuntime, live.SoloRuntime)
	}
	if !replayed.Solo() || replayed.SoloRuntime == nil || replayed.SoloRuntime.Harness != contracts.HarnessOpenCode {
		t.Fatalf("replayed solo runtime = %+v, want the opencode one", replayed.SoloRuntime)
	}
	// The clear between the two sets must not survive the replay.
	if len(replayed.Messages) != 1 {
		t.Fatalf("replayed message count = %d, want 1", len(replayed.Messages))
	}
}
