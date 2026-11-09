package domain

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func setLeads(t *testing.T, s *ThreadState, n int) ([]contracts.EventPayload, error) {
	t.Helper()
	return Decide(contracts.Command{Type: contracts.CmdLeadsSet, ThreadID: "t1", Leads: &n}, s)
}

func TestDecideLeadsSetBoundsTheTeam(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	for _, n := range []int{0, -1, contracts.MaxLeads + 1} {
		if _, err := setLeads(t, s, n); err == nil {
			t.Fatalf("leads=%d should be rejected", n)
		}
	}
	if _, err := Decide(contracts.Command{Type: contracts.CmdLeadsSet, ThreadID: "t1"}, s); err == nil {
		t.Fatal("a leads command without a count should be rejected")
	}
}

func TestDecideLeadsSetOnlyOneLeadKeepsMailboxAsIs(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	evs, err := setLeads(t, s, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evs) != 1 || evs[0].(contracts.LeadsSet).Count != 1 {
		t.Fatalf("want one LeadsSet{1}, got %#v", evs)
	}
}

func TestDecideLeadsSetTeamTurnsPeerMailboxOn(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	s.PeerMailbox = false
	evs, err := setLeads(t, s, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("want LeadsSet plus PeerMailboxSet, got %#v", evs)
	}
	if on, ok := evs[1].(contracts.PeerMailboxSet); !ok || !on.Enabled {
		t.Fatalf("a team must enable the peer mailbox it agrees through, got %#v", evs[1])
	}

	// A mailbox the user already turned on is not re-announced.
	s.PeerMailbox = true
	if evs, _ := setLeads(t, s, 2); len(evs) != 1 {
		t.Fatalf("mailbox already on: want only LeadsSet, got %#v", evs)
	}
}

func TestLeadCountDefaultsToOne(t *testing.T) {
	s := NewThreadState("t1")
	if got := s.LeadCount(); got != 1 {
		t.Fatalf("LeadCount() = %d, want 1 for a thread with no LeadsSet", got)
	}
	s.Leads = 3
	if got := s.LeadCount(); got != 3 {
		t.Fatalf("LeadCount() = %d, want 3", got)
	}
}
