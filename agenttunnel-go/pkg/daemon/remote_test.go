package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// The Remote section answers over the daemon's real socket, and a failed
// WhatsApp connect still broadcasts the resulting error state to the client.
func TestDaemonRemoteSectionAndBroadcast(t *testing.T) {
	// The shipped adapter reads its credentials from the environment; keep the
	// test hermetic so connect fails for the documented reason.
	t.Setenv("AT_WHATSAPP_TOKEN", "")
	t.Setenv("AT_WHATSAPP_PHONE_NUMBER_ID", "")
	t.Setenv("AT_WHATSAPP_TO", "")

	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var status transport.RemoteStatus
	if err := json.Unmarshal(call(t, c, "1", "remote.status", nil), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(status.Items) != 2 || status.Items[0].ID != "phone" || status.Items[1].ID != "whatsapp" {
		t.Fatalf("items = %+v, want phone then whatsapp", status.Items)
	}
	if status.Items[1].State != "off" {
		t.Fatalf("whatsapp should start off, got %+v", status.Items[1])
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := json.Marshal(transport.Request{ID: "2", Method: "remote.whatsapp.connect"})
	if err := c.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatalf("write connect: %v", err)
	}

	var notified, refused bool
	for !notified || !refused {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read (notified=%v refused=%v): %v", notified, refused, err)
		}
		var resp transport.Response
		if json.Unmarshal(msg, &resp) != nil {
			continue
		}
		if resp.Method == "remote.status.changed" {
			var pushed transport.RemoteStatus
			if json.Unmarshal(mustJSON(t, resp.Params), &pushed) != nil {
				continue
			}
			if len(pushed.Items) == 2 && pushed.Items[1].ID == "whatsapp" && pushed.Items[1].State == "error" {
				notified = true
			}
		}
		if resp.ID == "2" && resp.Error != nil {
			if !strings.Contains(resp.Error.Message, "not configured") {
				t.Fatalf("connect error = %q, want the configuration message", resp.Error.Message)
			}
			refused = true
		}
	}
}
