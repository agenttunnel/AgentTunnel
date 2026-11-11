package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// fakeRemoteAdapter links immediately, so the daemon test never reaches
// WhatsApp. The bridge is the behavior under test, not the client.
type fakeRemoteAdapter struct {
	connects int
}

func (f *fakeRemoteAdapter) Connect(_ context.Context, onQR func(string), onLinked func(string), _ func(error)) error {
	f.connects++
	onQR("2@qr-payload")
	onLinked("Linked device · +15551234567")
	return nil
}

func (f *fakeRemoteAdapter) Disconnect() error                  { return nil }
func (f *fakeRemoteAdapter) Send(context.Context, string) error { return nil }

// failingRemoteAdapter refuses to connect, so the daemon must surface the error
// to the caller and broadcast the error row.
type failingRemoteAdapter struct{ err error }

func (f *failingRemoteAdapter) Connect(context.Context, func(string), func(string), func(error)) error {
	return f.err
}

func (f *failingRemoteAdapter) Disconnect() error                  { return nil }
func (f *failingRemoteAdapter) Send(context.Context, string) error { return nil }

// The Remote section answers over the daemon's real socket, the linked-device
// adapter is the default (not an unavailable row), and a pairing transition
// reaches the client as a push.
func TestDaemonRemoteSectionAndBroadcast(t *testing.T) {
	d := newTestDaemonWith(t, Options{RemoteAdapter: &fakeRemoteAdapter{}})
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
	if status.Items[1].Unavailable {
		t.Fatalf("whatsapp should be connectable through the linked adapter, got %+v", status.Items[1])
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := json.Marshal(transport.Request{ID: "2", Method: "remote.whatsapp.connect"})
	if err := c.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatalf("write connect: %v", err)
	}

	var notified, replied bool
	for !notified || !replied {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read (notified=%v replied=%v): %v", notified, replied, err)
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
			if len(pushed.Items) == 2 && pushed.Items[1].ID == "whatsapp" && pushed.Items[1].State == "connected" {
				notified = true
			}
		}
		if resp.ID == "2" {
			if resp.Error != nil {
				t.Fatalf("connect error: %s", resp.Error.Message)
			}
			var result transport.RemoteStatus
			if err := json.Unmarshal(mustJSON(t, resp.Result), &result); err != nil {
				t.Fatalf("decode connect result: %v", err)
			}
			if result.Items[1].State != "connected" || !strings.Contains(result.Items[1].Detail, "+15551234567") {
				t.Fatalf("connect result = %+v, want the linked device", result.Items[1])
			}
			replied = true
		}
	}
}

// A failed connect must reply with the error and push the error row, so a
// window that missed the reply still shows what went wrong.
func TestDaemonBroadcastsWhatsAppConnectFailure(t *testing.T) {
	boom := errors.New("pairing refused")
	d := newTestDaemonWith(t, Options{RemoteAdapter: &failingRemoteAdapter{err: boom}})
	c, _ := dialWS(t, d)
	// A round-trip proves this socket is registered, so the failure pushes
	// cannot miss it.
	call(t, c, "0", "remote.status", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := json.Marshal(transport.Request{ID: "1", Method: "remote.whatsapp.connect"})
	if err := c.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatalf("write connect: %v", err)
	}

	var notified, replied bool
	for !notified || !replied {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read (notified=%v replied=%v): %v", notified, replied, err)
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
			if len(pushed.Items) == 2 && pushed.Items[1].ID == "whatsapp" &&
				pushed.Items[1].State == "error" && strings.Contains(pushed.Items[1].Detail, boom.Error()) {
				notified = true
			}
		}
		if resp.ID == "1" {
			if resp.Error == nil || !strings.Contains(resp.Error.Message, boom.Error()) {
				t.Fatalf("connect reply = %+v, want the adapter error", resp)
			}
			replied = true
		}
	}
}

// The relay connects and drops on its own; the follower must push both
// transitions without any RPC broadcasting them. Enabling and disabling the
// client directly keeps those pushes attributable to the follower.
func TestDaemonBroadcastsBackgroundRelayTransitions(t *testing.T) {
	base := startAtrelay(t)
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)
	// A round-trip proves this socket is registered, so pushes cannot miss it.
	call(t, c, "0", "remote.status", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := d.Relay.Enable(base); err != nil {
		t.Fatalf("enable relay: %v", err)
	}
	if err := d.Relay.WaitConnected(ctx); err != nil {
		t.Fatalf("relay never connected: %v", err)
	}
	awaitRemotePhone(t, c, "connected")

	if err := d.Relay.Disable(); err != nil {
		t.Fatalf("disable relay: %v", err)
	}
	if err := d.Relay.WaitDisconnected(ctx); err != nil {
		t.Fatalf("relay never dropped: %v", err)
	}
	awaitRemotePhone(t, c, "off")
}

// awaitRemotePhone reads pushes until the phone row reaches want.
func awaitRemotePhone(t *testing.T, c *websocket.Conn, want string) transport.RemoteStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read remote push (want %s): %v", want, err)
		}
		var resp transport.Response
		if json.Unmarshal(msg, &resp) != nil || resp.Method != "remote.status.changed" {
			continue
		}
		var st transport.RemoteStatus
		if json.Unmarshal(mustJSON(t, resp.Params), &st) != nil {
			continue
		}
		if len(st.Items) == 2 && st.Items[0].ID == "phone" && st.Items[0].State == want {
			return st
		}
	}
}
