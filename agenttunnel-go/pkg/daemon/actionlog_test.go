package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// A run must leave a structured, greppable record of what happened.
func TestDaemonWritesAnActionLog(t *testing.T) {
	stateDir := t.TempDir()
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

	c, _ := dialWS(t, d)
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "logged"}), &created)
	call(t, c, "2", "thread.turn.start", map[string]any{
		"threadId": created.ThreadID, "text": "build it",
	})
	d.Close()

	data, err := os.ReadFile(filepath.Join(stateDir, "logs", "actions.jsonl"))
	if err != nil {
		t.Fatalf("read action log: %v", err)
	}

	kinds := map[string]int{}
	lines := 0
	for _, raw := range strings.Split(string(data), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		lines++
		var rec map[string]any
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			t.Fatalf("action log line is not JSON: %v (%s)", err, raw)
		}
		kind, _ := rec["kind"].(string)
		kinds[kind]++
		if _, ok := rec["at"]; !ok {
			t.Fatalf("record missing a timestamp: %s", raw)
		}
	}

	for _, want := range []string{"daemon.start", "rpc", "command", "event", "agent.spawn", "agent.exit", "daemon.stop"} {
		if kinds[want] == 0 {
			t.Errorf("action log has no %q records\nkinds=%v", want, kinds)
		}
	}
	if lines < 10 {
		t.Errorf("action log looks too small: %d lines", lines)
	}

	// Secrets must never reach the log.
	if strings.Contains(string(data), "secret") {
		t.Error("the auth token leaked into the action log")
	}
}
