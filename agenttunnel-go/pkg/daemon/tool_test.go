package daemon

import (
	"net/http/httptest"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/toolcli"
)

// TestToolCommandReachesTheInbox exercises the whole agent-facing tool path
// deterministically: `tool dispatch` → authenticated HTTP → the inbox the
// orchestrator drains.
func TestToolCommandReachesTheInbox(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.Server.Handler())
	defer srv.Close()

	t.Setenv("AT_DAEMON_URL", srv.URL)
	t.Setenv("AT_TOKEN", "secret")
	t.Setenv("AT_THREAD_ID", "t1")
	t.Setenv("AT_AGENT_ID", "agent-orchestrator")

	code := toolcli.Run([]string{
		"dispatch", "--role", "planner", "--name", "explore", "--task", "map the repository",
	})
	if code != 0 {
		t.Fatalf("tool dispatch exited %d", code)
	}

	calls := d.Runner.DrainTools("t1")
	if len(calls) != 1 {
		t.Fatalf("inbox = %+v", calls)
	}
	got := calls[0]
	if got.Kind != toolbox.KindDispatch || got.Role != contracts.RolePlanner ||
		got.Task != "map the repository" || got.Name != "explore" || got.AgentID != "agent-orchestrator" {
		t.Fatalf("call = %+v", got)
	}
}

func TestToolCommandRejectsBadToken(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.Server.Handler())
	defer srv.Close()

	t.Setenv("AT_DAEMON_URL", srv.URL)
	t.Setenv("AT_TOKEN", "wrong")
	t.Setenv("AT_THREAD_ID", "t1")

	if code := toolcli.Run([]string{"dispatch", "--task", "x"}); code == 0 {
		t.Fatal("expected a non-zero exit for a bad token")
	}
	if calls := d.Runner.DrainTools("t1"); len(calls) != 0 {
		t.Fatalf("unauthorized call reached the inbox: %+v", calls)
	}
}

// The spawn-gating kinds ride the same path unchanged: the CLI passes any
// kind through, and the inbox records it verbatim.
func TestSpawnAndApproveCommandsReachTheInbox(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.Server.Handler())
	defer srv.Close()

	t.Setenv("AT_DAEMON_URL", srv.URL)
	t.Setenv("AT_TOKEN", "secret")
	t.Setenv("AT_THREAD_ID", "t1")
	t.Setenv("AT_AGENT_ID", "agent-outer-1")

	if code := toolcli.Run([]string{"spawn", "--name", "inner", "--task", "write the inner tests"}); code != 0 {
		t.Fatalf("tool spawn exited %d", code)
	}
	if code := toolcli.Run([]string{"approve", "--name", "inner"}); code != 0 {
		t.Fatalf("tool approve exited %d", code)
	}

	calls := d.Runner.DrainTools("t1")
	if len(calls) != 2 {
		t.Fatalf("inbox = %+v", calls)
	}
	if calls[0].Kind != toolbox.KindSpawn || calls[0].Name != "inner" ||
		calls[0].Task != "write the inner tests" || calls[0].AgentID != "agent-outer-1" {
		t.Fatalf("spawn call = %+v", calls[0])
	}
	if calls[1].Kind != toolbox.KindApprove || calls[1].Name != "inner" ||
		calls[1].AgentID != "agent-outer-1" {
		t.Fatalf("approve call = %+v", calls[1])
	}
}

func TestToolCommandRequiresThread(t *testing.T) {
	t.Setenv("AT_DAEMON_URL", "http://127.0.0.1:1")
	t.Setenv("AT_THREAD_ID", "")
	if code := toolcli.Run([]string{"dispatch", "--task", "x"}); code == 0 {
		t.Fatal("expected a non-zero exit without AT_THREAD_ID")
	}
}
