// Package daemonlock serializes the "is a daemon running? if not, start one"
// decision across processes that share a state directory. `at` holds the lock
// across connect -> spawn -> wait, and atd takes it before binding, so two
// simultaneous first runs cannot both start a daemon and orphan one of them.
package daemonlock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

// FileName is the lock file inside the daemon's state directory.
const FileName = "daemon.lock"

// staleAfter bounds how long a lock may claim to be held regardless of its
// recorded pid. A crashed holder's pid is usually dead already; this covers pid
// reuse and platforms where liveness cannot be probed.
const staleAfter = 10 * time.Minute

// writeGrace keeps the create-pidfile window from looking stale: a lock file
// briefly exists before its pid is written, and a competing TryAcquire must
// wait for that write instead of deleting a live holder's lock.
const writeGrace = 2 * time.Second

// ErrHeld reports that a live process holds the lock.
var ErrHeld = errors.New("daemon lock is held by another process")

// Lock is a held daemon lock. Release it when the daemon is known to be up (or
// the startup attempt is over).
type Lock struct {
	path string
	pid  int
}

// Acquire polls TryAcquire until the lock is held or ctx ends. Callers pass a
// short poll interval; a released lock is noticed on the next tick.
func Acquire(ctx context.Context, stateDir string, poll time.Duration) (*Lock, error) {
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	for {
		lock, err := TryAcquire(stateDir)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, ErrHeld) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for %s: %w", FileName, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// TryAcquire attempts to take the lock once. A stale lock — a dead or
// unparseable pid, or one older than staleAfter — is removed and replaced. A
// live holder reports ErrHeld.
func TryAcquire(stateDir string) (*Lock, error) {
	path := filepath.Join(stateDir, FileName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, werr := fmt.Fprintf(f, "%d\n", os.Getpid()); werr != nil {
				f.Close()
				_ = os.Remove(path)
				return nil, werr
			}
			if cerr := f.Close(); cerr != nil {
				_ = os.Remove(path)
				return nil, cerr
			}
			return &Lock{path: path, pid: os.Getpid()}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		stale, serr := isStale(path)
		if serr != nil {
			return nil, serr
		}
		if !stale {
			return nil, ErrHeld
		}
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return nil, rerr
		}
	}
	return nil, ErrHeld
}

// isStale reports whether an existing lock may be replaced.
func isStale(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	age := time.Duration(0)
	if info, statErr := os.Stat(path); statErr == nil {
		age = time.Since(info.ModTime())
	}
	if age > staleAfter {
		return true, nil
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
	if perr != nil || pid <= 0 {
		// A holder writes its pid right after creating the file; only treat an
		// unreadable record as stale once that window has passed.
		return age > writeGrace, nil
	}
	return !processAlive(pid), nil
}

// Release removes the lock file if it still records this process. A lock that
// was replaced after going stale belongs to someone else and is left alone.
func (l *Lock) Release() {
	if l == nil || l.path == "" {
		return
	}
	if data, err := os.ReadFile(l.path); err == nil {
		// Only our own pid proves the file is still ours; anything else (a
		// replaced or corrupted lock) belongs to someone else.
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr != nil || pid != l.pid {
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return
	}
	_ = os.Remove(l.path)
}

// Live reports whether the daemon recorded in <state>/daemon.json answers an
// RPC. A missing, unreadable, or unreachable record is not live.
func Live(ctx context.Context, stateDir string, timeout time.Duration) bool {
	info, err := discovery.Read(stateDir)
	if err != nil {
		return false
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ws, _, err := websocket.Dial(pctx, info.WSURL, &websocket.DialOptions{
		HTTPClient: &http.Client{Timeout: timeout},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + info.Token}},
	})
	if err != nil {
		return false
	}
	defer ws.CloseNow()
	req, err := json.Marshal(map[string]any{"id": "daemon-lock-probe", "method": "project.list"})
	if err != nil {
		return false
	}
	if err := ws.Write(pctx, websocket.MessageText, req); err != nil {
		return false
	}
	for {
		_, data, err := ws.Read(pctx)
		if err != nil {
			return false
		}
		var resp struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &resp) != nil || resp.ID != "daemon-lock-probe" {
			continue
		}
		return resp.Error == nil
	}
}
