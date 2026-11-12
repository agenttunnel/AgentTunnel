package domain

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func seedTranscript(t *testing.T, e *Engine, ctx context.Context, thread contracts.ThreadID) {
	t.Helper()
	mustDispatch(t, e, ctx, contracts.Command{ID: "c0", Type: contracts.CmdThreadCreate, ThreadID: thread, Title: "Login"})
	for _, m := range []contracts.MessageSent{
		{MessageID: "m1", Author: "user", Text: "first ask"},
		{MessageID: "m2", Author: "orchestrator", Text: "first answer"},
		{MessageID: "m3", Author: "user", Text: "second ask"},
	} {
		msg := m
		mustDispatch(t, e, ctx, contracts.Command{ID: "append-" + msg.MessageID, Type: contracts.CmdMessageAppend, ThreadID: thread, Message: &msg})
	}
}

func TestForkCopiesTranscriptPrefixAndLeavesSourceAlone(t *testing.T) {
	e, ctx := newTestEngine(t)
	seedTranscript(t, e, ctx, "src")

	mustDispatch(t, e, ctx, contracts.Command{
		ID: "f1", Type: contracts.CmdThreadFork, ThreadID: "fork", Title: "Login (branch)", ProjectID: "p1",
		Transcript: []contracts.MessageSent{
			{MessageID: "m1", Author: "user", Text: "first ask"},
			{MessageID: "m2", Author: "orchestrator", Text: "first answer"},
		},
	})

	fork, err := e.State(ctx, "fork")
	if err != nil {
		t.Fatal(err)
	}
	if !fork.Exists || fork.Title != "Login (branch)" || fork.ProjectID != "p1" || fork.ParentThreadID != "" || fork.Worktree != nil {
		t.Fatalf("fork = %+v, want a top-level thread with no worktree yet", fork)
	}
	if len(fork.Messages) != 2 || fork.Messages[0].ID != "m1" || fork.Messages[1].Text != "first answer" {
		t.Fatalf("fork messages = %+v, want the copied prefix", fork.Messages)
	}
	if fork.Turn != 1 {
		t.Fatalf("fork turn = %d, want the copied user message to count once", fork.Turn)
	}

	// The fork continues with a normal turn: the new user message follows the copy.
	mustDispatch(t, e, ctx, contracts.Command{ID: "t1", Type: contracts.CmdTurnStart, ThreadID: "fork", Text: "branch ask"})
	fork, _ = e.State(ctx, "fork")
	if len(fork.Messages) != 3 || fork.Messages[2].Text != "branch ask" || fork.Turn != 2 {
		t.Fatalf("after turn: messages = %+v turn = %d", fork.Messages, fork.Turn)
	}

	src, _ := e.State(ctx, "src")
	if len(src.Messages) != 3 || src.Turn != 2 {
		t.Fatalf("source changed: messages = %d turn = %d", len(src.Messages), src.Turn)
	}
}

func TestDecideForkRejectsExistingAndEmptyTranscript(t *testing.T) {
	fork := contracts.Command{Type: contracts.CmdThreadFork, ThreadID: "fork", Transcript: []contracts.MessageSent{{MessageID: "m1", Author: "user", Text: "x"}}}

	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadFork, ThreadID: "fork"}, NewThreadState("fork")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("empty transcript: err = %v, want ErrInvalidCommand", err)
	}
	existing := NewThreadState("fork")
	existing.Exists = true
	if _, err := Decide(fork, existing); !errors.Is(err, ErrThreadExists) {
		t.Fatalf("existing thread: err = %v, want ErrThreadExists", err)
	}
}

func TestForkSurvivesBootSweep(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "threads"))
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(store, nil)
	e.SetClock(testClock)
	e.Start(ctx)
	t.Cleanup(e.Close)

	mustDispatch(t, e, ctx, contracts.Command{
		ID: "f1", Type: contracts.CmdThreadFork, ThreadID: "fork", Title: "Login (branch)",
		Transcript: []contracts.MessageSent{{MessageID: "m1", Author: "user", Text: "kept"}},
	})

	removed, err := store.DropEmptyThreads()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("boot sweep removed %v, want the fork kept", removed)
	}
	events, err := store.Load(ctx, "fork")
	if err != nil || len(events) == 0 {
		t.Fatalf("fork log after sweep: %d events, err %v", len(events), err)
	}
}
