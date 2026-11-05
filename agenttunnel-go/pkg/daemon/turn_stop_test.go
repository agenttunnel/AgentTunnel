package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// slowScript reports one line of text and then blocks, so the test has a
// running agent it can stop mid-turn.
const slowScript = `printf '{"type":"text","text":"starting"}\n'
sleep 30
printf '{"type":"text","text":"done"}\n'`

func newSlowDaemon(t *testing.T) *Daemon {
	t.Helper()
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", slowScript}, PromptVia: "stdin"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: t.TempDir(),
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		Config:   orchestrator.Config{MaxPasses: 2, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// readResult blocks until the response for id arrives, skipping pushed events.
func readResult(t *testing.T, c *websocket.Conn, ctx context.Context, id string) json.RawMessage {
	t.Helper()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		var resp transport.Response
		if err := json.Unmarshal(msg, &resp); err != nil || resp.ID != id {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("%s: %s", id, resp.Error.Message)
		}
		return mustJSON(t, resp.Result)
	}
}

// agentStatus polls agents.list for one agent's status, returning "" until known.
func agentStatus(t *testing.T, c *websocket.Conn, threadID contracts.ThreadID, agentID contracts.AgentID) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := transport.Request{ID: "poll", Method: "agents.list", Params: mustJSON(t, map[string]any{"threadId": threadID})}
	data, _ := json.Marshal(req)
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write agents.list: %v", err)
	}
	var agents []struct {
		Agent struct {
			ID     contracts.AgentID `json:"id"`
			Status string            `json:"status"`
		} `json:"agent"`
	}
	raw := readResult(t, c, ctx, "poll")
	if err := json.Unmarshal(raw, &agents); err != nil {
		t.Fatalf("agents.list: %v", err)
	}
	for _, a := range agents {
		if a.Agent.ID == agentID {
			return a.Agent.Status
		}
	}
	return ""
}

func waitForAgentStatus(t *testing.T, c *websocket.Conn, threadID contracts.ThreadID, agentID contracts.AgentID, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := agentStatus(t, c, threadID, agentID); got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("agent %s never reached status %q", agentID, want)
}

func TestTurnStopCancelsRunningTurn(t *testing.T) {
	d := newSlowDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "stop me"}), &created)

	// The turn blocks until the (sleeping) agent settles; fire it in the
	// background on its own connection so this one stays free to stop it.
	go func() {
		bg, _ := dialWS(t, d)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		req := transport.Request{
			ID:     "turn",
			Method: "thread.turn.start",
			Params: mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "do something slow"}),
		}
		data, _ := json.Marshal(req)
		_ = bg.Write(ctx, websocket.MessageText, data)
	}()

	waitForAgentStatus(t, c, created.ThreadID, "agent-orchestrator", "running")

	var stopReceipt struct {
		OK      bool `json:"ok"`
		Stopped bool `json:"stopped"`
	}
	json.Unmarshal(call(t, c, "3", "thread.turn.stop", map[string]any{"threadId": created.ThreadID}), &stopReceipt)
	if !stopReceipt.OK || !stopReceipt.Stopped {
		t.Fatalf("stop receipt = %+v, want stopped", stopReceipt)
	}

	// The stopped agent winds up cancelled, not failed, and the turn slot is
	// freed once it unwinds.
	waitForAgentStatus(t, c, created.ThreadID, "agent-orchestrator", "cancelled")

	// A second turn on the same thread is accepted: the gate released with the
	// stopped turn. It will also be slow, so stop it too rather than waiting.
	go func() {
		bg2, _ := dialWS(t, d)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		req := transport.Request{
			ID:     "turn2",
			Method: "thread.turn.start",
			Params: mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "and again"}),
		}
		data, _ := json.Marshal(req)
		_ = bg2.Write(ctx, websocket.MessageText, data)
	}()
	waitForAgentStatus(t, c, created.ThreadID, "agent-orchestrator", "running")
	call(t, c, "4", "thread.turn.stop", map[string]any{"threadId": created.ThreadID})
	waitForAgentStatus(t, c, created.ThreadID, "agent-orchestrator", "cancelled")
}

func TestConcurrentTurnsAreRejected(t *testing.T) {
	d := newSlowDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "busy"}), &created)

	started := make(chan struct{})
	go func() {
		bg, _ := dialWS(t, d)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		req := transport.Request{
			ID: "turn", Method: "thread.turn.start",
			Params: mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "slow work"}),
		}
		data, _ := json.Marshal(req)
		_ = bg.Write(ctx, websocket.MessageText, data)
		close(started)
	}()
	<-started

	waitForAgentStatus(t, c, created.ThreadID, "agent-orchestrator", "running")

	// A competing turn on the same thread while one runs is an error.
	if _, err := tryCall(t, c, "2", "thread.turn.start", map[string]any{
		"threadId": created.ThreadID, "text": "competing turn",
	}); err == nil {
		t.Fatal("competing turn on a busy thread was accepted")
	}

	call(t, c, "3", "thread.turn.stop", map[string]any{"threadId": created.ThreadID})
	waitForAgentStatus(t, c, created.ThreadID, "agent-orchestrator", "cancelled")
}

// tryCall is call, but returns the RPC error instead of failing the test.
func tryCall(t *testing.T, c *websocket.Conn, id, method string, params any) (json.RawMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := transport.Request{ID: id, Method: method, Params: mustJSON(t, params)}
	data, _ := json.Marshal(req)
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read %s: %v", method, err)
		}
		var resp transport.Response
		if err := json.Unmarshal(msg, &resp); err != nil || resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, errors.New(resp.Error.Message)
		}
		return mustJSON(t, resp.Result), nil
	}
}
