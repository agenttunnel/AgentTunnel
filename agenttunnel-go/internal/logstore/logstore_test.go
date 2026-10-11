package logstore

import (
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestStoreAppendAndRetrieve(t *testing.T) {
	s := New(t.TempDir())
	now := time.Unix(0, 0).UTC()
	entries := []contracts.LogEntry{
		{AgentID: "a1", Kind: contracts.LogText, Text: "hello", At: now},
		{AgentID: "a1", Kind: contracts.LogReasoning, Text: "thinking", At: now},
		{AgentID: "a2", Kind: contracts.LogText, Text: "other agent", At: now},
	}
	for _, e := range entries {
		if err := s.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	if got := s.Entries("a1"); len(got) != 2 || got[0].Text != "hello" {
		t.Fatalf("in-memory entries = %+v", got)
	}

	persisted, err := s.ReadFile("a1")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if len(persisted) != 2 || persisted[1].Kind != contracts.LogReasoning {
		t.Fatalf("persisted entries = %+v", persisted)
	}
	if got := s.Entries("missing"); len(got) != 0 {
		t.Fatalf("missing agent entries = %+v", got)
	}
}

func TestStoreBufferIsBounded(t *testing.T) {
	s := New(t.TempDir())
	s.max = 3
	for i := 0; i < 10; i++ {
		_ = s.Append(contracts.LogEntry{AgentID: "a1", Kind: contracts.LogText, Text: "x"})
	}
	if got := s.Entries("a1"); len(got) != 3 {
		t.Fatalf("buffer not bounded: %d", len(got))
	}
}
