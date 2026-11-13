package domain

import (
	"context"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestProjectThreadCreatedStampsCreatedAt(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0).UTC()
	t1 := t0.Add(time.Hour)

	s := NewThreadState("t1")
	if err := Project(s, contracts.Event{Seq: 1, ThreadID: "t1", At: t0, Payload: contracts.ThreadCreated{Title: "Thread"}}); err != nil {
		t.Fatalf("project create: %v", err)
	}
	if !s.CreatedAt.Equal(t0) {
		t.Fatalf("createdAt = %s, want %s", s.CreatedAt, t0)
	}
	if !s.UpdatedAt.Equal(t0) {
		t.Fatalf("updatedAt = %s, want %s", s.UpdatedAt, t0)
	}

	if err := Project(s, contracts.Event{Seq: 2, ThreadID: "t1", At: t1, Payload: contracts.ThreadRenamed{Title: "Renamed"}}); err != nil {
		t.Fatalf("project rename: %v", err)
	}
	if !s.CreatedAt.Equal(t0) {
		t.Fatalf("rename moved createdAt: %s, want %s", s.CreatedAt, t0)
	}
	if !s.UpdatedAt.Equal(t1) {
		t.Fatalf("updatedAt = %s, want %s", s.UpdatedAt, t1)
	}
}

// A draft's submission time is its creation too: it is durable from the start.
func TestProjectDraftCreatedStampsCreatedAt(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0).UTC()
	s := NewThreadState("t1")
	if err := Project(s, contracts.Event{Seq: 1, ThreadID: "t1", At: t0, Payload: contracts.ThreadCreated{Title: "Login", Draft: true}}); err != nil {
		t.Fatalf("project create: %v", err)
	}
	if !s.CreatedAt.Equal(t0) {
		t.Fatalf("createdAt = %s, want %s", s.CreatedAt, t0)
	}
}

// A provisional thread carries its create time until the first durable command
// materializes it; the ThreadCreated envelope time then takes over. A second
// engine over the same store must replay to the same value.
func TestEngineCreatedAtProvisionalThroughMaterialize(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	t0 := time.Unix(1_700_000_000, 0).UTC()
	t1 := t0.Add(time.Minute)
	now := t0

	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(store, nil)
	e.SetClock(func() time.Time { return now })
	e.Start(ctx)

	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "Preview"})
	st, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.CreatedAt.Equal(t0) {
		t.Fatalf("provisional createdAt = %s, want %s", st.CreatedAt, t0)
	}

	now = t1
	mustDispatch(t, e, ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadRename, ThreadID: "t1", Title: "Named"})
	st, err = e.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.CreatedAt.Equal(t1) {
		t.Fatalf("materialized createdAt = %s, want %s (the creation event's envelope time)", st.CreatedAt, t1)
	}
	e.Close()

	store2, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(store2, nil)
	e2.Start(ctx)
	t.Cleanup(e2.Close)
	st2, err := e2.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !st2.CreatedAt.Equal(t1) {
		t.Fatalf("replayed createdAt = %s, want %s", st2.CreatedAt, t1)
	}
}
