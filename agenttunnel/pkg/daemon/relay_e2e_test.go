package daemon

// The relay end to end: a real atrelay on 127.0.0.1:0, a daemon with a temp
// StateDir and the scripted harness, and a phone that pairs, attaches, runs a
// turn, and is unpaired from the desktop. Every wait is on a typed frame, a
// reply, or the client's connected signal. There are no sleeps.

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
	relaywire "github.com/agenttunnel/agenttunnel-go/pkg/relay"
)

// startAtrelay runs a relay on loopback and returns its ws base URL.
func startAtrelay(t *testing.T) string {
	t.Helper()
	srv, err := relaywire.New(relaywire.Options{})
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return "ws://" + ln.Addr().String()
}

// relayDial opens a socket to the relay's client endpoint. The token and code
// travel in frames, never in the URL.
func relayDial(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, base+"/v1/client", nil)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

// relayFrame reads the next relay frame as a generic map.
func relayFrame(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("relay read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("relay frame: %v", err)
	}
	return m
}

// relayFrameOfType skips frames until one with control type typ arrives.
func relayFrameOfType(t *testing.T, c *websocket.Conn, typ string) map[string]any {
	t.Helper()
	for {
		m := relayFrame(t, c)
		if m["t"] == typ {
			return m
		}
	}
}

func writeRelay(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("relay write: %v", err)
	}
}

// pairPhone redeems a code through the relay and returns the token and client id.
func pairPhone(t *testing.T, base, code string) (string, string) {
	t.Helper()
	c := relayDial(t, base)
	writeRelay(t, c, relaywire.Pair{T: "pair", Code: code, Name: "Pixel 9"})
	ack := relayFrameOfType(t, c, "ack")
	if ack["ok"] != true {
		t.Fatalf("pair with a live code must succeed, got %v", ack)
	}
	token, _ := ack["token"].(string)
	clientID, _ := ack["clientId"].(string)
	if token == "" || clientID == "" {
		t.Fatalf("pair ack missing token or client id: %v", ack)
	}
	return token, clientID
}

// attachPhone opens a socket, authenticates with the token, and attaches.
func attachPhone(t *testing.T, base, token string) *websocket.Conn {
	t.Helper()
	c := relayDial(t, base)
	writeRelay(t, c, relaywire.Hello{T: "hello", Role: "client", Token: token})
	if ack := relayFrameOfType(t, c, "ack"); ack["ok"] != true {
		t.Fatalf("hello with a valid token must succeed, got %v", ack)
	}
	writeRelay(t, c, relaywire.Attach{T: "attach"})
	ack := relayFrameOfType(t, c, "ack")
	if ack["ok"] != true || ack["daemon"] != "online" {
		t.Fatalf("attach should find the daemon online, got %v", ack)
	}
	return c
}

func TestRelayPairsAPhoneAndUnpairsIt(t *testing.T) {
	base := startAtrelay(t)
	d := newTestDaemon(t)
	desk := dialAt(t, wsBase(t, d), "secret")

	// Off by default: the desktop sees the relay disabled and no socket is open.
	var status struct {
		Enabled   bool `json:"enabled"`
		Connected bool `json:"connected"`
	}
	json.Unmarshal(call(t, desk, "s0", "relay.status", nil), &status)
	if status.Enabled || status.Connected {
		t.Fatalf("relay must start off and disconnected, got %+v", status)
	}

	call(t, desk, "s1", "relay.enable", map[string]any{"url": base})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Relay.WaitConnected(ctx); err != nil {
		t.Fatalf("daemon never connected to the relay: %v", err)
	}

	// Pair: the desktop mints a code, the phone redeems it once.
	var offer struct {
		Code string `json:"code"`
	}
	json.Unmarshal(call(t, desk, "p1", "relay.pair.start", nil), &offer)
	if offer.Code == "" {
		t.Fatal("pairing start returned no code")
	}
	token, clientID := pairPhone(t, base, offer.Code)

	// Single use: the same code must now fail as bad_code.
	again := relayDial(t, base)
	writeRelay(t, again, relaywire.Pair{T: "pair", Code: offer.Code})
	if ack := relayFrameOfType(t, again, "ack"); ack["error"] != relaywire.ErrBadCode {
		t.Fatalf("a used code must fail as bad_code, got %v", ack)
	}

	var devices struct {
		Devices []struct {
			ClientID string `json:"clientId"`
		} `json:"devices"`
	}
	json.Unmarshal(call(t, desk, "d1", "relay.devices.list", nil), &devices)
	if len(devices.Devices) != 1 || devices.Devices[0].ClientID != clientID {
		t.Fatalf("desktop should list the paired phone, got %+v", devices.Devices)
	}

	// A wrong token is refused outright.
	bad := relayDial(t, base)
	writeRelay(t, bad, relaywire.Hello{T: "hello", Role: "client", Token: "not-a-token"})
	if ack := relayFrameOfType(t, bad, "ack"); ack["error"] != relaywire.ErrBadToken {
		t.Fatalf("a wrong token must fail as bad_token, got %v", ack)
	}

	// The phone drives the daemon through the relay: a thread, a subscription,
	// and a turn whose events come back over the relay socket.
	phone := attachPhone(t, base, token)
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, phone, "1", "thread.create", map[string]any{"title": "phone"}), &created)
	call(t, phone, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	var threads []struct {
		ID contracts.ThreadID `json:"id"`
	}
	json.Unmarshal(call(t, phone, "3", "thread.list", nil), &threads)
	if !containsThread(threads, created.ThreadID) {
		t.Fatalf("phone should list its thread, got %+v", threads)
	}

	startTurn(t, phone, "4", string(created.ThreadID), "from the phone")
	waitForEvent(t, phone, contracts.EvtAgentOutput)
	waitForEvent(t, phone, contracts.EvtTaskDispatched)
	waitForReply(t, phone, "4")

	// Unpair from the desktop: the phone's socket closes with bye revoked, and
	// the token cannot reconnect.
	json.Unmarshal(call(t, desk, "u1", "relay.unpair", map[string]any{"clientId": clientID}), &devices)
	if len(devices.Devices) != 0 {
		t.Fatalf("unpair should empty the desktop's device list, got %+v", devices.Devices)
	}
	bye := relayFrameOfType(t, phone, "bye")
	if bye["reason"] != "revoked" {
		t.Fatalf("phone should be told it was revoked, got %v", bye)
	}
	if _, _, err := phone.Read(context.Background()); err == nil {
		t.Fatal("the revoked phone socket must close")
	}

	reconnect := relayDial(t, base)
	writeRelay(t, reconnect, relaywire.Hello{T: "hello", Role: "client", Token: token})
	if ack := relayFrameOfType(t, reconnect, "ack"); ack["error"] != relaywire.ErrBadToken {
		t.Fatalf("a revoked token must not reconnect, got %v", ack)
	}
}

func TestRelayDisableClosesTheDaemonSide(t *testing.T) {
	base := startAtrelay(t)
	d := newTestDaemon(t)
	desk := dialAt(t, wsBase(t, d), "secret")

	call(t, desk, "1", "relay.enable", map[string]any{"url": base})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Relay.WaitConnected(ctx); err != nil {
		t.Fatalf("daemon never connected: %v", err)
	}

	call(t, desk, "2", "relay.disable", nil)
	if err := d.Relay.WaitDisconnected(ctx); err != nil {
		t.Fatalf("disable never closed the relay socket: %v", err)
	}
	var status struct {
		Enabled   bool `json:"enabled"`
		Connected bool `json:"connected"`
	}
	json.Unmarshal(call(t, desk, "3", "relay.status", nil), &status)
	if status.Enabled || status.Connected {
		t.Fatalf("disable should persist the off state, got %+v", status)
	}
	if _, err := d.Relay.PairStart(ctx); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("a disabled daemon must not mint pairing codes, got %v", err)
	}
}

func TestRelayRefusesURLsWithSecrets(t *testing.T) {
	d := newTestDaemon(t)
	desk := dialAt(t, wsBase(t, d), "secret")
	for i, u := range []string{"ws://relay.example.com", "wss://relay.example.com?token=abc"} {
		if msg := rpcErrorText(t, desk, "bad"+string(rune('0'+i)), "relay.enable", map[string]any{"url": u}); msg == "" {
			t.Fatalf("relay.enable must refuse %q", u)
		}
	}
}

// rpcErrorText sends one request and returns its error message, or "" on success.
func rpcErrorText(t *testing.T, c *websocket.Conn, id, method string, params any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, _ := json.Marshal(transport.Request{ID: id, Method: method, Params: mustJSON(t, params)})
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read %s: %v", method, err)
		}
		var resp transport.Response
		if json.Unmarshal(msg, &resp) != nil || resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return resp.Error.Message
		}
		return ""
	}
}

func containsThread(list []struct {
	ID contracts.ThreadID `json:"id"`
}, id contracts.ThreadID) bool {
	for _, th := range list {
		if th.ID == id {
			return true
		}
	}
	return false
}
