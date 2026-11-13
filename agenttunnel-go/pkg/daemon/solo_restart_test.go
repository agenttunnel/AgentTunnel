package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// A solo runtime set over the WebSocket survives a daemon restart: the
// persisted event log replays it back into the thread state, and the boot
// sweep that drops never-used threads keeps it (the runtime event counts as
// content).
func TestSoloRuntimeSurvivesDaemonRestart(t *testing.T) {
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
	json.Unmarshal(call(t, c1, "1", "thread.create", map[string]any{"title": "solo durable"}), &created)
	var ack struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(call(t, c1, "2", "thread.setSolo", map[string]any{
		"threadId": created.ThreadID, "harness": "claude", "model": "sonnet",
	}), &ack)
	if !ack.OK {
		t.Fatal("thread.setSolo should acknowledge")
	}
	d1.Close()

	// A fresh daemon over the same state directory still knows the thread is
	// solo, with the harness and model it was set to.
	d2 := open()
	defer d2.Close()
	c2, _ := dialWS(t, d2)

	var state struct {
		SoloRuntime *struct {
			Harness contracts.Harness `json:"harness"`
			Model   string            `json:"model"`
		} `json:"soloRuntime"`
	}
	json.Unmarshal(call(t, c2, "1", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)
	if state.SoloRuntime == nil || state.SoloRuntime.Harness != contracts.HarnessClaude || state.SoloRuntime.Model != "sonnet" {
		t.Fatalf("soloRuntime after restart = %+v, want claude/sonnet", state.SoloRuntime)
	}

	// The sweep at boot must not have swept the solo thread away either.
	var list []struct {
		ID string `json:"id"`
	}
	json.Unmarshal(call(t, c2, "2", "thread.list", nil), &list)
	for _, row := range list {
		if row.ID == string(created.ThreadID) {
			return
		}
	}
	t.Fatalf("thread %s missing after restart: %+v", created.ThreadID, list)
}
