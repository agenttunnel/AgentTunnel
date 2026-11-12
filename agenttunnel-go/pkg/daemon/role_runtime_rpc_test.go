package daemon

import (
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// TestSetRoleRuntimeRPCPersistsPerThread covers thread.setRoleRuntime over the
// WebSocket surface: the override is stored on the thread state the template
// and job commands read back, and stays with its thread.
func TestSetRoleRuntimeRPCPersistsPerThread(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var first, second struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "job one"}), &first)
	json.Unmarshal(call(t, c, "2", "thread.create", map[string]any{"title": "job two"}), &second)

	var ok struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(call(t, c, "3", "thread.setRoleRuntime", map[string]any{
		"threadId": first.ThreadID, "role": "executor", "harness": "codex", "model": "gpt-x", "variant": "high",
	}), &ok)
	if !ok.OK {
		t.Fatal("setRoleRuntime should acknowledge")
	}

	var view struct {
		RoleRuntimes map[string]contracts.LeadRuntime `json:"roleRuntimes"`
	}
	json.Unmarshal(call(t, c, "4", "thread.get", map[string]any{"threadId": first.ThreadID}), &view)
	got, exists := view.RoleRuntimes["executor"]
	if !exists || got.Harness != contracts.HarnessCodex || got.Model != "gpt-x" || got.Variant != "high" {
		t.Fatalf("executor runtime not persisted: %+v", view.RoleRuntimes)
	}

	// The other thread inherits settings and its default.
	view.RoleRuntimes = nil
	json.Unmarshal(call(t, c, "5", "thread.get", map[string]any{"threadId": second.ThreadID}), &view)
	if len(view.RoleRuntimes) != 0 {
		t.Fatalf("thread two picked up thread one's runtime: %+v", view.RoleRuntimes)
	}

	// An unknown role is refused by the decider, not silently stored.
	if _, rpcErr := rawCall(t, c, "6", "thread.setRoleRuntime", map[string]any{
		"threadId": first.ThreadID, "role": "no-such-role", "model": "m",
	}); rpcErr == nil {
		t.Fatal("an unknown role should be refused")
	}
}
