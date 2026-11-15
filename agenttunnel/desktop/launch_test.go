package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// recordingDaemon answers every RPC with an empty result and records each
// method and its params, so a test can assert exactly what the launcher sent.
type recordingDaemon struct {
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	method string
	params map[string]any
}

func (d *recordingDaemon) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var req struct {
				ID     string         `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			d.mu.Lock()
			d.calls = append(d.calls, recordedCall{method: req.Method, params: req.Params})
			d.mu.Unlock()
			resp, _ := json.Marshal(map[string]any{"id": req.ID, "result": map[string]any{"ok": true}})
			if err := conn.Write(ctx, websocket.MessageText, resp); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (d *recordingDaemon) methods() []recordedCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]recordedCall(nil), d.calls...)
}

func TestForwardToRunningFocusesThreadWithoutOpeningAProject(t *testing.T) {
	daemon := &recordingDaemon{}
	srv := daemon.serve(t)
	state := t.TempDir()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	if err := writeLock(state, launchLock{WsURL: wsURL, Token: "tok"}); err != nil {
		t.Fatalf("writeLock: %v", err)
	}

	if !forwardToRunning(state, "", "th_abc123456789") {
		t.Fatal("forwardToRunning reported no running app")
	}
	calls := daemon.methods()
	if len(calls) != 1 || calls[0].method != "app.open" || calls[0].params["threadId"] != "th_abc123456789" {
		t.Fatalf("calls = %+v, want one app.open for the thread", calls)
	}
}

func TestForwardToRunningWithoutLockBootsItself(t *testing.T) {
	if forwardToRunning(t.TempDir(), "", "th_abc") {
		t.Fatal("forwardToRunning claimed a running app without a lock file")
	}
}
