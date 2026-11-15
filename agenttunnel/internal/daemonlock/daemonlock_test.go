package daemonlock

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

func lockPath(dir string) string { return filepath.Join(dir, FileName) }

func TestAcquireAndRelease(t *testing.T) {
	state := t.TempDir()
	lock, err := TryAcquire(state)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	data, err := os.ReadFile(lockPath(state))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock pid = %q, want %d", got, os.Getpid())
	}
	if _, err := TryAcquire(state); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire = %v, want ErrHeld", err)
	}

	lock.Release()
	if _, err := os.Stat(lockPath(state)); !os.IsNotExist(err) {
		t.Fatalf("release left the lock file (err=%v)", err)
	}
	again, err := TryAcquire(state)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	again.Release()
}

// A waiting process takes the lock as soon as the holder releases it.
func TestAcquireWaitsForRelease(t *testing.T) {
	state := t.TempDir()
	first, err := TryAcquire(state)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waiting := make(chan *Lock, 1)
	waitErr := make(chan error, 1)
	go func() {
		lock, err := Acquire(ctx, state, 5*time.Millisecond)
		if err != nil {
			waitErr <- err
			return
		}
		waiting <- lock
	}()

	// While the first lock is held, the waiter must not proceed.
	select {
	case <-waiting:
		t.Fatal("second acquire succeeded while the first was held")
	case err := <-waitErr:
		t.Fatalf("second acquire failed early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	first.Release()
	select {
	case lock := <-waiting:
		lock.Release()
	case err := <-waitErr:
		t.Fatalf("waiter failed after release: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not acquire after release")
	}
}

func TestStaleLockIsReplaced(t *testing.T) {
	// A pid far above any platform's pid_max is provably dead.
	deadPID := strconv.Itoa(1 << 30)
	for _, content := range []string{deadPID + "\n", "not-a-pid\n"} {
		state := t.TempDir()
		if err := os.WriteFile(lockPath(state), []byte(content), 0o600); err != nil {
			t.Fatalf("write stale lock: %v", err)
		}
		// An unparseable record only proves staleness after the write window;
		// a fresh one could be a holder that has not written its pid yet.
		if content == "not-a-pid\n" {
			old := time.Now().Add(-writeGrace - time.Second)
			if err := os.Chtimes(lockPath(state), old, old); err != nil {
				t.Fatalf("age lock: %v", err)
			}
		}
		lock, err := TryAcquire(state)
		if err != nil {
			t.Fatalf("acquire over stale %q: %v", content, err)
		}
		data, _ := os.ReadFile(lockPath(state))
		if got := strings.TrimSpace(string(data)); got != strconv.Itoa(os.Getpid()) {
			t.Fatalf("replaced lock pid = %q, want %d", got, os.Getpid())
		}
		lock.Release()
	}
}

// A lock file created but not yet carrying its pid is a live acquisition in
// progress, not a stale lock: replacing it would let two processes both hold it.
func TestFreshEmptyLockIsHeld(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(lockPath(state), nil, 0o600); err != nil {
		t.Fatalf("write empty lock: %v", err)
	}
	if _, err := TryAcquire(state); !errors.Is(err, ErrHeld) {
		t.Fatalf("acquire over a fresh empty lock = %v, want ErrHeld", err)
	}
}

// A lock older than staleAfter is replaced even if the recorded pid is alive:
// that covers pid reuse on platforms where liveness cannot be probed.
func TestOldLockIsReplaced(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(lockPath(state), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	old := time.Now().Add(-staleAfter - time.Minute)
	if err := os.Chtimes(lockPath(state), old, old); err != nil {
		t.Fatalf("age lock: %v", err)
	}
	lock, err := TryAcquire(state)
	if err != nil {
		t.Fatalf("acquire over old lock: %v", err)
	}
	lock.Release()
}

// Releasing a lock that was replaced must not delete the new owner's file.
func TestReleaseLeavesReplacedLock(t *testing.T) {
	state := t.TempDir()
	lock, err := TryAcquire(state)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := os.WriteFile(lockPath(state), []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("replace lock: %v", err)
	}
	lock.Release()
	if _, err := os.Stat(lockPath(state)); err != nil {
		t.Fatalf("release removed a lock it no longer owned: %v", err)
	}
}

func TestLive(t *testing.T) {
	state := t.TempDir()
	if Live(context.Background(), state, time.Second) {
		t.Fatal("Live reported a daemon without a discovery file")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &req)
		resp, _ := json.Marshal(map[string]any{"id": req.ID, "result": []any{}})
		_ = conn.Write(ctx, websocket.MessageText, resp)
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	if err := discovery.Write(state, discovery.Info{WSURL: wsURL, Token: "tok"}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	if !Live(context.Background(), state, time.Second) {
		t.Fatal("Live did not see the answering daemon")
	}

	// The recorded endpoint no longer answers: a stale file is not live.
	dead := t.TempDir()
	if err := discovery.Write(dead, discovery.Info{WSURL: "ws://127.0.0.1:1/ws", Token: "tok"}); err != nil {
		t.Fatalf("write dead discovery: %v", err)
	}
	if Live(context.Background(), dead, 500*time.Millisecond) {
		t.Fatal("Live reported an unreachable daemon")
	}
}
