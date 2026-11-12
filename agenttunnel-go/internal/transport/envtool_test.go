package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/files"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// newEnvFixture gives thread t1 a working directory: the settings workdir,
// which a thread with no worktree or project runs in.
func newEnvFixture(t *testing.T) (*domain.Engine, *Server, string) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	dir := t.TempDir()
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Settings: settings.New(dir)})
	srv := NewServer(disp, eng, nil, "", toolbox.New(), nil)
	createThreadForTool(t, eng, "t1", nil)
	return eng, srv, dir
}

func TestEnvToolCallsAreRefusedForSubagents(t *testing.T) {
	_, srv, _ := newEnvFixture(t)
	for _, kind := range []string{"read-file", "write-file", "shell"} {
		body := `{"threadId":"t1","agentId":"agent-hub","kind":"` + kind + `","path":"a.txt","command":"true"}`
		if rec := postLocalToolCall(t, srv, body); rec.Code != http.StatusForbidden {
			t.Fatalf("subagent %s = %d, want 403", kind, rec.Code)
		}
		anon := `{"threadId":"t1","kind":"` + kind + `","path":"a.txt","command":"true"}`
		if rec := postLocalToolCall(t, srv, anon); rec.Code != http.StatusForbidden {
			t.Fatalf("anonymous %s = %d, want 403", kind, rec.Code)
		}
	}
}

func TestEnvWriteThenReadFile(t *testing.T) {
	_, srv, dir := newEnvFixture(t)
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"write-file","path":"notes/a.txt","text":"hello"}`); rec.Code != http.StatusOK {
		t.Fatalf("write-file = %d: %s", rec.Code, rec.Body.String())
	}
	if got, err := os.ReadFile(filepath.Join(dir, "notes", "a.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("on disk = %q, %v; want hello", got, err)
	}

	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"read-file","path":"notes/a.txt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("read-file = %d: %s", rec.Code, rec.Body.String())
	}
	var res files.ReadResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("read-file body: %v\n%s", err, rec.Body.String())
	}
	if res.Text != "hello" || res.Size != 5 || res.Truncated || res.Binary {
		t.Fatalf("read-file = %+v, want hello, size 5", res)
	}
}

func TestEnvFilePathsCannotLeaveTheWorkdir(t *testing.T) {
	_, srv, dir := newEnvFixture(t)
	// ".." is clamped at the workdir root, so the write lands inside the workdir.
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"write-file","path":"../escape.txt","text":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("clamped write-file = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err != nil {
		t.Fatalf("clamped write did not land in the workdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); err == nil {
		t.Fatal("an escaping write reached the disk outside the workdir")
	}
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"read-file"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("read-file with no path = %d, want 400", rec.Code)
	}
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"read-file","path":"."}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("read-file of the workdir root = %d, want 400 (not a file)", rec.Code)
	}
}

func TestEnvWriteRejectsOversizedContent(t *testing.T) {
	_, srv, _ := newEnvFixture(t)
	body, err := json.Marshal(map[string]any{
		"threadId": "t1", "agentId": "agent-orchestrator", "kind": "write-file",
		"path": "big.txt", "text": strings.Repeat("x", files.MaxWriteBytes+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := postLocalToolCall(t, srv, string(body)); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized write-file = %d, want 400", rec.Code)
	}
}

// A shell runs in the worktree, or in --cwd under it, and its output is
// returned with the exit code and written to the thread log.
func TestEnvShellRunsInWorkdirAndRecordsOutput(t *testing.T) {
	eng, srv, _ := newEnvFixture(t)
	ctx := context.Background()
	postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"write-file","path":"sub/marker.txt","text":"marker"}`)

	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"shell","command":"cat marker.txt; exit 3","cwd":"sub"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("shell = %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		ExitCode int    `json:"exitCode"`
		Output   string `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("shell body: %v\n%s", err, rec.Body.String())
	}
	if reply.ExitCode != 3 || reply.Output != "marker" {
		t.Fatalf("shell = exit %d output %q, want exit 3 output marker", reply.ExitCode, reply.Output)
	}

	st, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	recorded := false
	for _, m := range st.Messages {
		if m.Author == "shell" && strings.Contains(m.Text, "marker") {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("shell output was not written to the thread log: %+v", st.Messages)
	}
	if st.Turn != 0 {
		t.Fatalf("a shell call counted as a turn: Turn = %d", st.Turn)
	}
}

func TestEnvShellCwdMustBeADirectory(t *testing.T) {
	_, srv, _ := newEnvFixture(t)
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"shell","command":"true","cwd":"missing"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing cwd = %d, want 400", rec.Code)
	}
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"shell","command":"  "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank command = %d, want 400", rec.Code)
	}
}

// Stopping the thread's turn ends a lead's shell at once, through the same turn
// context as any other turn work. The hook stops the turn the moment the child
// is running, so the test does not wait on the command.
func TestEnvShellStopsWithTheTurn(t *testing.T) {
	_, srv, _ := newEnvFixture(t)
	disp := srv.disp
	_, done, err := disp.beginTurn(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	disp.shellStarted = func() { disp.stopTurn("t1") }
	defer func() { disp.shellStarted = nil }()

	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"shell","command":"sleep 30"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("shell = %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		Stopped bool `json:"stopped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.Stopped {
		t.Fatalf("shell was not stopped by the turn: %s", rec.Body.String())
	}
}

// With no turn running, a shell still ends when the calling process goes away.
func TestToolContextEndsWithTheRequest(t *testing.T) {
	disp := NewDispatcher(DispatcherDeps{})
	reqCtx, cancelReq := context.WithCancel(context.Background())
	ctx, done := disp.toolContext(reqCtx, "t1")
	defer done()
	cancelReq()
	<-ctx.Done()
}

func TestEnvShellTimeoutIsDefaultedAndCapped(t *testing.T) {
	if got := envShellTimeout(0); got != shellTimeout {
		t.Fatalf("unset timeout = %s, want %s", got, shellTimeout)
	}
	if got := envShellTimeout(5); got != 5*time.Second {
		t.Fatalf("timeout 5 = %s", got)
	}
	if got := envShellTimeout(1 << 40); got != envShellMaxTimeout {
		t.Fatalf("huge timeout = %s, want the cap %s", got, envShellMaxTimeout)
	}
}

// A lead's call from a remote peer is refused even though the body names the
// lead: the agent id is self-declared, so the peer is what decides.
func TestEnvToolCallsAreRefusedFromARemotePeer(t *testing.T) {
	_, srv, dir := newEnvFixture(t)
	for _, body := range []string{
		`{"threadId":"t1","agentId":"agent-orchestrator","kind":"write-file","path":"remote.txt","text":"x"}`,
		`{"threadId":"t1","agentId":"agent-orchestrator","kind":"read-file","path":"remote.txt"}`,
		`{"threadId":"t1","agentId":"agent-orchestrator","kind":"shell","command":"touch remote-shell.txt"}`,
	} {
		if rec := postToolCall(t, srv, body); rec.Code != http.StatusForbidden {
			t.Fatalf("remote %s = %d, want 403", body, rec.Code)
		}
	}
	for _, name := range []string{"remote.txt", "remote-shell.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("a refused remote call still created %s", name)
		}
	}
}

// A symlink inside the worktree that points outside it is not a way out: a
// write, a read, or a --cwd through it is refused, and nothing lands outside.
func TestEnvFilePathsCannotFollowSymlinksOut(t *testing.T) {
	_, srv, dir := newEnvFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"write-file","path":"link/new.txt","text":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("write-file through a symlink out = %d, want 400", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Fatal("a write through a symlink reached the outside directory")
	}
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"read-file","path":"link/secret.txt"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("read-file through a symlink out = %d, want 400", rec.Code)
	}
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"shell","command":"true","cwd":"link"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("shell --cwd through a symlink out = %d, want 400", rec.Code)
	}
}

// --cwd is relative to the worktree. An absolute path or a ".." is refused
// outright rather than clamped, so the request means what it says.
func TestEnvShellCwdCannotLeaveTheWorkdir(t *testing.T) {
	_, srv, dir := newEnvFixture(t)
	for _, cwd := range []string{dir, "..", "sub/../..", "/"} {
		body := `{"threadId":"t1","agentId":"agent-orchestrator","kind":"shell","command":"touch cwd-escape.txt","cwd":"` + strings.ReplaceAll(cwd, `\`, `\\`) + `"}`
		if rec := postLocalToolCall(t, srv, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("shell --cwd %q = %d, want 400", cwd, rec.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "cwd-escape.txt")); err == nil {
		t.Fatal("a refused --cwd still ran the command")
	}
}
