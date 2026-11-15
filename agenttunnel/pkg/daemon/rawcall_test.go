package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// rawCall sends a request and returns both the result and any RPC error, for
// tests that assert on failures (the shared call helper fails the test instead).
func rawCall(t *testing.T, c *websocket.Conn, id, method string, params any) (json.RawMessage, *transport.RPCError) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
		if json.Unmarshal(msg, &resp) != nil || resp.Method == "thread.event" || resp.ID != id {
			continue
		}
		return mustJSON(t, resp.Result), resp.Error
	}
}
