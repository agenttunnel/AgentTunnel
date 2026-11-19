package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// TestThreadChangedFollowsDurableEvents: every client hears a slim
// thread.changed frame per durable event on any thread, without subscribing,
// and ephemeral agent output is not announced.
func TestThreadChangedFollowsDurableEvents(t *testing.T) {
	d, srv, url := newOpenFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitDeadline)
	defer cancel()
	srv.FollowThreadChanges(ctx)

	ws, _, err := websocket.Dial(ctx, url+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()
	// A reply proves the connection is registered for broadcasts.
	req, _ := json.Marshal(map[string]any{"id": "1", "method": "thread.list"})
	if err := ws.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	}

	d.engine.Publish("t1", contracts.AgentOutput{AgentID: "a", Kind: "text", Text: "hi"})
	if err := d.engine.Dispatch(ctx, contracts.Command{ID: "c0", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	// Creation is provisional; the first durable command writes the log.
	if err := d.engine.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadRename, ThreadID: "t1", Title: "named"}); err != nil {
		t.Fatal(err)
	}

	_, msg, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Method string       `json:"method"`
		Params threadChange `json:"params"`
	}
	if err := json.Unmarshal(msg, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Method != "thread.changed" || frame.Params.ThreadID != "t1" || frame.Params.Seq != 1 || frame.Params.Type != contracts.EvtThreadCreated {
		t.Fatalf("first frame = %s, want thread.changed for t1 seq 1 thread.created", msg)
	}
}
