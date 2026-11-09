package domain

import (
	"context"
	"errors"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestWorktreeAttachIsProjectedOnceAndReplays(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	eng := NewEngine(store, nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	const id = contracts.ThreadID("th_wt")
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: id, ProjectID: "p1"}); err != nil {
		t.Fatal(err)
	}
	ref := contracts.WorktreeRef{Path: "/tmp/wt", Branch: "agenttunnel/x", Base: "main"}
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdWorktreeAttach, ThreadID: id, Worktree: &ref}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	st, err := eng.State(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Worktree == nil || *st.Worktree != ref {
		t.Fatalf("worktree = %+v, want %+v", st.Worktree, ref)
	}

	// A thread has at most one worktree.
	other := contracts.WorktreeRef{Path: "/tmp/other", Branch: "agenttunnel/y", Base: "main"}
	err = eng.Dispatch(ctx, contracts.Command{ID: "c3", Type: contracts.CmdWorktreeAttach, ThreadID: id, Worktree: &other})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("second attach err = %v, want ErrInvalidCommand", err)
	}

	// Replaying the durable log yields the same worktree: the projector is pure.
	events, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var replayed ThreadState
	for _, ev := range events {
		if err := Project(&replayed, ev); err != nil {
			t.Fatal(err)
		}
	}
	if replayed.Worktree == nil || *replayed.Worktree != ref {
		t.Fatalf("replayed worktree = %+v, want %+v", replayed.Worktree, ref)
	}
}

func TestWorktreeAttachRefusedForSubthread(t *testing.T) {
	ctx := context.Background()
	eng := NewEngine(NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	const child = contracts.ThreadID("th_child")
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: child, ProjectID: "p1", ParentThreadID: "th_parent",
	}); err != nil {
		t.Fatal(err)
	}
	ref := contracts.WorktreeRef{Path: "/tmp/wt", Branch: "agenttunnel/x", Base: "main"}
	err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdWorktreeAttach, ThreadID: child, Worktree: &ref})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("err = %v, want ErrInvalidCommand", err)
	}
}

func TestWorktreeOnCreationEventCarriesInheritance(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	eng := NewEngine(store, nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	const id = contracts.ThreadID("th_child")
	parent := &contracts.WorktreeRef{Path: "/tmp/wt", Branch: "agenttunnel/x", Base: "main"}
	inherited := *parent
	inherited.Inherited = true
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: id, ProjectID: "p1",
		ParentThreadID: "th_parent", Worktree: &inherited,
	}); err != nil {
		t.Fatal(err)
	}
	// The ref rides the creation event, so a provisional thread is durable with it.
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdTurnStart, ThreadID: id, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	events, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	created, ok := events[0].Payload.(contracts.ThreadCreated)
	if !ok {
		t.Fatalf("first event is %T, want ThreadCreated", events[0].Payload)
	}
	if created.Worktree == nil || *created.Worktree != inherited {
		t.Fatalf("created worktree = %+v, want %+v", created.Worktree, inherited)
	}
}
