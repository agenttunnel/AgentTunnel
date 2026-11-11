package transport

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRemote stands in for the WhatsApp bridge as the RPC surface drives it.
type fakeRemote struct {
	mu          sync.Mutex
	item        RemoteItem
	connects    int
	disconnects int
}

func (f *fakeRemote) Status() RemoteItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.item
}

func (f *fakeRemote) Connect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	f.item = RemoteItem{State: "pairing", Detail: "Scan the QR", QR: "2@qr"}
	return nil
}

func (f *fakeRemote) Disconnect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnects++
	f.item = RemoteItem{State: "off", Detail: "Not connected"}
	return nil
}

// The phone row is derived from the existing tunnel/relay state, never stored.
func TestPhoneRemoteItemDerivesFromRelayState(t *testing.T) {
	cases := []struct {
		name   string
		relay  *fakeRelay
		state  string
		detail string
	}{
		{
			name:   "tunnel listening",
			relay:  &fakeRelay{tunnel: TunnelStatus{Listening: true, Port: 7777, LANURL: "ws://192.168.1.5:7777"}},
			state:  "connected",
			detail: "ws://192.168.1.5:7777",
		},
		{
			name:   "relay connected",
			relay:  &fakeRelay{enabled: true, connected: true},
			state:  "connected",
			detail: "relay",
		},
		{
			name:   "relay waiting",
			relay:  &fakeRelay{enabled: true},
			state:  "connecting",
			detail: "waiting",
		},
		{
			name:   "off",
			relay:  &fakeRelay{},
			state:  "off",
			detail: "No phone",
		},
	}
	for _, tc := range cases {
		item := phoneRemoteItem(tc.relay)
		if item.ID != "phone" || item.Label != "Phone" {
			t.Errorf("%s: identity = %+v", tc.name, item)
		}
		if item.State != tc.state {
			t.Errorf("%s: state = %q, want %q", tc.name, item.State, tc.state)
		}
		if !strings.Contains(item.Detail, tc.detail) {
			t.Errorf("%s: detail = %q, want it to mention %q", tc.name, item.Detail, tc.detail)
		}
	}
}

// remote.status composes both rows, and the WhatsApp RPCs drive the bridge.
func TestRemoteRPCsDriveWhatsApp(t *testing.T) {
	ctx := context.Background()
	phone := &fakeRelay{tunnel: TunnelStatus{Listening: true, LANURL: "ws://192.168.1.5:7777"}}
	wa := &fakeRemote{item: RemoteItem{State: "off", Detail: "Not connected"}}
	disp := NewDispatcher(DispatcherDeps{Relay: phone, Remote: wa})

	notifications := make(chan RemoteStatus, 4)
	disp.SetRemoteNotifier(func(st RemoteStatus) { notifications <- st })

	res, err := disp.Handle(ctx, Request{ID: "1", Method: "remote.status"})
	if err != nil {
		t.Fatalf("remote.status: %v", err)
	}
	status := res.(RemoteStatus)
	if len(status.Items) != 2 || status.Items[0].ID != "phone" || status.Items[1].ID != "whatsapp" {
		t.Fatalf("status items = %+v, want phone then whatsapp", status.Items)
	}
	if status.Items[0].State != "connected" {
		t.Fatalf("phone item = %+v, want the listening tunnel reflected", status.Items[0])
	}
	if status.Items[1].State != "off" {
		t.Fatalf("whatsapp item = %+v, want off", status.Items[1])
	}

	res, err = disp.Handle(ctx, Request{ID: "2", Method: "remote.whatsapp.connect"})
	if err != nil {
		t.Fatalf("remote.whatsapp.connect: %v", err)
	}
	status = res.(RemoteStatus)
	if item := status.Items[1]; item.State != "pairing" || item.QR != "2@qr" || item.ID != "whatsapp" {
		t.Fatalf("whatsapp item after connect = %+v", item)
	}
	notified := awaitRemoteStatus(t, notifications, func(st RemoteStatus) bool {
		return st.Items[1].State == "pairing" && st.Items[1].QR == "2@qr"
	})
	if !strings.Contains(mustMarshal(t, notified), "phone") {
		t.Fatalf("broadcast should carry the whole section, got %s", mustMarshal(t, notified))
	}

	res, err = disp.Handle(ctx, Request{ID: "3", Method: "remote.whatsapp.disconnect"})
	if err != nil {
		t.Fatalf("remote.whatsapp.disconnect: %v", err)
	}
	if item := res.(RemoteStatus).Items[1]; item.State != "off" || item.QR != "" {
		t.Fatalf("whatsapp item after disconnect = %+v", item)
	}
	awaitRemoteStatus(t, notifications, func(st RemoteStatus) bool {
		return st.Items[1].State == "off"
	})

	wa.mu.Lock()
	defer wa.mu.Unlock()
	if wa.connects != 1 || wa.disconnects != 1 {
		t.Fatalf("bridge calls = connect %d, disconnect %d", wa.connects, wa.disconnects)
	}
}

// Starting the phone tunnel changes the derived phone row, so clients hear it.
func TestRelayTunnelBroadcastsRemoteStatus(t *testing.T) {
	ctx := context.Background()
	disp := NewDispatcher(DispatcherDeps{Relay: &fakeRelay{}, Remote: &fakeRemote{item: RemoteItem{State: "off"}}})
	notifications := make(chan RemoteStatus, 4)
	disp.SetRemoteNotifier(func(st RemoteStatus) { notifications <- st })

	if _, err := disp.Handle(ctx, Request{ID: "1", Method: "relay.tunnel.start"}); err != nil {
		t.Fatalf("relay.tunnel.start: %v", err)
	}
	st := awaitRemoteStatus(t, notifications, func(st RemoteStatus) bool {
		return st.Items[0].State == "connected"
	})
	if !strings.Contains(st.Items[0].Detail, "192.168.1.5") {
		t.Fatalf("phone detail = %q, want the tunnel address", st.Items[0].Detail)
	}
}

// A daemon without a bridge still answers remote.status and refuses connect.
func TestRemoteRPCsWithoutABridge(t *testing.T) {
	ctx := context.Background()
	disp := NewDispatcher(DispatcherDeps{})

	res, err := disp.Handle(ctx, Request{ID: "1", Method: "remote.status"})
	if err != nil {
		t.Fatalf("remote.status: %v", err)
	}
	status := res.(RemoteStatus)
	if len(status.Items) != 2 || status.Items[1].ID != "whatsapp" || status.Items[1].State != "off" {
		t.Fatalf("status = %+v, want an off whatsapp row", status.Items)
	}
	if !status.Items[1].Unavailable {
		t.Fatalf("whatsapp row = %+v, want it marked unavailable without a bridge", status.Items[1])
	}
	if _, err := disp.Handle(ctx, Request{ID: "2", Method: "remote.whatsapp.connect"}); err == nil {
		t.Fatal("connect without a bridge must fail")
	}
	if _, err := disp.Handle(ctx, Request{ID: "3", Method: "remote.whatsapp.disconnect"}); err == nil {
		t.Fatal("disconnect without a bridge must fail")
	}
}

// A relayed phone cannot re-pair WhatsApp, and its status view never carries
// another surface's pairing secret.
func TestSessionRefusesWhatsAppPairing(t *testing.T) {
	wa := &fakeRemote{item: RemoteItem{State: "pairing", QR: "2@qr"}}
	disp := NewDispatcher(DispatcherDeps{Remote: wa})
	srv := NewServer(disp, nil, nil, "", nil)

	replies := make(chan Response, 4)
	sess := srv.NewSession(func(r Response) { replies <- r })
	t.Cleanup(sess.Close)

	for _, frame := range []string{
		`{"id":"w1","method":"remote.whatsapp.connect"}`,
		`{"id":"w2","method":"remote.whatsapp.disconnect"}`,
	} {
		sess.Handle([]byte(frame))
		select {
		case r := <-replies:
			if r.Error == nil || r.Error.Code != codeRelayRestricted {
				t.Fatalf("%s: want restricted error, got %+v", frame, r)
			}
		default:
			t.Fatalf("%s: refusal must reply synchronously", frame)
		}
	}

	wa.mu.Lock()
	connects, disconnects := wa.connects, wa.disconnects
	wa.mu.Unlock()
	if connects != 0 || disconnects != 0 {
		t.Fatalf("bridge was driven through a session: connects %d, disconnects %d", connects, disconnects)
	}

	sess.Handle([]byte(`{"id":"w3","method":"remote.status"}`))
	select {
	case r := <-replies:
		if r.Error != nil {
			t.Fatalf("remote.status over a session should answer: %+v", r.Error)
		}
		st, ok := r.Result.(RemoteStatus)
		if !ok {
			t.Fatalf("remote.status result = %T, want RemoteStatus", r.Result)
		}
		if st.Items[1].QR != "" {
			t.Fatalf("relayed status leaked a pairing QR: %+v", st.Items[1])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remote.status never replied")
	}

	// A local client is the one that must scan, so it keeps the QR.
	res, err := disp.Handle(context.Background(), Request{ID: "w4", Method: "remote.status"})
	if err != nil {
		t.Fatalf("local remote.status: %v", err)
	}
	if got := res.(RemoteStatus).Items[1].QR; got != "2@qr" {
		t.Fatalf("local status QR = %q, want the pairing secret", got)
	}
}

func awaitRemoteStatus(t *testing.T, ch <-chan RemoteStatus, match func(RemoteStatus) bool) RemoteStatus {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case st := <-ch:
			if match(st) {
				return st
			}
		case <-deadline:
			t.Fatal("no matching remote.status.changed notification")
		}
	}
}
