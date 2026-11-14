package orchestrator

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/files"
)

// An exact agent id wins; a roster name resolves to the newest agent carrying
// it, running or terminal. agent-logs and revive share the rule, so a name
// names the same agent through both.
func TestResolveLogTargetPrefersIDThenNewestName(t *testing.T) {
	st := &domain.ThreadState{Agents: map[contracts.AgentID]contracts.Agent{
		"agent-hub-1":   {ID: "agent-hub-1", Name: "hub"},
		"agent-hub-2":   {ID: "agent-hub-2", Name: "hub"},
		"agent-spoke-1": {ID: "agent-spoke-1", Name: "spoke"},
	}, AgentOrder: []contracts.AgentID{"agent-hub-1", "agent-spoke-1", "agent-hub-2"}}

	if id, ok := ResolveLogTarget(st, "agent-hub-1"); !ok || id != "agent-hub-1" {
		t.Fatalf("by id = %q %v, want agent-hub-1", id, ok)
	}
	if id, ok := ResolveLogTarget(st, "hub"); !ok || id != "agent-hub-2" {
		t.Fatalf("by name = %q %v, want the newest hub run", id, ok)
	}
	if id, ok := ResolveLogTarget(st, "ghost"); ok || id != "" {
		t.Fatalf("unknown target = %q %v, want none", id, ok)
	}
}

// Over the read cap the rendering keeps the newest lines and drops whole
// leading ones, saying so on the first line.
func TestRenderAgentLogKeepsNewestLinesOverCap(t *testing.T) {
	var entries []contracts.LogEntry
	now := time.Unix(0, 0).UTC()
	for i := 0; i < 300; i++ {
		entries = append(entries, contracts.LogEntry{
			AgentID: "agent-a", Kind: contracts.LogText,
			Text: fmt.Sprintf("%06d %s", i, strings.Repeat("x", 1000)), At: now,
		})
	}
	text, truncated := RenderAgentLog(entries)
	if !truncated {
		t.Fatal("a log over the cap was not marked truncated")
	}
	if !strings.HasPrefix(text, "(older lines truncated)\n") {
		t.Fatalf("truncated log lacks its note:\n%.80s", text)
	}
	if !strings.Contains(text, "000299") {
		t.Fatal("the newest line was dropped")
	}
	if strings.Contains(text, "000000") {
		t.Fatal("an old line survived the cap")
	}
	if len(text) > files.MaxReadBytes {
		t.Fatalf("truncated log is %d bytes, over the %d cap", len(text), files.MaxReadBytes)
	}

	// Under the cap the log is whole, oldest first, one entry per line.
	small := renderLogWithKinds()
	text, truncated = RenderAgentLog(small)
	if truncated || text != "plain\n[tool] $ go test ./...\n[error] boom\n[reasoning] hmm\n[raw] noise\n[raw] usage line\n" {
		t.Fatalf("render = %q truncated=%v", text, truncated)
	}
}

func renderLogWithKinds() []contracts.LogEntry {
	now := time.Unix(0, 0).UTC()
	return []contracts.LogEntry{
		{AgentID: "agent-a", Kind: contracts.LogText, Text: "plain", At: now},
		{AgentID: "agent-a", Kind: contracts.LogTool, Text: "$ go test ./...", At: now},
		{AgentID: "agent-a", Kind: contracts.LogError, Text: "boom", At: now},
		{AgentID: "agent-a", Kind: contracts.LogReasoning, Text: "hmm", At: now},
		{AgentID: "agent-a", Kind: contracts.LogRaw, Text: "noise", At: now},
		{AgentID: "agent-a", Kind: contracts.LogUsage, Text: "usage line", At: now},
	}
}
