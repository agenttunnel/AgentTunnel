package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// Overflow drops the oldest thread.event and leaves one resync hint for its
// client. RPC replies keep their place.
func TestOutboxDropsOldestEventAndKeepsReplies(t *testing.T) {
	o := newOutbox()
	if !o.push([]byte(`{"method":"thread.event","c":"a"}`), true, "a") {
		t.Fatal("first event should queue")
	}
	for i := 0; i < queueFrames-1; i++ {
		if !o.push([]byte(`{"id":"r"}`), false, "a") {
			t.Fatalf("reply %d should queue", i)
		}
	}
	// The queue is full. A new reply must make room by dropping the event.
	if !o.push([]byte(`{"id":"last"}`), false, "a") {
		t.Fatal("a reply must not be refused while an event can be dropped")
	}

	done := make(chan struct{})
	defer close(done)
	first, ok := o.pop(done)
	if !ok || !bytes.Contains(first, []byte(`"resync":true`)) || !bytes.Contains(first, []byte(`"c":"a"`)) {
		t.Fatalf("the hint for client a must come first, got %s", first)
	}
	// Closing lets pop report empty instead of waiting for more frames.
	o.close()
	replies := 0
	for {
		data, ok := o.pop(done)
		if !ok {
			break
		}
		if bytes.Contains(data, []byte("thread.event")) {
			t.Fatalf("the dropped event must not be delivered: %s", data)
		}
		replies++
	}
	if replies != queueFrames {
		t.Fatalf("every reply should survive, got %d of %d", replies, queueFrames)
	}
}

// When the queue holds only frames that may not be dropped, the push fails so
// the caller can close the socket instead of growing without bound.
func TestOutboxRefusesWhenNothingCanBeDropped(t *testing.T) {
	o := newOutbox()
	for i := 0; i < queueFrames; i++ {
		if !o.push([]byte(`{"id":"r"}`), false, "") {
			t.Fatalf("reply %d should queue", i)
		}
	}
	if o.push([]byte(`{"id":"one-too-many"}`), false, "") {
		t.Fatal("a full queue of replies must refuse, not grow")
	}
}

func TestBackoffIsJitteredWithinTheCeiling(t *testing.T) {
	for attempt := 0; attempt < 12; attempt++ {
		ceiling := time.Second << min(attempt, 6)
		if ceiling > maxBackoff {
			ceiling = maxBackoff
		}
		for i := 0; i < 50; i++ {
			d := backoff(attempt)
			if d <= 0 || d > ceiling+time.Millisecond {
				t.Fatalf("attempt %d: backoff %v outside (0, %v]", attempt, d, ceiling)
			}
		}
	}
}

// The relay is off by default: a fresh daemon dials nothing.
func TestDisabledRelayDialsNothing(t *testing.T) {
	var dials atomic.Int32
	relayHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/daemon" {
			dials.Add(1)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(relayHTTP.Close)

	store := settings.New(t.TempDir())
	c, err := New(Options{StateDir: t.TempDir(), Settings: store})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.Start(context.Background())
	st := c.Status()
	if st.Enabled || st.Connected || st.Instance != "" {
		t.Fatalf("a fresh daemon must report relay off and not minted, got %+v", st)
	}
	c.Close()
	if n := dials.Load(); n != 0 {
		t.Fatalf("a disabled relay must not dial, saw %d", n)
	}
}

func TestPairingNeedsALiveConnection(t *testing.T) {
	c, err := New(Options{StateDir: t.TempDir(), Settings: settings.New("")})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.PairStart(context.Background()); err == nil {
		t.Fatal("a pairing code cannot be minted without a relay socket")
	}
	if err := c.Unpair(context.Background(), "x", false); err == nil {
		t.Fatal("unpair needs the relay to revoke on")
	}
}

func TestEnableRefusesInsecureRemoteURL(t *testing.T) {
	store := settings.New("")
	c, err := New(Options{StateDir: t.TempDir(), Settings: store})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Enable("ws://relay.example.com"); err == nil {
		t.Fatal("plain ws to a public host must be refused")
	}
	if store.Relay().Enabled {
		t.Fatal("a refused URL must not enable the relay")
	}
}

// The identity holds the daemon secret, so it is written owner-only and reads
// back unchanged.
func TestIdentityIsSavedOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.json")
	id, err := newIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if len(id.Instance) != 32 || len(id.Secret) != 64 {
		t.Fatalf("instance %d chars, secret %d chars", len(id.Instance), len(id.Secret))
	}
	if err := saveIdentity(path, id); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode = %v, want 0600", info.Mode().Perm())
	}
	back, err := loadIdentity(path)
	if err != nil || back == nil {
		t.Fatalf("load: %v", err)
	}
	a, _ := json.Marshal(id)
	b, _ := json.Marshal(back)
	if !bytes.Equal(a, b) {
		t.Fatalf("round trip changed the identity:\n%s\n%s", a, b)
	}
}
