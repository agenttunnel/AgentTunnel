package toolcli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// newCLIServer assembles a real daemon HTTP surface for the tool CLI to post
// against, so the flags the CLI defines are the ones the server reads.
func newCLIServer(t *testing.T) (*domain.Engine, *orchestrator.Runner) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:  eng,
		Logs:    logstore.New(t.TempDir()),
		Mailbox: contextstore.NewMailbox(),
	})
	runner.Mailbox().SetEnabled(true)
	disp := transport.NewDispatcher(transport.DispatcherDeps{Engine: eng, Runner: runner})
	srv := transport.NewServer(disp, eng, runner, "secret", toolbox.New())
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)

	t.Setenv("AT_DAEMON_URL", httpSrv.URL)
	t.Setenv("AT_TOKEN", "secret")
	t.Setenv("AT_THREAD_ID", "t1")
	t.Setenv("AT_AGENT_ID", "agent-hub")
	return eng, runner
}

func TestToolMessageCLI(t *testing.T) {
	eng, runner := newCLIServer(t)
	ctx := context.Background()
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Dispatch(ctx, "t1", []contracts.DispatchTask{{
		AgentID: "agent-hub", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work",
	}}); err != nil {
		t.Fatal(err)
	}

	if code := Run([]string{"message", "--to", "orchestrator", "--text", "ping the lead"}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	state, _ := eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 ||
		state.Mailbox[0].From != "agent-hub" || state.Mailbox[0].To != "agent-orchestrator" ||
		state.Mailbox[0].Kind != "message" || state.Mailbox[0].Body != "ping the lead" {
		t.Fatalf("mailbox = %+v", state.Mailbox)
	}
	if inbox := runner.Mailbox().Inbox("t1", "agent-orchestrator"); len(inbox) != 1 {
		t.Fatalf("lead inbox = %+v", inbox)
	}

	// A message without --to is rejected by the server and reported as a
	// failed command, so the model behind the CLI sees why. Nothing durable
	// is recorded for the rejection.
	if code := Run([]string{"message", "--text", "no recipient"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	state, _ = eng.State(ctx, "t1")
	if len(state.Mailbox) != 1 {
		t.Fatalf("rejected message was recorded: %+v", state.Mailbox)
	}
}

func TestToolRequiresThreadID(t *testing.T) {
	t.Setenv("AT_THREAD_ID", "")
	if code := Run([]string{"note", "--text", "hi"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

// captureStdout runs fn and returns what it printed to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()
	fn()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestToolTaskCLI(t *testing.T) {
	eng, _ := newCLIServer(t)
	ctx := context.Background()
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}

	if code := Run([]string{"task-create", "--name", "api", "--task", "Design the API"}); code != 0 {
		t.Fatalf("create exit = %d, want 0", code)
	}
	if code := Run([]string{"task-create", "--name", "db", "--task", "Schema"}); code != 0 {
		t.Fatalf("create exit = %d, want 0", code)
	}
	// A duplicate name is refused, and the refusal reaches the model.
	if code := Run([]string{"task-create", "--name", "api", "--task", "Again"}); code != 1 {
		t.Fatalf("duplicate create exit = %d, want 1", code)
	}
	if code := Run([]string{"task-update", "--name", "api", "--status", "doing"}); code != 0 {
		t.Fatalf("update exit = %d, want 0", code)
	}
	if code := Run([]string{"task-update", "--name", "api", "--status", "blocked"}); code != 1 {
		t.Fatalf("bad status exit = %d, want 1", code)
	}

	var listed struct {
		Tasks []struct {
			Name   string `json:"name"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"tasks"`
	}
	var code int
	out := captureStdout(t, func() { code = Run([]string{"task-list"}) })
	if code != 0 {
		t.Fatalf("list exit = %d, want 0", code)
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list output is not JSON: %v\n%s", err, out)
	}
	want := []struct{ name, title, status string }{
		{"api", "Design the API", "doing"},
		{"db", "Schema", "todo"},
	}
	if len(listed.Tasks) != len(want) {
		t.Fatalf("listed %+v, want %+v", listed.Tasks, want)
	}
	for i, w := range want {
		got := listed.Tasks[i]
		if got.Name != w.name || got.Title != w.title || got.Status != w.status {
			t.Fatalf("task %d = %+v, want %+v", i, got, w)
		}
	}
}

// The subthread verb posts its brief as the task of a `subthread` call. The
// refusal or start is the daemon's to decide; the CLI only carries the request.
func TestToolSubthreadPostsTheBrief(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AT_THREAD_ID", "t1")
	t.Setenv("AT_AGENT_ID", "agent-orchestrator")
	t.Setenv("AT_DAEMON_URL", srv.URL)

	var code int
	captureStdout(t, func() { code = Run([]string{"subthread", "--task", "write the release notes"}) })
	if code != 0 {
		t.Fatalf("subthread exit = %d, want 0", code)
	}
	if got["kind"] != "subthread" || got["task"] != "write the release notes" || got["agentId"] != "agent-orchestrator" {
		t.Fatalf("posted body = %+v", got)
	}
}

// A started subthread prints the new thread's id, the only handle the lead has
// on it beyond the sidebar.
func TestToolSubthreadPrintsTheThreadID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"threadId":"thread-sub-7"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AT_THREAD_ID", "t1")
	t.Setenv("AT_AGENT_ID", "agent-orchestrator")
	t.Setenv("AT_DAEMON_URL", srv.URL)

	var code int
	out := captureStdout(t, func() { code = Run([]string{"subthread", "--task", "write the release notes"}) })
	if code != 0 {
		t.Fatalf("subthread exit = %d, want 0", code)
	}
	if out != "thread-sub-7\n" {
		t.Fatalf("stdout = %q, want the subthread's thread id", out)
	}
}
