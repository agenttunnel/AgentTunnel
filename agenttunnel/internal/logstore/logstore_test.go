package logstore

import (
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestStoreKeepsThreadsApart(t *testing.T) {
	s := New(t.TempDir())
	now := time.Unix(0, 0).UTC()

	// Both threads share the team lead's fixed agent id; their logs must not mix.
	_ = s.Append("t1", contracts.LogEntry{AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "thread one", At: now})
	_ = s.Append("t2", contracts.LogEntry{AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "thread two", At: now})

	if got := s.Entries("t1"); len(got) != 1 || got[0].Text != "thread one" {
		t.Fatalf("t1 entries = %+v", got)
	}
	if got := s.Entries("t2"); len(got) != 1 || got[0].Text != "thread two" {
		t.Fatalf("t2 entries = %+v", got)
	}
	if got := s.Entries("missing"); len(got) != 0 {
		t.Fatalf("missing thread entries = %+v", got)
	}
}

func TestStoreRestoresThreadAfterRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(0, 0).UTC()

	first := New(dir)
	_ = first.Append("t1", contracts.LogEntry{AgentID: "a1", Kind: contracts.LogText, Text: "hello", At: now})
	_ = first.Append("t1", contracts.LogEntry{AgentID: "a1", Kind: contracts.LogReasoning, Text: "thinking", At: now})

	// A fresh store over the same directory stands in for a daemon restart.
	second := New(dir)
	got := second.Entries("t1")
	if len(got) != 2 || got[0].Text != "hello" || got[1].Kind != contracts.LogReasoning {
		t.Fatalf("restored entries = %+v", got)
	}
	// New output continues the restored history instead of replacing it.
	_ = second.Append("t1", contracts.LogEntry{AgentID: "a1", Kind: contracts.LogText, Text: "more", At: now})
	if got := second.Entries("t1"); len(got) != 3 || got[2].Text != "more" {
		t.Fatalf("entries after append = %+v", got)
	}
}

func TestStoreBufferIsBounded(t *testing.T) {
	s := New(t.TempDir())
	s.max = 3
	for i := 0; i < 10; i++ {
		_ = s.Append("t1", contracts.LogEntry{AgentID: "a1", Kind: contracts.LogText, Text: "x"})
	}
	if got := s.Entries("t1"); len(got) != 3 {
		t.Fatalf("buffer not bounded: %d", len(got))
	}
}
