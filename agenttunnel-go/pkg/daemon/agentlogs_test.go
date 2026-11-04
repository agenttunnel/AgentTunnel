package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// Agent logs are part of a thread's history: opening a subagent after a restart
// must show its output, not "No output yet".
func TestAgentLogsSurviveRestart(t *testing.T) {
	stateDir := t.TempDir()
	open := func() *Daemon {
		t.Helper()
		driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
			return providers.CommandSpec{Binary: "sh", Args: []string{"-c", e2eScript}, PromptVia: "stdin"}, nil
		}, providers.JSONLineTranslator{})
		d, err := New(context.Background(), Options{
			StateDir: stateDir,
			Token:    "secret",
			Registry: providers.NewRegistry(driver),
			Config:   orchestrator.Config{MaxPasses: 2, MaxConcurrency: 2},
		})
		if err != nil {
			t.Fatalf("daemon: %v", err)
		}
		return d
	}

	d1 := open()
	c1, _ := dialWS(t, d1)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c1, "1", "thread.create", map[string]any{"title": "logs"}), &created)
	call(t, c1, "2", "thread.turn.start", map[string]any{
		"threadId": created.ThreadID,
		"text":     "build it",
	})

	// Sanity: the live buffer has the orchestrator's output.
	var live []contracts.LogEntry
	json.Unmarshal(call(t, c1, "3", "agents.logs", map[string]any{
		"agentId": "agent-orchestrator",
	}), &live)
	if len(live) == 0 {
		t.Fatal("no logs for the orchestrator in the same session")
	}
	d1.Close()

	// A fresh daemon must still serve them, from disk.
	d2 := open()
	defer d2.Close()
	c2, _ := dialWS(t, d2)

	var after []contracts.LogEntry
	json.Unmarshal(call(t, c2, "1", "agents.logs", map[string]any{
		"agentId": "agent-orchestrator",
	}), &after)
	if len(after) == 0 {
		t.Fatal("agent logs did not survive the restart")
	}
	if after[0].AgentID != "agent-orchestrator" {
		t.Fatalf("log entry has the wrong agent: %+v", after[0])
	}
}
