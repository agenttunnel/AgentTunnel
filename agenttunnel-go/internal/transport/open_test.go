package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// waitDeadline bounds how long a test waits for a pushed frame.
const waitDeadline = 5 * time.Second

// newOpenFixture assembles the dispatcher with a settings store, which
// thread.open's project resolution needs, plus a live HTTP server a second
// client (the desktop CLI forwarder's shape) can dial.
func newOpenFixture(t *testing.T) (*Dispatcher, *Server, string) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	disp := NewDispatcher(DispatcherDeps{
		Engine:   eng,
		Logs:     logstore.New(t.TempDir()),
		Settings: settings.New(t.TempDir()),
	})
	srv := NewServer(disp, eng, nil, "", toolbox.New())
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	return disp, srv, httpSrv.URL
}

type openResult struct {
	ThreadID       string `json:"threadId"`
	ProjectID      string `json:"projectId"`
	ProjectCreated bool   `json:"projectCreated"`
}

// callOpen posts `thread.open` over a raw WS connection, the way the desktop
// CLI forwarder does, and returns the decoded result.
func callOpen(t *testing.T, url, dir string) openResult {
	t.Helper()
	ws, _, err := websocket.Dial(context.Background(), url+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.CloseNow()
	req, _ := json.Marshal(map[string]any{"id": "1", "method": "thread.open", "params": map[string]any{"dir": dir}})
	if err := ws.Write(context.Background(), websocket.MessageText, req); err != nil {
		t.Fatal(err)
	}
	_, msg, err := ws.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result openResult `json:"result"`
		Error  *RPCError  `json:"error"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("thread.open failed: %s", resp.Error.Message)
	}
	return resp.Result
}

// TestThreadOpenCreatesProjectForUnknownDir: an unknown directory becomes a
// project named after the directory, and the thread is provisional — nothing
// durable exists until the thread is used.
func TestThreadOpenCreatesProjectForUnknownDir(t *testing.T) {
	d, _, url := newOpenFixture(t)

	dir := t.TempDir()
	res := callOpen(t, url, dir)
	if !res.ProjectCreated || res.ProjectID == "" || res.ThreadID == "" {
		t.Fatalf("open = %+v", res)
	}
	if p, ok := d.settings.Project(res.ProjectID); !ok || p.Name != filepath.Base(resolveDirPath(dir)) || p.Path != resolveDirPath(dir) {
		t.Fatalf("created project = %+v", p)
	}

	// Provisional: the thread answers thread.get but was never persisted.
	st, err := d.engine.State(context.Background(), contracts.ThreadID(res.ThreadID))
	if err != nil || !st.Exists || st.ProjectID != res.ProjectID {
		t.Fatalf("thread state = %+v, err = %v", st, err)
	}

	// Empty dir is refused.
	ws, _, err := websocket.Dial(context.Background(), url+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	req, _ := json.Marshal(map[string]any{"id": "2", "method": "thread.open", "params": map[string]any{"dir": "  "}})
	_ = ws.Write(context.Background(), websocket.MessageText, req)
	_, msg, err := ws.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "dir is required") {
		t.Fatalf("empty dir should be refused: %s", msg)
	}
}

// TestThreadOpenReusesProjectByDir: a known directory (or a symlink to it)
// reuses its project instead of creating a duplicate, and opens a fresh thread
// each time.
func TestThreadOpenReusesProjectByDir(t *testing.T) {
	d, _, url := newOpenFixture(t)
	ctx := context.Background()

	dir := t.TempDir()
	first := callOpen(t, url, dir)
	if !first.ProjectCreated {
		t.Fatalf("first open should create: %+v", first)
	}

	// Same directory: same project, new thread.
	second := callOpen(t, url, dir)
	if second.ProjectCreated || second.ProjectID != first.ProjectID {
		t.Fatalf("second open = %+v, want project %s reused", second, first.ProjectID)
	}
	if second.ThreadID == first.ThreadID {
		t.Fatalf("each open must be a fresh thread: %s", second.ThreadID)
	}

	// A symlink to the directory resolves to the same project.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	third := callOpen(t, url, link)
	if third.ProjectCreated || third.ProjectID != first.ProjectID {
		t.Fatalf("symlink open = %+v, want project %s", third, first.ProjectID)
	}

	// A relative path to the same directory also resolves there, when it is
	// reachable without climbing out of the working directory.
	wd, _ := os.Getwd()
	rel, err := filepath.Rel(wd, dir)
	if err == nil && !strings.HasPrefix(rel, "..") {
		fourth := callOpen(t, url, rel)
		if fourth.ProjectID != first.ProjectID {
			t.Fatalf("relative open = %+v, want project %s", fourth, first.ProjectID)
		}
	}

	if ids, _ := d.engine.Threads(ctx); len(ids) != 3 {
		t.Fatalf("threads = %v", ids)
	}
}

// TestAppOpenBroadcastsToClients: one client's app.open reaches every other
// client as an app.open frame with the thread's project, so the running
// desktop can open the forwarded thread.
func TestAppOpenBroadcastsToClients(t *testing.T) {
	_, _, url := newOpenFixture(t)
	ctx := context.Background()

	// The listener: the running desktop's connection.
	listener, _, err := websocket.Dial(ctx, url+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.CloseNow()
	// Drain nothing first; the frame we want is the only unsolicited one.

	// The forwarder: opens a thread, then asks the app to show it.
	res := callOpen(t, url, t.TempDir())
	forwarder, _, err := websocket.Dial(ctx, url+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer forwarder.CloseNow()
	req, _ := json.Marshal(map[string]any{
		"id": "1", "method": "app.open", "params": map[string]any{"threadId": res.ThreadID},
	})
	if err := forwarder.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatal(err)
	}

	deadline, cancel := context.WithTimeout(ctx, waitDeadline)
	defer cancel()
	for {
		_, msg, err := listener.Read(deadline)
		if err != nil {
			t.Fatalf("waiting for app.open frame: %v", err)
		}
		var frame struct {
			Method string `json:"method"`
			Params struct {
				ThreadID  string `json:"threadId"`
				ProjectID string `json:"projectId"`
			} `json:"params"`
		}
		if json.Unmarshal(msg, &frame) != nil || frame.Method != "app.open" {
			continue
		}
		if frame.Params.ThreadID != res.ThreadID || frame.Params.ProjectID != res.ProjectID {
			t.Fatalf("app.open frame = %+v", frame.Params)
		}
		return
	}
}
