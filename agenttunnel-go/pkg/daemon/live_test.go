package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// TestLiveTurnStreamsAgentOutput drives a real turn through the real opencode CLI
// and asserts that agent output is streamed to a subscriber. Opt-in:
//
//	AGENTTUNNEL_LIVE_TESTS=1 go test ./pkg/daemon/ -run Live
func TestLiveTurnStreamsAgentOutput(t *testing.T) {
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
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "live"}), &created)
	call(t, c, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	// turn.start blocks until the turn settles, so fire it in the background.
	go func() {
		data, _ := json.Marshal(transport.Request{
			ID:     "3",
			Method: "thread.turn.start",
			Params: mustJSON(t, map[string]any{
				"threadId": created.ThreadID,
				"text":     "Reply with only the single word: pong",
			}),
		})
		_ = c.Write(context.Background(), websocket.MessageText, data)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var sawOutput bool
	turnDone := false
	for !turnDone {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (sawOutput=%v)", err, sawOutput)
		}
		var resp transport.Response
		if json.Unmarshal(msg, &resp) != nil {
			continue
		}
		if resp.Method == "thread.event" {
			var ev struct {
				Type string `json:"type"`
			}
			json.Unmarshal(mustJSON(t, resp.Params), &ev)
			if ev.Type == "agent.output" {
				sawOutput = true
			}
			continue
		}
		if resp.ID == "3" {
			turnDone = true
			if resp.Error != nil {
				t.Fatalf("turn.start failed: %s", resp.Error.Message)
			}
		}
	}
	if !sawOutput {
		t.Fatal("turn completed but no agent.output was streamed")
	}
	if !sawOutput {
		t.Fatal("no agent.output streamed during the turn")
	}
	t.Log("streamed live agent output during a real opencode turn")
}
