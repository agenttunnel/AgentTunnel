package daemon

import (
	"encoding/json"
	"testing"
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
