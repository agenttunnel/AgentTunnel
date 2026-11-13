package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// A client frame bigger than websocket's 32768-byte default (a
// thread.turn.start with a long prompt) must still be dispatched and answered
// rather than failing the read and closing the connection.
func TestHandleWSAcceptsFrameOverDefaultLimit(t *testing.T) {
	eng, disp := newCommandFixture(t)
	srv := httptest.NewServer(NewServer(disp, eng, disp.runner, "", toolbox.New(), nil).Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(readLimit)

	// project.list ignores its params, so the padding only makes the frame
	// big; the reply proves the server read and dispatched it.
	frame := fmt.Sprintf(`{"id":"big-1","method":"project.list","params":{"pad":%q}}`, strings.Repeat("x", 128*1024))
	if err := ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read reply: %v (the server rejected the large frame)", err)
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("decode reply %s: %v", data, err)
	}
	if resp.ID != "big-1" {
		t.Fatalf("reply id = %q, want big-1", resp.ID)
	}
}
