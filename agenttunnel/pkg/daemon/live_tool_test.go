package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// TestLiveOrchestratorUsesTheToolCommand runs a real turn and asserts the
// orchestrator actually delegates by running `tool dispatch`, so the work shows
// up as a subagent rather than being done inline.
//
//	AGENTTUNNEL_LIVE_TESTS=1 go test ./pkg/daemon/ -run LiveOrchestrator -v
func TestLiveOrchestratorUsesTheToolCommand(t *testing.T) {
	if os.Getenv("AGENTTUNNEL_LIVE_TESTS") != "1" {
		t.Skip("set AGENTTUNNEL_LIVE_TESTS=1 to run a real turn")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not installed")
	}

	// The agent must run a real binary that understands `tool …`; the test
	// binary does not.
	toolBin := filepath.Join(t.TempDir(), "atd")
	build := exec.Command("go", "build", "-o", toolBin, "github.com/agenttunnel/agenttunnel-go/cmd/atd")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build atd: %v\n%s", err, out)
	}

	d, err := New(context.Background(), Options{
		StateDir: t.TempDir(),
		Token:    "secret",
		ToolPath: toolBin,
		Config:   orchestrator.Config{MaxPasses: 3, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	defer d.Close()

	srv := httptest.NewServer(d.Server.Handler())
	defer srv.Close()
	d.SetEndpoint(srv.URL)

	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "live tool"}), &created)
	call(t, c, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	go func() {
		data, _ := json.Marshal(transport.Request{
			ID:     "3",
			Method: "thread.turn.start",
			Params: mustJSON(t, map[string]any{
				"threadId": created.ThreadID,
				"text": "Delegate exactly one job to an executor named probe: create a file named " +
					"probe.txt containing the word hello. Use the dispatch tool; do not do it yourself.",
			}),
		})
		_ = c.Write(context.Background(), websocket.MessageText, data)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	turnDone := false
	for !turnDone {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp transport.Response
		if json.Unmarshal(msg, &resp) != nil {
			continue
		}
		if resp.Method == "thread.event" {
			continue
		}
		if resp.ID == "3" {
			turnDone = true
			if resp.Error != nil {
				t.Fatalf("turn failed: %s", resp.Error.Message)
			}
		}
	}

	state, err := d.Engine.State(context.Background(), created.ThreadID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.AgentOrder) < 2 {
		t.Fatalf("expected the orchestrator to delegate, but only %d agent(s) ran", len(state.AgentOrder))
	}
	for _, id := range state.AgentOrder {
		t.Logf("agent %s role=%s status=%s", id, state.Agents[id].Role, state.Agents[id].Status)
	}
}
