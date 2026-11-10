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

func TestWorktreeDetachClearsOnlyOwnRef(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	eng := NewEngine(store, nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	const id = contracts.ThreadID("th_detach")
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: id, ProjectID: "p1"}); err != nil {
		t.Fatal(err)
	}
	// Nothing to detach yet.
	err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdWorktreeDetach, ThreadID: id})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("detach with no worktree err = %v, want ErrInvalidCommand", err)
	}

	ref := contracts.WorktreeRef{Path: "/tmp/wt", Branch: "agenttunnel/x", Base: "main"}
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c3", Type: contracts.CmdWorktreeAttach, ThreadID: id, Worktree: &ref}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c4", Type: contracts.CmdWorktreeDetach, ThreadID: id}); err != nil {
		t.Fatalf("detach own ref: %v", err)
	}
	if st, _ := eng.State(ctx, id); st.Worktree != nil {
		t.Fatalf("worktree after detach = %+v, want nil", st.Worktree)
	}

	// After detaching, the thread may take a fresh worktree of its own.
	fresh := contracts.WorktreeRef{Path: "/tmp/wt2", Branch: "agenttunnel/y", Base: "main"}
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c5", Type: contracts.CmdWorktreeAttach, ThreadID: id, Worktree: &fresh}); err != nil {
		t.Fatalf("attach after detach: %v", err)
	}

	// Replay yields the same ref: detach is projected, not inferred.
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
	if replayed.Worktree == nil || *replayed.Worktree != fresh {
		t.Fatalf("replayed worktree = %+v, want %+v", replayed.Worktree, fresh)
	}
}

func TestSubthreadRefreshReplacesInheritedRef(t *testing.T) {
	ctx := context.Background()
	eng := NewEngine(NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	const child = contracts.ThreadID("th_child")
	old := contracts.WorktreeRef{Path: "/tmp/old", Branch: "agenttunnel/old", Base: "main", Inherited: true}
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: child, ProjectID: "p1",
		ParentThreadID: "th_parent", Worktree: &old,
	}); err != nil {
		t.Fatal(err)
	}

	next := contracts.WorktreeRef{Path: "/tmp/new", Branch: "agenttunnel/new", Base: "main", Inherited: true}
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdWorktreeAttach, ThreadID: child, Worktree: &next}); err != nil {
		t.Fatalf("refresh inherited ref: %v", err)
	}
	if st, _ := eng.State(ctx, child); st.Worktree == nil || *st.Worktree != next {
		t.Fatalf("worktree = %+v, want %+v", st.Worktree, next)
	}

	// A subthread may never own a worktree, and a top-level thread never inherits one.
	own := contracts.WorktreeRef{Path: "/tmp/own", Branch: "agenttunnel/own", Base: "main"}
	err := eng.Dispatch(ctx, contracts.Command{ID: "c3", Type: contracts.CmdWorktreeAttach, ThreadID: child, Worktree: &own})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("subthread own attach err = %v, want ErrInvalidCommand", err)
	}

	const top = contracts.ThreadID("th_top")
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c4", Type: contracts.CmdThreadCreate, ThreadID: top, ProjectID: "p1"}); err != nil {
		t.Fatal(err)
	}
	err = eng.Dispatch(ctx, contracts.Command{ID: "c5", Type: contracts.CmdWorktreeAttach, ThreadID: top, Worktree: &next})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("top-level inherited attach err = %v, want ErrInvalidCommand", err)
	}
}
