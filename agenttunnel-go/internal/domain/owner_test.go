package domain

import (
	"context"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The decider carries the command owner into ThreadCreated and normalizes an
// empty owner to the user, so every durable creation event carries an
// explicit value.
func TestDecideThreadCreateOwner(t *testing.T) {
	evs, err := Decide(contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1", Owner: contracts.ThreadOwnerOrchestrator}, NewThreadState("t1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	created := evs[0].(contracts.ThreadCreated)
	if created.Owner != contracts.ThreadOwnerOrchestrator {
		t.Fatalf("owner = %q, want %q", created.Owner, contracts.ThreadOwnerOrchestrator)
	}

	evs, err = Decide(contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1"}, NewThreadState("t1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	created = evs[0].(contracts.ThreadCreated)
	if created.Owner != contracts.ThreadOwnerUser {
		t.Fatalf("empty owner = %q, want the default %q", created.Owner, contracts.ThreadOwnerUser)
	}
}

// The projector records the event owner as-is: an owner-less event (a log
// recorded before owners existed) leaves the state owner empty, which reads
// as the user.
func TestProjectorThreadCreatedOwner(t *testing.T) {
	s := NewThreadState("t1")
	if err := Project(s, contracts.Event{Payload: contracts.ThreadCreated{Owner: contracts.ThreadOwnerOrchestrator}}); err != nil {
		t.Fatalf("project: %v", err)
	}
	if s.Owner != contracts.ThreadOwnerOrchestrator {
		t.Fatalf("owner = %q, want %q", s.Owner, contracts.ThreadOwnerOrchestrator)
	}

	s = NewThreadState("t1")
	if err := Project(s, contracts.Event{Payload: contracts.ThreadCreated{}}); err != nil {
		t.Fatalf("project: %v", err)
	}
	if s.Owner != "" {
		t.Fatalf("owner = %q, want empty for a pre-owner log", s.Owner)
	}
}

// A provisional thread keeps its creation owner when the first durable command
// materializes it, both in the live state and on replay from disk. This is the
// path every orchestrator-started subthread takes.
func testOwnerSurvivesMaterialize(t *testing.T, owner contracts.ThreadOwner) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e := NewEngine(store, nil)
	e.Start(ctx)

	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Owner: owner})

	st, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("provisional state: %v", err)
	}
	if st.Owner != owner {
		t.Fatalf("provisional owner = %q, want %q", st.Owner, owner)
	}

	mustDispatch(t, e, ctx, contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "run"})

	st, err = e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("materialized state: %v", err)
	}
	if st.Provisional || st.Owner != owner {
		t.Fatalf("materialized owner = %q (provisional %v), want %q", st.Owner, st.Provisional, owner)
	}
	// The durable creation event carries the owner explicitly.
	events, err := e.Events(ctx, "t1")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	created, ok := events[0].Payload.(contracts.ThreadCreated)
	if !ok {
		t.Fatalf("first event = %T, want ThreadCreated", events[0].Payload)
	}
	if created.Owner != owner {
		t.Fatalf("durable creation owner = %q, want %q", created.Owner, owner)
	}

	// Replay from the same store reconstructs the owner.
	e.Close()
	e2 := NewEngine(store, nil)
	e2.Start(ctx)
	t.Cleanup(e2.Close)
	st, err = e2.State(ctx, "t1")
	if err != nil {
		t.Fatalf("replayed state: %v", err)
	}
	if !st.Exists || st.Owner != owner {
		t.Fatalf("replayed owner = %q (exists %v), want %q", st.Owner, st.Exists, owner)
	}
}

func TestEngineMaterializeKeepsOrchestratorOwner(t *testing.T) {
	testOwnerSurvivesMaterialize(t, contracts.ThreadOwnerOrchestrator)
}

func TestEngineMaterializeKeepsUserOwner(t *testing.T) {
	testOwnerSurvivesMaterialize(t, contracts.ThreadOwnerUser)
}

// A separate assertion from the table above: a thread created without an owner
// (every thread.open call predating owners) is recorded as user-owned.
func TestEngineCreateDefaultsOwnerToUser(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	mustDispatch(t, e, ctx, contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "run"})

	st, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Owner != contracts.ThreadOwnerUser {
		t.Fatalf("owner = %q, want %q", st.Owner, contracts.ThreadOwnerUser)
	}
}
