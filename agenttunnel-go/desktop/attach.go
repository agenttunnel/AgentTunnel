package main

// Attach handling: a window may boot against a daemon someone else owns (an
// `at` autostart, or a desktop left running in the background) instead of
// starting its own. The discovery file is the handoff point; the window only
// ever reads it, and only removes it when the recorded daemon does not answer.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

// attachTimeout bounds the reachability probe. A healthy daemon answers
// project.list immediately; anything slower is treated as a stale file rather
// than making the window wait.
const attachTimeout = 2 * time.Second

// daemonTarget is a running daemon this window attaches to.
type daemonTarget struct {
	wsURL   string
	httpURL string
	token   string
	host    string
}

// findDaemon reads <state>/daemon.json and confirms the daemon answers. A
// missing or unreachable file means "no daemon to attach to", but it is only
// removed when the recorded process is provably dead: project.list walks every
// thread, so a large environment can legitimately outlast the probe, and
// deleting a live daemon's file would strand `at` and let this window boot a
// second daemon on the same state dir.
func findDaemon(stateDir string) (daemonTarget, bool) {
	info, err := discovery.Read(stateDir)
	if err != nil {
		return daemonTarget{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), attachTimeout)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, info.WSURL, &websocket.DialOptions{
		HTTPClient: &http.Client{Timeout: attachTimeout},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + info.Token}},
	})
	if err != nil {
		removeIfDead(stateDir, info.PID)
		return daemonTarget{}, false
	}
	defer ws.CloseNow()
	if _, err := rpcCall(ctx, ws, "project.list", nil); err != nil {
		removeIfDead(stateDir, info.PID)
		return daemonTarget{}, false
	}
	httpURL := info.HTTPURL
	if httpURL == "" {
		httpURL = httpURLFromWS(info.WSURL)
	}
	return daemonTarget{
		wsURL:   info.WSURL,
		httpURL: httpURL,
		token:   info.Token,
		host:    hostFromWS(info.WSURL),
	}, true
}

// removeIfDead deletes the discovery file only when its recorded pid is
// provably gone. A failed probe against a live process (or a record with no
// pid) leaves the file for its owner; the next writer replaces it atomically.
func removeIfDead(stateDir string, pid int) {
	if pid <= 0 || processAlive(pid) {
		return
	}
	_ = discovery.Remove(stateDir)
}

// hostFromWS returns the daemon's host:port for display, falling back to the
// raw URL when it cannot be parsed.
func hostFromWS(ws string) string {
	u, err := url.Parse(ws)
	if err != nil || u.Host == "" {
		return ws
	}
	return u.Host
}

// prefs are the desktop shell's own settings, kept beside the daemon state so
// they survive restarts without touching daemon settings.
type prefs struct {
	KeepRunning bool `json:"keepRunning"`
}

func prefsPath(stateDir string) string { return filepath.Join(stateDir, "desktop-prefs.json") }

// loadPrefs returns the saved desktop preferences. A missing or malformed file
// reads as defaults: the shell must boot even when its own state is unreadable.
func loadPrefs(stateDir string) prefs {
	var p prefs
	data, err := os.ReadFile(prefsPath(stateDir))
	if err != nil {
		return p
	}
	_ = json.Unmarshal(data, &p)
	return p
}

// savePrefs writes the desktop preferences atomically as 0600, matching the
// discovery file's handling: a reader sees the old file or the complete new
// one, and the keep-running flag never becomes world-readable.
func savePrefs(stateDir string, p prefs) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(stateDir, ".desktop-prefs-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, prefsPath(stateDir))
}
