package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

func TestSettingsSurviveDaemonRestart(t *testing.T) {
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
	call(t, c1, "1", "settings.set", map[string]any{
		"autoMode": true,
		"workdir":  "/tmp/persisted",
		"harness":  map[string]any{"id": "codex", "enabled": false},
		"role":     map[string]any{"id": "executor", "harness": "claude", "model": "sonnet"},
	})
	d1.Close()

	// A fresh daemon over the same state directory must see the saved settings.
	d2 := open()
	defer d2.Close()
	c2, _ := dialWS(t, d2)

	var view struct {
		Workdir  string `json:"workdir"`
		AutoMode bool   `json:"autoMode"`
		Roles    []struct {
			ID      string `json:"id"`
			Harness string `json:"harness"`
			Model   string `json:"model"`
		} `json:"roles"`
	}
	json.Unmarshal(call(t, c2, "1", "settings.get", nil), &view)

	if !view.AutoMode {
		t.Error("auto mode did not survive the restart")
	}
	if view.Workdir != "/tmp/persisted" {
		t.Errorf("workdir = %q", view.Workdir)
	}
	found := false
	for _, r := range view.Roles {
		if r.ID == "executor" {
			found = true
			if r.Harness != "claude" || r.Model != "sonnet" {
				t.Errorf("executor role = %+v", r)
			}
		}
	}
	if !found {
		t.Error("executor role missing")
	}

	var list struct {
		Enabled []string `json:"enabled"`
	}
	json.Unmarshal(call(t, c2, "2", "harness.list", nil), &list)
	for _, h := range list.Enabled {
		if h == "codex" {
			t.Error("codex should stay disabled across the restart")
		}
	}
}
