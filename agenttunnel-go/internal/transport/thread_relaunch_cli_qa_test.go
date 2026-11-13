package transport

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/toolcli"
)

// newSchedulingToolFixture is the /tool/call surface assembled the way the
// daemon assembles it: the runner and the HTTP server share one tool inbox.
func newSchedulingToolFixture(t *testing.T) (*domain.Engine, *orchestrator.Runner, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	box := toolbox.New()
	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:  eng,
		Logs:    logstore.New(t.TempDir()),
		Mailbox: contextstore.NewMailbox(),
		Tools:   box,
	})
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Runner: runner})
	srv := httptest.NewServer(NewServer(disp, eng, runner, "", box, nil).Handler())
	t.Cleanup(srv.Close)
	return eng, runner, srv
}

// The agent-facing contract for the thread-relaunch tool: an agent runs
// `"$AT_TOOL" tool thread-relaunch --thread T --text ...` mid-turn, the CLI
// posts it to /tool/call, and the call is queued for the orchestrator's
// pass-boundary drain. The queued call must name the target thread, or the
// drain refuses it with "target thread is required".
func TestThreadRelaunchCLICallQueuesItsTargetThread(t *testing.T) {
	eng, runner, srv := newSchedulingToolFixture(t)
	createThreadForTool(t, eng, "t1", nil)

	t.Setenv("AT_THREAD_ID", "t1")
	t.Setenv("AT_AGENT_ID", "agent-orchestrator")
	t.Setenv("AT_DAEMON_URL", srv.URL)

	if code := toolcli.Run([]string{"thread-relaunch", "--thread", "t2", "--text", "go deeper"}); code != 0 {
		t.Fatalf("tool thread-relaunch exited %d", code)
	}
	calls := runner.DrainTools("t1")
	if len(calls) != 1 {
		t.Fatalf("queued calls = %+v, want exactly the relaunch", calls)
	}
	call := calls[0]
	if call.Kind != "thread-relaunch" {
		t.Fatalf("queued kind = %q, want thread-relaunch", call.Kind)
	}
	if call.Text != "go deeper" {
		t.Fatalf("queued text = %q, want the relaunch prompt", call.Text)
	}
	if call.Thread != "t2" {
		t.Fatalf("queued thread-relaunch lost its --thread target: thread = %q; the orchestrator drain refuses it with \"target thread is required\"", call.Thread)
	}
}

// The thread-results tool shares the same shape and the same drain, so its
// queued call must name the thread to read.
func TestThreadResultsCLICallQueuesItsTargetThread(t *testing.T) {
	eng, runner, srv := newSchedulingToolFixture(t)
	createThreadForTool(t, eng, "t1", nil)

	t.Setenv("AT_THREAD_ID", "t1")
	t.Setenv("AT_AGENT_ID", "agent-orchestrator")
	t.Setenv("AT_DAEMON_URL", srv.URL)

	if code := toolcli.Run([]string{"thread-results", "--thread", "t2"}); code != 0 {
		t.Fatalf("tool thread-results exited %d", code)
	}
	calls := runner.DrainTools("t1")
	if len(calls) != 1 {
		t.Fatalf("queued calls = %+v, want exactly the results read", calls)
	}
	call := calls[0]
	if call.Kind != "thread-results" {
		t.Fatalf("queued kind = %q, want thread-results", call.Kind)
	}
	if call.Thread != "t2" {
		t.Fatalf("queued thread-results lost its --thread target: thread = %q; the orchestrator drain refuses it with \"target thread is required\"", call.Thread)
	}
}
