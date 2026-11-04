package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

func TestThreadsSurviveDaemonRestart(t *testing.T) {
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
	json.Unmarshal(call(t, c1, "1", "thread.create", map[string]any{"title": "durable"}), &created)
	call(t, c1, "2", "thread.turn.start", map[string]any{
		"threadId": created.ThreadID,
		"text":     "build it",
	})
	d1.Close()

	// A fresh daemon over the same state directory must find the thread, its
	// messages, and its agents by replaying the persisted event log.
	d2 := open()
	defer d2.Close()
	c2, _ := dialWS(t, d2)

	var list []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Agents int    `json:"agents"`
	}
	json.Unmarshal(call(t, c2, "1", "thread.list", nil), &list)
	if len(list) != 1 || list[0].ID != string(created.ThreadID) || list[0].Title != "durable" {
		t.Fatalf("threads after restart = %+v", list)
	}

	var state struct {
		Messages []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		} `json:"messages"`
		Agents map[string]contracts.Agent `json:"agents"`
	}
	json.Unmarshal(call(t, c2, "2", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)

	if len(state.Messages) == 0 {
		t.Fatal("messages did not survive the restart")
	}
	if _, ok := state.Agents["agent-orchestrator"]; !ok {
		t.Fatalf("agents did not survive the restart: %+v", state.Agents)
	}
}
