package transport

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// fakeRelay records the calls the relay RPCs make.
type fakeRelay struct {
	mu        sync.Mutex
	enabled   bool
	connected bool
	url       string
	unpairAll bool
	unpairID  string
	tunnel    TunnelStatus
}

func (f *fakeRelay) Status() RelayStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return RelayStatus{Enabled: f.enabled, URL: f.url, Connected: f.connected}
}

func (f *fakeRelay) Enable(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled, f.url = true, url
	return nil
}

func (f *fakeRelay) Disable() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = false
	return nil
}

func (f *fakeRelay) PairStart(context.Context) (PairOffer, error) {
	return PairOffer{Code: "ABCDE-FGHJK", Expires: time.Unix(0, 0).UTC()}, nil
}

func (f *fakeRelay) Devices() []RelayDevice {
	return []RelayDevice{{ClientID: "c1", Name: "Pixel 9"}}
}

func (f *fakeRelay) Unpair(_ context.Context, clientID string, all bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unpairID, f.unpairAll = clientID, all
	return nil
}

func (f *fakeRelay) StartTunnel(context.Context) (TunnelStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tunnel = TunnelStatus{Listening: true, Port: 7777, LANURL: "ws://192.168.1.5:7777"}
	return f.tunnel, nil
}

func (f *fakeRelay) StopTunnel() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tunnel = TunnelStatus{}
	return nil
}

func (f *fakeRelay) TunnelStatus() TunnelStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tunnel
}

func relayDispatcher(t *testing.T, rc RelayControl) *Dispatcher {
	t.Helper()
	disp := NewDispatcher(DispatcherDeps{Relay: rc})
	return disp
}

func TestRelayRPCsDriveTheClient(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRelay{}
	disp := relayDispatcher(t, fr)

	res, err := disp.Handle(ctx, Request{ID: "1", Method: "relay.enable", Params: json.RawMessage(`{"url":"ws://127.0.0.1:7777"}`)})
	if err != nil {
		t.Fatalf("relay.enable: %v", err)
	}
	if st, ok := res.(RelayStatus); !ok || !st.Enabled || st.URL != "ws://127.0.0.1:7777" {
		t.Fatalf("enable should return the new status, got %#v", res)
	}

	if _, err := disp.Handle(ctx, Request{ID: "2", Method: "relay.disable"}); err != nil {
		t.Fatalf("relay.disable: %v", err)
	}
	if fr.Status().Enabled {
		t.Fatal("disable should turn the relay off")
	}

	offer, err := disp.Handle(ctx, Request{ID: "3", Method: "relay.pair.start"})
	if err != nil {
		t.Fatalf("relay.pair.start: %v", err)
	}
	if po, ok := offer.(PairOffer); !ok || po.Code != "ABCDE-FGHJK" {
		t.Fatalf("pair.start should return the code, got %#v", offer)
	}

	list, err := disp.Handle(ctx, Request{ID: "4", Method: "relay.devices.list"})
	if err != nil {
		t.Fatalf("relay.devices.list: %v", err)
	}
	if !strings.Contains(mustMarshal(t, list), "Pixel 9") {
		t.Fatalf("devices.list should name the paired phone, got %s", mustMarshal(t, list))
	}

	if _, err := disp.Handle(ctx, Request{ID: "5", Method: "relay.unpair", Params: json.RawMessage(`{"all":true}`)}); err != nil {
		t.Fatalf("relay.unpair all: %v", err)
	}
	if !fr.unpairAll {
		t.Fatal("unpair with all=true should reach the relay client")
	}
}

// The tunnel RPCs start, report, and stop the in-app relay through RelayControl.
func TestRelayRPCsDriveTheTunnel(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRelay{}
	disp := relayDispatcher(t, fr)

	res, err := disp.Handle(ctx, Request{ID: "1", Method: "relay.tunnel.start"})
	if err != nil {
		t.Fatalf("relay.tunnel.start: %v", err)
	}
	if st := res.(TunnelStatus); !st.Listening || st.LANURL != "ws://192.168.1.5:7777" {
		t.Fatalf("start must report the listening tunnel, got %+v", st)
	}
	res, err = disp.Handle(ctx, Request{ID: "2", Method: "relay.tunnel.status"})
	if err != nil {
		t.Fatalf("relay.tunnel.status: %v", err)
	}
	if st := res.(TunnelStatus); !st.Listening {
		t.Fatalf("status must show the tunnel listening, got %+v", st)
	}
	res, err = disp.Handle(ctx, Request{ID: "3", Method: "relay.tunnel.stop"})
	if err != nil {
		t.Fatalf("relay.tunnel.stop: %v", err)
	}
	if st := res.(TunnelStatus); st.Listening {
		t.Fatalf("stop must report the tunnel closed, got %+v", st)
	}
}

func TestRelayUnpairNeedsATarget(t *testing.T) {
	disp := relayDispatcher(t, &fakeRelay{})
	if _, err := disp.Handle(context.Background(), Request{ID: "1", Method: "relay.unpair", Params: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("unpair with neither clientId nor all must be refused, not treated as unpair-all")
	}
}

func TestRelayRPCsWithoutARelayAnswerCleanly(t *testing.T) {
	ctx := context.Background()
	disp := NewDispatcher(DispatcherDeps{})

	status, err := disp.Handle(ctx, Request{ID: "1", Method: "relay.status"})
	if err != nil {
		t.Fatalf("status should answer even without a relay: %v", err)
	}
	if st, ok := status.(RelayStatus); !ok || st.Enabled || st.Connected {
		t.Fatalf("a daemon without a relay reports off, got %#v", status)
	}
	if _, err := disp.Handle(ctx, Request{ID: "2", Method: "relay.enable", Params: json.RawMessage(`{"url":"wss://x"}`)}); err == nil {
		t.Fatal("enable without a relay must fail")
	}
}

func TestSessionServesRPCThroughTheSink(t *testing.T) {
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(context.Background())
	t.Cleanup(eng.Close)
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Relay: &fakeRelay{}})
	srv := NewServer(disp, eng, nil, "", toolbox.New())

	replies := make(chan Response, 4)
	sess := srv.NewSession(func(r Response) { replies <- r })
	t.Cleanup(sess.Close)

	sess.Handle([]byte(`{"id":"s1","method":"thread.list"}`))
	select {
	case r := <-replies:
		if r.ID != "s1" || r.Error != nil {
			t.Fatalf("session reply = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session never replied")
	}
}

// A relayed session must refuse every relay.* call before it reaches the
// dispatcher, and the refusal is synchronous, so the reply is already queued.
func TestSessionRefusesRelayMethods(t *testing.T) {
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(context.Background())
	t.Cleanup(eng.Close)
	rc := &fakeRelay{}
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Relay: rc})
	srv := NewServer(disp, eng, nil, "", toolbox.New())

	replies := make(chan Response, 1)
	sess := srv.NewSession(func(r Response) { replies <- r })
	t.Cleanup(sess.Close)

	methods := []string{
		`{"id":"r1","method":"relay.status"}`,
		`{"id":"r2","method":"relay.enable","params":{"url":"wss://evil.example"}}`,
		`{"id":"r3","method":"relay.pair.start"}`,
		`{"id":"r4","method":"relay.devices.list"}`,
		`{"id":"r5","method":"relay.unpair","params":{"all":true}}`,
		`{"id":"r6","method":"relay.disable"}`,
	}
	for _, frame := range methods {
		sess.Handle([]byte(frame))
		select {
		case r := <-replies:
			if r.Error == nil || r.Error.Code != codeRelayRestricted {
				t.Fatalf("%s: want restricted error, got %+v", frame, r)
			}
			if r.Result != nil {
				t.Fatalf("%s: refused call must not return a result", frame)
			}
		default:
			t.Fatalf("%s: refusal must reply synchronously", frame)
		}
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.enabled || rc.url != "" || rc.unpairAll || rc.unpairID != "" {
		t.Fatalf("relay was touched through a session: %+v", rc)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
