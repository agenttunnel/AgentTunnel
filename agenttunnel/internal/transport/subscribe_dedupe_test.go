package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// Re-subscribing to a thread on one socket must not fan its events out twice:
// the TUI re-opens a thread after every Esc, and each thread.subscribe used to
// start another forwarder for the same engine channel.
func TestSubscribeTwiceDeliversEachEventOnce(t *testing.T) {
	eng, disp := newCommandFixture(t)
	srv := httptest.NewServer(NewServer(disp, eng, disp.runner, "", toolbox.New(), nil).Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })

	for _, id := range []string{"sub1", "sub2"} {
		wsSend(ctx, t, c, Request{ID: id, Method: "thread.subscribe", Params: mustJSON(t, map[string]string{"threadId": "t1"})})
		wsReply(ctx, t, c, id)
	}

	// The engine's own subscription counts the committed events: setThreadSolo
	// commits, and the channel is filled before the reply is sent.
	committed, unsub := eng.Subscribe("t1")
	t.Cleanup(unsub)
	wsSend(ctx, t, c, Request{ID: "solo", Method: "thread.setSolo",
		Params: mustJSON(t, map[string]string{"threadId": "t1", "harness": "claude", "model": "sonnet"})})
	frames := readUntilReply(ctx, t, c, "solo")
	// The events are committed before the reply is sent, so the engine channel
	// holds the full count now.
	want := len(committed)
	if want == 0 {
		t.Fatal("thread.setSolo committed no events to fan out")
	}
	frames += readEventFrames(ctx, t, c, want-frames)

	// A barrier reply: any frame a duplicate forwarder still owes arrives before it.
	wsSend(ctx, t, c, Request{ID: "barrier", Method: "thread.list"})
	frames += readUntilReply(ctx, t, c, "barrier")
	if frames != want {
		t.Fatalf("socket got %d thread.event frames for %d committed events; re-subscribing must not duplicate", frames, want)
	}
}

// readUntilReply reads until the reply to id arrives and returns how many
// thread.event frames came first.
func readUntilReply(ctx context.Context, t *testing.T, c *websocket.Conn, id string) int {
	t.Helper()
	events := 0
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read reply %s: %v", id, err)
		}
		var frame struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(msg, &frame); err != nil {
			continue
		}
		if frame.Method == "thread.event" {
			events++
		}
		if frame.ID == id {
			return events
		}
	}
}

// readEventFrames blocks until n thread.event frames have arrived.
func readEventFrames(ctx context.Context, t *testing.T, c *websocket.Conn, n int) int {
	t.Helper()
	seen := 0
	for seen < n {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read event frame: %v", err)
		}
		var frame struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(msg, &frame); err == nil && frame.Method == "thread.event" {
			seen++
		}
	}
	return seen
}
