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
