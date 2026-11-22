package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
	"github.com/coder/websocket"
)

func TestExitCodeEmitsJSONErrorEnvelope(t *testing.T) {
	var errOut bytes.Buffer
	env := &Env{JSON: true, Err: &errOut, Out: &errOut}
	if code := exitCode(env, usageError("bad flag %q", "--nope")); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	var got struct {
		Error string `json:"error"`
		Code  int    `json:"code"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(errOut.Bytes()), &got); err != nil {
		t.Fatalf("stderr is not a JSON envelope: %v\n%s", err, errOut.String())
	}
	if got.Error != `bad flag "--nope"` || got.Code != 2 {
		t.Fatalf("envelope = %+v, want error/code 2", got)
	}
}

func TestExitCodeTextModeKeepsPlainErrors(t *testing.T) {
	var errOut bytes.Buffer
	env := &Env{Err: &errOut, Out: &errOut}
	if code := exitCode(env, errors.New("boom")); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got := errOut.String(); got != "agenttunnel: boom\n" {
		t.Fatalf("text stderr = %q", got)
	}
}

// A failure with no message means the command already printed its own result
// (for example daemon status --json with no daemon). Under --json it must add
// nothing to stderr, so the output stays one JSON document.
func TestExitCodeFailureWithoutMessageReportsCodeUnderJSON(t *testing.T) {
	var errOut bytes.Buffer
	env := &Env{JSON: true, Err: &errOut, Out: &errOut}
	if code := exitCode(env, failure()); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want nothing for a failure with no message", errOut.String())
	}
}

// connectReadOnly must say which cause kept it from a daemon: no discovery file,
// a token the daemon refuses, or a file whose daemon does not answer.
func TestConnectReadOnlyNamesTheCause(t *testing.T) {
	t.Run("not running", func(t *testing.T) {
		env := &Env{HomeDir: t.TempDir(), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
		_, err := connectReadOnly(context.Background(), env)
		if err == nil || !strings.Contains(err.Error(), "no AgentTunnel daemon is running") {
			t.Fatalf("err = %v, want the not-running message", err)
		}
	})

	t.Run("token rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		defer srv.Close()
		dir := t.TempDir()
		writeDiscovery(t, dir, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws")
		env := &Env{HomeDir: dir, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
		_, err := connectReadOnly(context.Background(), env)
		if err == nil || !strings.Contains(err.Error(), "rejected the token") {
			t.Fatalf("err = %v, want the rejected-token message", err)
		}
	})

	t.Run("not answering", func(t *testing.T) {
		// A listener that is closed before use gives a port nothing serves.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		dir := t.TempDir()
		writeDiscovery(t, dir, "ws://"+addr+"/ws")
		env := &Env{HomeDir: dir, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
		_, err = connectReadOnly(context.Background(), env)
		if err == nil || !strings.Contains(err.Error(), "not answering") {
			t.Fatalf("err = %v, want the not-answering message", err)
		}
		if strings.Contains(err.Error(), "no AgentTunnel daemon is running") {
			t.Fatalf("a stale daemon.json must not read as not running: %v", err)
		}
	})
}

func writeDiscovery(t *testing.T, dir, wsURL string) {
	t.Helper()
	if err := discovery.Write(dir, discovery.Info{WSURL: wsURL, Token: "stale-token"}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
}

// A failed autostart leaves the daemon's own words in atd.log, and the error
// points there. The fake atd writes to stderr and exits at once.
func TestSpawnDaemonKeepsOutputInAtdLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake atd is a shell script")
	}
	dir := t.TempDir()
	fake := filepath.Join(t.TempDir(), "atd")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'boot failure: bad config' >&2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AT_ATD", fake)

	if err := spawnDaemon(dir); err != nil {
		t.Fatalf("spawnDaemon: %v", err)
	}
	// The child writes its output after Start returns, so wait for the line to
	// land. The deadline only bounds a broken run; success is the file content.
	err := waitFor(context.Background(), 10*time.Second, func() bool {
		data, _ := os.ReadFile(atdLogPath(dir))
		return bytes.Contains(data, []byte("boot failure: bad config"))
	})
	if err != nil {
		t.Fatalf("atd.log never received the daemon output: %v", err)
	}
	data, _ := os.ReadFile(atdLogPath(dir))
	if !bytes.Contains(data, []byte("agenttunnel started atd")) {
		t.Fatalf("atd.log lacks the start marker:\n%s", data)
	}
}

// fakeStopDaemon answers RPCs. After daemon.shutdown it answers project.list
// for `answersAfter` more pings, then fails them, as a daemon mid-teardown does.
type fakeStopDaemon struct {
	answersAfter int

	mu           sync.Mutex
	shuttingDown bool
	pings        int
}

func (f *fakeStopDaemon) pingsAfterShutdown() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pings
}

func (f *fakeStopDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	for {
		_, data, err := ws.Read(r.Context())
		if err != nil {
			return
		}
		var req struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(data, &req) != nil {
			continue
		}
		reply := map[string]any{"id": req.ID, "result": map[string]any{}}
		switch req.Method {
		case "daemon.shutdown":
			f.mu.Lock()
			f.shuttingDown = true
			f.mu.Unlock()
			reply["result"] = map[string]any{"ok": true}
		case "project.list":
			f.mu.Lock()
			fail := false
			if f.shuttingDown {
				f.pings++
				fail = f.pings > f.answersAfter
			}
			f.mu.Unlock()
			if fail {
				reply = map[string]any{"id": req.ID, "error": map[string]any{"code": -32000, "message": "stopping"}}
			} else {
				reply["result"] = []any{}
			}
		}
		out, _ := json.Marshal(reply)
		if err := ws.Write(r.Context(), websocket.MessageText, out); err != nil {
			return
		}
	}
}

// `daemon stop --daemon <url>` must not return while the daemon still answers,
// and must report the discovery path's JSON shape (pid) plus the legacy wsUrl.
func TestDaemonStopURLWaitsForTheDaemonToStop(t *testing.T) {
	fake := &fakeStopDaemon{answersAfter: 2}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var out bytes.Buffer
	env := &Env{
		JSON:      true,
		DaemonURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws",
		HomeDir:   t.TempDir(),
		Out:       &out,
		Err:       &out,
	}
	if err := runDaemonStop(context.Background(), env, nil); err != nil {
		t.Fatalf("runDaemonStop: %v\n%s", err, out.String())
	}
	// Answers 1 and 2 keep the daemon up; the wait ends only on the first failed
	// ping, so at least answersAfter+1 pings happened before the command returned.
	if got := fake.pingsAfterShutdown(); got < fake.answersAfter+1 {
		t.Fatalf("returned after %d post-shutdown pings, want the wait to run until the daemon stopped answering", got)
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if got["ok"] != true || got["stopped"] != true {
		t.Fatalf("output = %v, want ok and stopped", got)
	}
	if _, ok := got["pid"]; !ok {
		t.Fatalf("output = %v, want the discovery shape with a pid", got)
	}
	// wsUrl is the legacy key; scripts that read it keep working.
	if got["wsUrl"] != env.DaemonURL {
		t.Fatalf("output = %v, want the legacy wsUrl %q kept beside pid", got, env.DaemonURL)
	}
}
