package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// A master conversation is the home screen's chat history: creating one,
// exchanging a message, and restarting the daemon must leave it in
// master.history.list and master.history.open with the full transcript. This
// pins the whole persistence path (thread log plus the boot sweep) rather
// than DropEmptyThreads alone.
func TestDaemonMasterConversationSurvivesRestart(t *testing.T) {
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
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(call(t, c1, "1", "master.history.new", map[string]any{"title": "Home chat"}), &created); err != nil {
		t.Fatalf("decode master.history.new: %v", err)
	}
	if created.ID == "" || created.Title != "Home chat" {
		t.Fatalf("created = %+v", created)
	}

	// One exchange, so the transcript — not just the creation — must survive.
	var sent struct {
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(call(t, c1, "2", "master.send", map[string]any{"text": "anything running?", "threadId": created.ID}), &sent); err != nil {
		t.Fatalf("decode master.send: %v", err)
	}
	if sent.Reply == "" {
		t.Fatal("master.send returned no reply")
	}

	d1.Close()

	d2 := open()
	defer d2.Close()
	c2, _ := dialWS(t, d2)

	var rows []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Messages int    `json:"messages"`
	}
	if err := json.Unmarshal(call(t, c2, "1", "master.history.list", nil), &rows); err != nil {
		t.Fatalf("decode master.history.list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != created.ID || rows[0].Title != "Home chat" || rows[0].Messages != 2 {
		t.Fatalf("list after restart = %+v, want the conversation with its exchange", rows)
	}

	var detail struct {
		Title    string `json:"title"`
		Messages []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(call(t, c2, "2", "master.history.open", map[string]any{"threadId": created.ID}), &detail); err != nil {
		t.Fatalf("decode master.history.open: %v", err)
	}
	if detail.Title != "Home chat" || len(detail.Messages) != 2 {
		t.Fatalf("detail after restart = %+v", detail)
	}
	if detail.Messages[0].Role != "user" || detail.Messages[0].Text != "anything running?" {
		t.Fatalf("user message after restart = %+v", detail.Messages[0])
	}
	if detail.Messages[1].Role != "assistant" || detail.Messages[1].Text != sent.Reply {
		t.Fatalf("reply after restart = %+v, want the recorded reply %q", detail.Messages[1], sent.Reply)
	}
}
