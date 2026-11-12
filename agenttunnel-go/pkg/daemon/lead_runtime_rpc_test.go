package daemon

import (
	"encoding/json"
	"strings"
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

type leadSettingsView struct {
	Lead  contracts.LeadRuntime `json:"lead"`
	Roles []struct {
		ID      string `json:"id"`
		Harness string `json:"harness"`
		Model   string `json:"model"`
	} `json:"roles"`
}

func orchestratorRole(t *testing.T, view leadSettingsView) (harness, model string) {
	t.Helper()
	for _, r := range view.Roles {
		if r.ID == string(contracts.RoleOrchestrator) {
			return r.Harness, r.Model
		}
	}
	t.Fatal("orchestrator role missing from settings view")
	return "", ""
}

// TestSetLeadRuntimeSettingPersistsMasterOrchestratorRuntime covers the master
// orchestrator's persisted harness/model: settings.set pins the lead runtime,
// settings.get reads it back, and the Orchestrator role row shows the same
// effective runtime.
func TestSetLeadRuntimeSettingPersistsMasterOrchestratorRuntime(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var view leadSettingsView
	json.Unmarshal(call(t, c, "1", "settings.get", nil), &view)
	if view.Lead.Harness != "" || view.Lead.Model != "" {
		t.Fatalf("lead runtime should start unset, got %+v", view.Lead)
	}

	json.Unmarshal(call(t, c, "2", "settings.set", map[string]any{
		"lead": map[string]any{"harness": "claude", "model": "claude-sonnet-4-5"},
	}), &view)
	if view.Lead.Harness != contracts.HarnessClaude || view.Lead.Model != "claude-sonnet-4-5" {
		t.Fatalf("lead setting = %+v, want claude/claude-sonnet-4-5", view.Lead)
	}
	if h, m := orchestratorRole(t, view); h != "claude" || m != "claude-sonnet-4-5" {
		t.Fatalf("orchestrator role = %s/%s, want claude/claude-sonnet-4-5", h, m)
	}

	// Re-read to confirm the pin survived the round trip.
	json.Unmarshal(call(t, c, "3", "settings.get", nil), &view)
	if view.Lead.Harness != contracts.HarnessClaude || view.Lead.Model != "claude-sonnet-4-5" {
		t.Fatalf("lead setting not persisted: %+v", view.Lead)
	}

	// Clearing the pin returns the lead to the orchestrator role's defaults.
	json.Unmarshal(call(t, c, "4", "settings.set", map[string]any{
		"lead": map[string]any{"harness": "", "model": ""},
	}), &view)
	if view.Lead.Harness != "" || view.Lead.Model != "" {
		t.Fatalf("lead setting not cleared: %+v", view.Lead)
	}
}

// TestSetLeadRuntimeSettingRefusesUnknownHarness covers validation: a bad
// harness is refused before any part of the patch is applied.
func TestSetLeadRuntimeSettingRefusesUnknownHarness(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	resp := callRaw(t, c, "1", "settings.set", map[string]any{
		"autoMode": true,
		"lead":     map[string]any{"harness": "not-a-harness", "model": "m"},
	})
	if resp.Error == nil {
		t.Fatal("unknown harness accepted, want error")
	}
	if !strings.Contains(resp.Error.Message, "unknown harness") {
		t.Fatalf("error = %q, want unknown harness", resp.Error.Message)
	}

	var view leadSettingsView
	var settings struct {
		AutoMode bool `json:"autoMode"`
	}
	json.Unmarshal(call(t, c, "2", "settings.get", nil), &view)
	json.Unmarshal(call(t, c, "3", "settings.get", nil), &settings)
	if view.Lead.Harness != "" || view.Lead.Model != "" {
		t.Fatalf("refused patch still pinned the lead: %+v", view.Lead)
	}
	if settings.AutoMode {
		t.Fatal("refused patch still applied autoMode")
	}
}
