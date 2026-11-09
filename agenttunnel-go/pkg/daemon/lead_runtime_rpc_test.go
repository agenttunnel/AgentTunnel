package daemon

import (
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// TestSetLeadRuntimeRPCPersistsPerLeadHarness covers thread.setLeadRuntime over
// the WebSocket surface: the override is stored on the thread state the desktop
// Models panel reads back, under the lead index it was set for.
func TestSetLeadRuntimeRPCPersistsPerLeadHarness(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "leads"}), &created)

	var ok struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(call(t, c, "2", "thread.setLeadRuntime", map[string]any{
		"threadId": created.ThreadID, "index": 1, "harness": "codex", "model": "gpt-x",
	}), &ok)
	if !ok.OK {
		t.Fatal("setLeadRuntime should acknowledge")
	}

	var view struct {
		LeadRuntimes []contracts.LeadRuntime `json:"leadRuntimes"`
	}
	json.Unmarshal(call(t, c, "3", "thread.get", map[string]any{"threadId": created.ThreadID}), &view)
	if len(view.LeadRuntimes) != 2 || view.LeadRuntimes[1].Harness != contracts.HarnessCodex || view.LeadRuntimes[1].Model != "gpt-x" {
		t.Fatalf("lead 2 runtime not persisted: %+v", view.LeadRuntimes)
	}
	if view.LeadRuntimes[0].Harness != "" {
		t.Fatalf("lead 1 should still inherit, got %+v", view.LeadRuntimes[0])
	}
}
