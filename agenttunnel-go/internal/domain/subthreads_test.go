package domain

import (
	"errors"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func spawnSubthread(s *ThreadState, child contracts.ThreadID) ([]contracts.EventPayload, error) {
	return Decide(contracts.Command{
		Type: contracts.CmdSubthreadSpawn, ThreadID: "t1",
		Subthread: &contracts.SubthreadSpawned{ThreadID: child},
	}, s)
}

func liveParent() *ThreadState {
	s := NewThreadState("t1")
	s.Exists = true
	s.PeerMailbox = true
	return s
}

func TestDecideSubthreadSpawnRecordsTheChild(t *testing.T) {
	evs, err := spawnSubthread(liveParent(), "t2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evs) != 1 || evs[0].(contracts.SubthreadSpawned).ThreadID != "t2" {
		t.Fatalf("want one SubthreadSpawned{t2}, got %#v", evs)
	}
}

// TestDecideSubthreadSpawnRefusals covers the recursion guard (a subthread
// starts none), the count cap, the mailbox a subthread reports through, and
// duplicate or missing ids. Each refusal must leave nothing to record.
func TestDecideSubthreadSpawnRefusals(t *testing.T) {
	cases := []struct {
		name   string
		parent func() *ThreadState
		child  contracts.ThreadID
		want   error
	}{
		{"missing thread", func() *ThreadState { return NewThreadState("t1") }, "t2", ErrThreadNotFound},
		{"subthread cannot start subthreads", func() *ThreadState {
			s := liveParent()
			s.ParentThreadID = "t0"
			return s
		}, "t2", ErrInvalidCommand},
		{"count cap", func() *ThreadState {
			s := liveParent()
			for i := 0; i < contracts.MaxSubthreads; i++ {
				s.Subthreads = append(s.Subthreads, contracts.ThreadID("sub"+string(rune('a'+i))))
			}
			return s
		}, "t2", ErrInvalidCommand},
		{"peer mailbox off", func() *ThreadState {
			s := liveParent()
			s.PeerMailbox = false
			return s
		}, "t2", ErrInvalidCommand},
		{"duplicate child", func() *ThreadState {
			s := liveParent()
			s.Subthreads = []contracts.ThreadID{"t2"}
			return s
		}, "t2", ErrInvalidCommand},
		{"empty child id", liveParent, "", ErrInvalidCommand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evs, err := spawnSubthread(tc.parent(), tc.child)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(evs) != 0 {
				t.Fatalf("a refused spawn recorded %#v", evs)
			}
		})
	}
}

func TestProjectSubthreadLinkAndCloneIsolation(t *testing.T) {
	s := NewThreadState("t2")
	if err := Project(s, contracts.Event{ThreadID: "t2", Payload: contracts.ThreadCreated{Title: "x", ParentThreadID: "t1"}}); err != nil {
		t.Fatal(err)
	}
	if s.ParentThreadID != "t1" {
		t.Fatalf("ParentThreadID = %q, want t1", s.ParentThreadID)
	}

	p := NewThreadState("t1")
	if err := Project(p, contracts.Event{ThreadID: "t1", Payload: contracts.SubthreadSpawned{ThreadID: "t2"}}); err != nil {
		t.Fatal(err)
	}
	c := p.Clone()
	c.Subthreads[0] = "mutated"
	if p.Subthreads[0] != "t2" {
		t.Fatal("Clone must not share the subthread list with the live state")
	}
}

// TestSubthreadLinkSurvivesReplay starts a subthread through the engine, then
// rebuilds both threads from their logs alone. The parent link and the
// inherited project must come back from events, not from the cache.
func TestSubthreadLinkSurvivesReplay(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1", ProjectID: "proj"})
	mustDispatch(t, e, ctx, contracts.Command{Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "parent"})

	mustDispatch(t, e, ctx, contracts.Command{
		Type: contracts.CmdSubthreadSpawn, ThreadID: "t1",
		Subthread: &contracts.SubthreadSpawned{ThreadID: "t2"},
	})
	mustDispatch(t, e, ctx, contracts.Command{
		Type: contracts.CmdThreadCreate, ThreadID: "t2", ProjectID: "proj", ParentThreadID: "t1",
	})
	mustDispatch(t, e, ctx, contracts.Command{Type: contracts.CmdTurnStart, ThreadID: "t2", Text: "unrelated"})

	child, err := e.State(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentThreadID != "t1" || child.ProjectID != "proj" {
		t.Fatalf("live child = parent %q project %q", child.ParentThreadID, child.ProjectID)
	}

	events, err := e.Events(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	replayed := NewThreadState("t2")
	for _, ev := range events {
		if err := Project(replayed, ev); err != nil {
			t.Fatal(err)
		}
	}
	if replayed.ParentThreadID != "t1" || replayed.ProjectID != "proj" {
		t.Fatalf("replayed child = parent %q project %q", replayed.ParentThreadID, replayed.ProjectID)
	}

	// A subthread cannot start one of its own: the depth cap holds on the live
	// state the decider reads.
	err = e.Dispatch(ctx, contracts.Command{
		Type: contracts.CmdSubthreadSpawn, ThreadID: "t2",
		Subthread: &contracts.SubthreadSpawned{ThreadID: "t3"},
	})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("nested spawn err = %v, want ErrInvalidCommand", err)
	}
	parent, _ := e.State(ctx, "t1")
	if len(parent.Subthreads) != 1 || parent.Subthreads[0] != "t2" {
		t.Fatalf("parent subthreads = %v, want [t2]", parent.Subthreads)
	}
}
