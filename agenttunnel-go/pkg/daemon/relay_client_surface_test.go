package daemon

// The relay ships as a separate process a mobile phone pairs with, but the
// contract it forwards is this daemon's WS surface. These tests hold the two
// halves of that contract steady until it exists:
//
//   - a second concurrent connection presenting the same token (what a relay
//     presenting the instance token looks like) receives the same
//     thread.event frames as the first client;
//   - a client that drops and reconnects with the same token sees the
//     thread again in thread.list and its thread.event frames resume after
//     re-subscribing, on a turn started after the reconnect.
//
// All waits are on typed events and replies, never sleeps.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// One server for the whole test: both clients hit the same listener, like a
// desktop and a relayed phone hitting one atd.
func wsBase(t *testing.T, d *Daemon) string {
	t.Helper()
	srv := httptest.NewServer(d.Server.Handler())
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

func dialAt(t *testing.T, base, token string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, base+"?token="+token, nil)
	if err != nil {
		t.Fatalf("dial with token %q: %v", token, err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

// waitForEvent reads frames until a thread.event of the wanted type arrives.
func waitForEvent(t *testing.T, c *websocket.Conn, want contracts.EventType) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read waiting for %s: %v", want, err)
		}
		var resp transport.Response
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue
		}
		if resp.Method != "thread.event" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(mustJSON(t, resp.Params), &ev); err != nil {
			continue
		}
		if ev.Type == string(want) {
			return
		}
	}
}

// waitForReply reads frames until the reply with the given id arrives.
func waitForReply(t *testing.T, c *websocket.Conn, id string) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read waiting for reply %s: %v", id, err)
		}
		var resp transport.Response
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			t.Fatalf("reply %s error: %s", id, resp.Error.Message)
		}
		return mustJSON(t, resp.Result)
	}
}

// startTurn writes a thread.turn.start request in the background and returns
// the reply id, so the caller can wait on events while the turn runs. The
// reply to turn.start arrives only when the turn finishes.
func startTurn(t *testing.T, c *websocket.Conn, id, threadID, text string) string {
	t.Helper()
	req := transport.Request{
		ID:     id,
		Method: "thread.turn.start",
		Params: mustJSON(t, map[string]any{"threadId": threadID, "text": text}),
	}
	go func() {
		data, _ := json.Marshal(req)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.Write(ctx, websocket.MessageText, data); err != nil {
			t.Errorf("write turn.start: %v", err)
		}
	}()
	return id
}

func TestDaemonSecondClientReceivesThreadEvents(t *testing.T) {
	d := newTestDaemon(t)
	base := wsBase(t, d)

	desktop := dialAt(t, base, "secret")
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, desktop, "1", "thread.create", map[string]any{"title": "relay"}), &created)
	call(t, desktop, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	// The phone: a second concurrent connection, same token, same thread.
	phone := dialAt(t, base, "secret")
	call(t, phone, "10", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	// The phone starts the turn on its own socket.
	startTurn(t, phone, "11", string(created.ThreadID), "build it")

	// Live output (what the mobile chat renders) reaches the phone...
	waitForEvent(t, phone, contracts.EvtAgentOutput)
	// ...and the same turn's state events (what the desktop graph renders)
	// reach the first client on its own connection.
	waitForEvent(t, desktop, contracts.EvtTaskDispatched)
	waitForReply(t, phone, "11")
}

func TestDaemonReconnectResumesThreadEvents(t *testing.T) {
	d := newTestDaemon(t)
	base := wsBase(t, d)

	c1 := dialAt(t, base, "secret")
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c1, "1", "thread.create", map[string]any{"title": "resume"}), &created)
	call(t, c1, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	// One full turn, then the client goes away.
	startTurn(t, c1, "3", string(created.ThreadID), "first")
	waitForEvent(t, c1, contracts.EvtAgentOutput)
	waitForReply(t, c1, "3")
	if err := c1.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("close first client: %v", err)
	}

	// The client comes back on a new connection with the same token.
	c2 := dialAt(t, base, "secret")

	// The thread survived the disconnect and is listed again.
	var threads []struct {
		ID contracts.ThreadID `json:"id"`
	}
	json.Unmarshal(call(t, c2, "4", "thread.list", nil), &threads)
	found := false
	for _, th := range threads {
		if th.ID == created.ThreadID {
			found = true
		}
	}
	if !found {
		t.Fatalf("thread %s missing from thread.list after reconnect: %+v", created.ThreadID, threads)
	}

	call(t, c2, "5", "thread.subscribe", map[string]any{"threadId": created.ThreadID})
	startTurn(t, c2, "6", string(created.ThreadID), "second")

	// Events from the new turn flow to the reconnected client.
	waitForEvent(t, c2, contracts.EvtMessageSent)
	waitForEvent(t, c2, contracts.EvtAgentOutput)
	waitForReply(t, c2, "6")
}
