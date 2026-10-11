package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
)

// TestLiveThreadIsNamedByTheModel runs a real turn and asserts the orchestrator's
// model renames the thread, rather than it staying "New thread".
//
//	AGENTTUNNEL_LIVE_TESTS=1 go test ./pkg/daemon/ -run LiveThread -v
func TestLiveThreadIsNamedByTheModel(t *testing.T) {
	if os.Getenv("AGENTTUNNEL_LIVE_TESTS") != "1" {
		t.Skip("set AGENTTUNNEL_LIVE_TESTS=1 to run a real turn")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not installed")
	}

	d, err := New(context.Background(), Options{
		StateDir: t.TempDir(),
		Token:    "secret",
		Config:   orchestrator.Config{MaxPasses: 1, MaxConcurrency: 1},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	defer d.Close()

	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": ""}), &created)
	call(t, c, "2", "thread.turn.start", map[string]any{
		"threadId": created.ThreadID,
		"text":     "Explain what a goroutine is, briefly.",
	})

	// Naming happens off the turn, so poll for it.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var state struct {
			Title string `json:"title"`
		}
		json.Unmarshal(call(t, c, "3", "thread.get", map[string]any{
			"threadId": created.ThreadID,
		}), &state)
		if state.Title != "" && state.Title != contracts.DefaultThreadTitle {
			t.Logf("model named the thread %q", state.Title)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("thread was never named by the model")
}
