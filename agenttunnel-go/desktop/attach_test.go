package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

// fakeDaemon answers one project.list call on an accepted WebSocket and
// reports the Authorization header it saw, so the test can prove the token
// never rides in the query string.
func fakeDaemon(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.RawQuery != "" {
			t.Errorf("unexpected query string %q (the token must be a header)", r.URL.RawQuery)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &req)
		resp, _ := json.Marshal(map[string]any{"id": req.ID, "result": []any{}})
		_ = conn.Write(ctx, websocket.MessageText, resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func TestFindDaemonAttachesToReachableDaemon(t *testing.T) {
	srv, auth := fakeDaemon(t)
	state := t.TempDir()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	if err := discovery.Write(state, discovery.Info{WSURL: wsURL, Token: "tok"}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}

	target, ok := findDaemon(state)
	if !ok {
		t.Fatal("findDaemon did not attach to a reachable daemon")
	}
	if *auth != "Bearer tok" {
		t.Fatalf("Authorization = %q, want bearer token", *auth)
	}
	if target.wsURL != wsURL || target.token != "tok" {
		t.Fatalf("target = %+v, want wsURL %q token tok", target, wsURL)
	}
	if target.host != strings.TrimPrefix(srv.URL, "http://") {
		t.Fatalf("host = %q", target.host)
	}

	// Attaching must not disturb the owner's discovery file.
	if _, err := discovery.Read(state); err != nil {
		t.Fatalf("discovery file was removed: %v", err)
	}
}

// A failed probe must leave the file alone: a large environment can outlast the
// 2s probe, and deleting a live daemon's file strands `at` and invites a second
// daemon on the same state dir. Only a provably dead recorded pid may be
// cleaned up.
func TestFindDaemonKeepsUnreachableDiscovery(t *testing.T) {
	state := t.TempDir()
	// Port 1 is reserved and refuses connections; a short probe fails fast.
	// PID 0 is "unknown", so nothing proves the owner is dead.
	if err := discovery.Write(state, discovery.Info{WSURL: "ws://127.0.0.1:1/ws", Token: "tok"}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}

	if _, ok := findDaemon(state); ok {
		t.Fatal("findDaemon attached to an unreachable daemon")
	}
	if _, err := os.Stat(discovery.Path(state)); err != nil {
		t.Fatalf("unreachable discovery file was removed: %v", err)
	}
}

func TestFindDaemonRemovesProvablyDeadDiscovery(t *testing.T) {
	state := t.TempDir()
	// A pid far above any platform's pid_max cannot be running.
	if err := discovery.Write(state, discovery.Info{WSURL: "ws://127.0.0.1:1/ws", Token: "tok", PID: 1 << 30}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}

	if _, ok := findDaemon(state); ok {
		t.Fatal("findDaemon attached to a dead daemon")
	}
	if _, err := os.Stat(discovery.Path(state)); !os.IsNotExist(err) {
		t.Fatalf("dead discovery file still exists (err=%v)", err)
	}
}

// An unreadable record has no pid to check, so the probe leaves it for the next
// writer to replace atomically.
func TestFindDaemonKeepsMalformedDiscovery(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(discovery.Path(state), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write malformed discovery: %v", err)
	}
	if _, ok := findDaemon(state); ok {
		t.Fatal("findDaemon attached without a valid discovery file")
	}
	if _, err := os.Stat(discovery.Path(state)); err != nil {
		t.Fatalf("malformed discovery file was removed: %v", err)
	}
}

func TestFindDaemonWithoutDiscovery(t *testing.T) {
	if _, ok := findDaemon(t.TempDir()); ok {
		t.Fatal("findDaemon attached without a discovery file")
	}
}

func TestPrefsRoundTrip(t *testing.T) {
	state := t.TempDir()
	if got := loadPrefs(state); got.KeepRunning {
		t.Fatal("missing prefs did not default to keepRunning=false")
	}
	if err := savePrefs(state, prefs{KeepRunning: true}); err != nil {
		t.Fatalf("savePrefs: %v", err)
	}
	if !loadPrefs(state).KeepRunning {
		t.Fatal("keepRunning preference did not round-trip")
	}
	info, err := os.Stat(filepath.Join(state, "desktop-prefs.json"))
	if err != nil {
		t.Fatalf("stat prefs: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("prefs mode = %o, want 600", perm)
	}
}
