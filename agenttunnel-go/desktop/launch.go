package main

// Launch handling for `agenttunnel [dir]`: opening a project directory from
// the terminal. When the app is already running (its lock file answers), the
// invocation forwards the open-request over the daemon's WebSocket and exits;
// otherwise the app boots with the directory as its launch target.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/coder/websocket"
)

// launchLock is the running app's handoff point: where its daemon listens and
// how to authenticate to it. Written on boot, removed on exit; a stale lock
// from a crash simply fails to connect and is ignored.
type launchLock struct {
	WsURL string `json:"wsUrl"`
	Token string `json:"token"`
}

func lockPath(stateDir string) string {
	return filepath.Join(stateDir, "desktop.json")
}

func readLock(stateDir string) (launchLock, bool) {
	var lock launchLock
	data, err := os.ReadFile(lockPath(stateDir))
	if err != nil {
		return lock, false
	}
	if json.Unmarshal(data, &lock) != nil || lock.WsURL == "" || lock.Token == "" {
		return lock, false
	}
	return lock, true
}

func writeLock(stateDir string, lock launchLock) error {
	data, err := json.Marshal(lock)
	if err != nil {
		return err
	}
	return os.WriteFile(lockPath(stateDir), data, 0o600)
}

func removeLock(stateDir string) {
	_ = os.Remove(lockPath(stateDir))
}

// launchDirArg validates a command-line argument as a project directory and
// returns its absolute path.
func launchDirArg(arg string) (string, error) {
	info, err := os.Stat(arg)
	if err != nil {
		return "", fmt.Errorf("cannot open %q: %w", arg, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", arg)
	}
	if abs, err := filepath.Abs(arg); err == nil {
		return abs, nil
	}
	return arg, nil
}

// forwardToRunning hands the open-request to an already-running app over its
// daemon WebSocket, then reports true. A missing or stale lock, or any
// connection failure, reports false so the caller boots a fresh app instead.
func forwardToRunning(stateDir, dir string) bool {
	lock, ok := readLock(stateDir)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, lock.WsURL+"?token="+lock.Token, &websocket.DialOptions{
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
	})
	if err != nil {
		return false
	}
	defer ws.CloseNow()

	var threadID string
	if dir != "" {
		res, err := rpcCall(ctx, ws, "thread.open", map[string]string{"dir": dir})
		if err != nil {
			fmt.Fprintf(os.Stderr, "agenttunnel: open %s: %v\n", dir, err)
			return false
		}
		threadID, _ = res["threadId"].(string)
	}
	if _, err := rpcCall(ctx, ws, "app.open", map[string]string{"threadId": threadID}); err != nil {
		return false
	}
	if dir == "" {
		fmt.Println("AgentTunnel is already running — focused it.")
	}
	return true
}

// rpcCall performs one JSON-RPC call over the WebSocket and returns its
// result object, keeping the connection's other frames moving.
func rpcCall(ctx context.Context, ws *websocket.Conn, method string, params any) (map[string]any, error) {
	req, err := json.Marshal(map[string]any{"id": method, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if err := ws.Write(ctx, websocket.MessageText, req); err != nil {
		return nil, err
	}
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		var resp struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &resp) != nil || resp.ID != method {
			continue // a pushed event frame; keep reading
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s", resp.Error.Message)
		}
		out := map[string]any{}
		if len(resp.Result) > 0 {
			_ = json.Unmarshal(resp.Result, &out)
		}
		return out, nil
	}
}
