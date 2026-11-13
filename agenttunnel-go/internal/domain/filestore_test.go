package domain

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func testClock() time.Time { return time.Unix(0, 0).UTC() }

func TestFileStorePersistsAndReplays(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "threads")

	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if _, err := s.Append(ctx, "t1", []contracts.EventPayload{
		contracts.ThreadCreated{Title: "durable", ProjectID: "p1"},
		contracts.MessageSent{MessageID: "m1", Author: "user", Text: "hello"},
	}, func() time.Time { return testClock() }); err != nil {
		t.Fatalf("append: %v", err)
	}

	// A fresh store over the same directory sees the log and continues its seq.
	reopened, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	events, err := reopened.Load(ctx, "t1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	created, ok := events[0].Payload.(contracts.ThreadCreated)
	if !ok || created.Title != "durable" || created.ProjectID != "p1" {
		t.Fatalf("payload did not survive: %+v", events[0].Payload)
	}

	appended, err := reopened.Append(ctx, "t1", []contracts.EventPayload{
		contracts.MessageSent{MessageID: "m2", Author: "user", Text: "again"},
	}, func() time.Time { return testClock() })
	if err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if appended[0].Seq != 3 {
		t.Fatalf("seq = %d, want 3 (continuing the existing log)", appended[0].Seq)
	}

	threads, err := reopened.Threads(ctx)
	if err != nil {
		t.Fatalf("threads: %v", err)
	}
	if len(threads) != 1 || threads[0] != "t1" {
		t.Fatalf("threads = %v", threads)
	}
}

func TestFileStoreSkipsTornLine(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, _ := NewFileStore(dir)

	if _, err := s.Append(ctx, "t1", []contracts.EventPayload{
		contracts.ThreadCreated{Title: "ok"},
	}, func() time.Time { return testClock() }); err != nil {
		t.Fatal(err)
	}

	// Simulate an interrupted write.
	f, err := os.OpenFile(filepath.Join(dir, "t1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"seq":2,"threadId":"t1","ty`)
	f.Close()

	events, err := s.Load(ctx, "t1")
	if err != nil {
		t.Fatalf("load should tolerate a torn line: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

// Boot sweeps creation-only logs as ghosts, but a draft's log is only its
// creation too: it must survive, while a plain created-and-unused thread goes.
func TestDropEmptyThreadsKeepsDrafts(t *testing.T) {
	ctx := context.Background()
	s, err := NewFileStore(filepath.Join(t.TempDir(), "threads"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	clock := func() time.Time { return testClock() }
	if _, err := s.Append(ctx, "draft", []contracts.EventPayload{
		contracts.ThreadCreated{Title: "later", Draft: true, Description: "keep me"},
		contracts.PeerMailboxSet{Enabled: true},
	}, clock); err != nil {
		t.Fatalf("append draft: %v", err)
	}
	if _, err := s.Append(ctx, "ghost", []contracts.EventPayload{
		contracts.ThreadCreated{Title: "unused"},
		contracts.PeerMailboxSet{Enabled: true},
	}, clock); err != nil {
		t.Fatalf("append ghost: %v", err)
	}

	removed, err := s.DropEmptyThreads()
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if len(removed) != 1 || removed[0] != "ghost" {
		t.Fatalf("removed = %v, want only the ghost", removed)
	}
	if evs, err := s.Load(ctx, "draft"); err != nil || len(evs) != 2 {
		t.Fatalf("draft log after sweep: %d events, err %v", len(evs), err)
	}
}

// A thread switched to solo mode with no turn yet is not empty: the sweep's
// default branch treats the runtime event as content, so the thread survives
// a restart sweep (a prior incident swept draft-like threads).
func TestDropEmptyThreadsKeepsSoloThreads(t *testing.T) {
	ctx := context.Background()
	s, err := NewFileStore(filepath.Join(t.TempDir(), "threads"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	clock := func() time.Time { return testClock() }
	if _, err := s.Append(ctx, "solo", []contracts.EventPayload{
		contracts.ThreadCreated{Title: "just solo"},
		contracts.PeerMailboxSet{Enabled: true},
		contracts.SoloRuntimeSet{Runtime: contracts.LeadRuntime{Harness: contracts.HarnessClaude}},
	}, clock); err != nil {
		t.Fatalf("append solo: %v", err)
	}

	if removed, err := s.DropEmptyThreads(); err != nil {
		t.Fatalf("drop: %v", err)
	} else if len(removed) != 0 {
		t.Fatalf("removed = %v, want nothing", removed)
	}
	if evs, err := s.Load(ctx, "solo"); err != nil || len(evs) != 3 {
		t.Fatalf("solo log after sweep: %d events, err %v", len(evs), err)
	}
}
