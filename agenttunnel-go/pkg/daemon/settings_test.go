package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

func TestSettingsRoundTrip(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var view struct {
		Workdir        string   `json:"workdir"`
		AutoMode       bool     `json:"autoMode"`
		ApprovedModels []string `json:"approvedModels"`
		Roles          []struct {
			ID      string `json:"id"`
			Harness string `json:"harness"`
			Model   string `json:"model"`
		} `json:"roles"`
	}
	find := func(id string) (string, string) {
		for _, r := range view.Roles {
			if r.ID == id {
				return r.Harness, r.Model
			}
		}
		return "", ""
	}

	json.Unmarshal(call(t, c, "1", "settings.get", nil), &view)
	if len(view.Roles) < 5 {
		t.Fatalf("roles = %d, want >= 5", len(view.Roles))
	}

	json.Unmarshal(call(t, c, "2", "settings.set", map[string]any{
		"autoMode":       true,
		"approvedModels": []string{"sonnet"},
		"role":           map[string]any{"id": "executor", "harness": "claude", "model": "sonnet"},
	}), &view)

	if !view.AutoMode {
		t.Fatal("auto mode not persisted")
	}
	if h, m := find("executor"); h != "claude" || m != "sonnet" {
		t.Fatalf("executor override = %s/%s", h, m)
	}

	// Re-read to confirm the change stuck.
	json.Unmarshal(call(t, c, "3", "settings.get", nil), &view)
	if h, m := find("executor"); h != "claude" || m != "sonnet" {
		t.Fatalf("override not persisted: %s/%s", h, m)
	}
}

// callRaw sends an RPC and returns the full response, including any error,
// so tests can assert on refusals.
func callRaw(t *testing.T, c *websocket.Conn, id, method string, params any) transport.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, _ := json.Marshal(transport.Request{ID: id, Method: method, Params: mustJSON(t, params)})
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read %s: %v", method, err)
		}
		var resp transport.Response
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue
		}
		if resp.Method == "thread.event" || resp.ID != id {
			continue
		}
		return resp
	}
}

type roleView struct {
	ID           string `json:"id"`
	Enabled      bool   `json:"enabled"`
	Required     bool   `json:"required"`
	Experimental bool   `json:"experimental"`
}

type settingsViewJSON struct {
	AutoMode          bool       `json:"autoMode"`
	ExperimentalRoles bool       `json:"experimentalRoles"`
	Roles             []roleView `json:"roles"`
}

func findRole(t *testing.T, view settingsViewJSON, id string) roleView {
	t.Helper()
	for _, r := range view.Roles {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("role %q missing from settings view", id)
	return roleView{}
}

func TestSettingsRoleEnabledRoundTrip(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var view settingsViewJSON
	json.Unmarshal(call(t, c, "1", "settings.get", nil), &view)
	if r := findRole(t, view, "tester"); !r.Enabled || r.Required {
		t.Fatalf("tester by default = %+v, want enabled and not required", r)
	}
	if r := findRole(t, view, "reviewer"); !r.Required || !r.Enabled {
		t.Fatalf("reviewer = %+v, want required and enabled", r)
	}
	if r := findRole(t, view, "context-manager"); !r.Experimental {
		t.Fatalf("context-manager = %+v, want experimental", r)
	}

	json.Unmarshal(call(t, c, "2", "settings.set", map[string]any{
		"roleEnabled": map[string]any{"id": "tester", "enabled": false},
	}), &view)
	if r := findRole(t, view, "tester"); r.Enabled {
		t.Fatalf("tester still enabled after disable: %+v", r)
	}

	// Re-read to confirm the disable persisted in the store.
	json.Unmarshal(call(t, c, "3", "settings.get", nil), &view)
	if r := findRole(t, view, "tester"); r.Enabled {
		t.Fatalf("tester disable not persisted: %+v", r)
	}

	json.Unmarshal(call(t, c, "4", "settings.set", map[string]any{
		"roleEnabled": map[string]any{"id": "tester", "enabled": true},
	}), &view)
	if r := findRole(t, view, "tester"); !r.Enabled {
		t.Fatalf("tester not re-enabled: %+v", r)
	}
}

func TestSettingsRefusesDisablingRequiredRole(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	resp := callRaw(t, c, "1", "settings.set", map[string]any{
		"autoMode":    true,
		"roleEnabled": map[string]any{"id": "reviewer", "enabled": false},
	})
	if resp.Error == nil {
		t.Fatal("disabling a required role succeeded, want error")
	}
	if want := `required role "reviewer" cannot be disabled`; resp.Error.Message != want {
		t.Fatalf("error = %q, want %q", resp.Error.Message, want)
	}

	// The whole patch is validated first, so the autoMode change in the same
	// request must not have been applied.
	var view settingsViewJSON
	json.Unmarshal(call(t, c, "2", "settings.get", nil), &view)
	if view.AutoMode {
		t.Fatal("refused patch still applied autoMode")
	}
	if r := findRole(t, view, "reviewer"); !r.Enabled {
		t.Fatalf("required role disabled despite refusal: %+v", r)
	}
}

func TestSettingsExperimentalRolesToggle(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var view settingsViewJSON
	json.Unmarshal(call(t, c, "1", "settings.get", nil), &view)
	if view.ExperimentalRoles {
		t.Fatal("experimentalRoles on by default, want off")
	}

	on := true
	json.Unmarshal(call(t, c, "2", "settings.set", map[string]any{"experimentalRoles": on}), &view)
	if !view.ExperimentalRoles {
		t.Fatal("experimentalRoles not enabled")
	}

	json.Unmarshal(call(t, c, "3", "settings.get", nil), &view)
	if !view.ExperimentalRoles {
		t.Fatal("experimentalRoles not persisted")
	}

	json.Unmarshal(call(t, c, "4", "settings.set", map[string]any{"experimentalRoles": false}), &view)
	if view.ExperimentalRoles {
		t.Fatal("experimentalRoles not disabled")
	}
}
