package daemon

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// The spawn-gating loop end to end over the real agent-facing path: the
// executor runs its `$AT_TOOL` binary mid-wave, the proposal arrives over HTTP,
// the next lead pass sees it (and could not have run it without approving),
// the lead approves, and the child runs parented on the executor.
func TestApprovedSpawnProposalRunsEndToEnd(t *testing.T) {
	// Tool output is redirected: "ok" on stdout would otherwise glue itself to
	// the JSON text frames the fake harness streams.
	script := `case "$AT_ROLE" in
orchestrator)
  case "$AT_PASS" in
  0) "$AT_TOOL" tool dispatch --name outer --task "outer work" >/dev/null ;;
  1) "$AT_TOOL" tool approve --name inner >/dev/null ;;
  esac
  printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
  ;;
*)
  if [ "$AT_TASK" = "outer work" ]; then
    "$AT_TOOL" tool spawn --name inner --task "inner work" >/dev/null
  fi
  printf '{"type":"text","text":"%s findings"}\n' "$AT_AGENT_ID"
  ;;
esac`

	// Agents need a real binary that understands `tool …`; build one.
	toolBin := filepath.Join(t.TempDir(), "atd")
	build := exec.Command("go", "build", "-o", toolBin, "github.com/agenttunnel/agenttunnel-go/cmd/atd")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build atd: %v\n%s", err, out)
	}

	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: t.TempDir(),
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		ToolPath: toolBin,
		Config:   orchestrator.Config{MaxPasses: 3, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	defer d.Close()

	srv := httptest.NewServer(d.Server.Handler())
	defer srv.Close()
	d.SetEndpoint(srv.URL)

	ctx := context.Background()
	if err := d.Engine.Dispatch(ctx, contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Orch.RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := d.Engine.State(ctx, "t1")
	inner := state.Agents["agent-inner-2"]
	if inner.Status != contracts.AgentSucceeded {
		t.Fatalf("inner status = %s, agents = %+v", inner.Status, state.Agents)
	}
	// The child hangs off the executor that proposed it, not the lead.
	if inner.ParentAgentID != "agent-outer-1" {
		t.Fatalf("inner parent = %q, want the executor agent-outer-1", inner.ParentAgentID)
	}
	if inner.Role != contracts.RoleExecutor || inner.Task != "inner work" {
		t.Fatalf("approved request not preserved: %+v", inner)
	}
	if state.Agents["agent-orchestrator"].Status != contracts.AgentSucceeded {
		t.Fatalf("orchestrator status = %s", state.Agents["agent-orchestrator"].Status)
	}

	found := false
	for _, m := range state.Messages {
		if m.Author == "orchestrator" && m.Text == "report-2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("orchestrator final message missing: %+v", state.Messages)
	}

	// If the lead had approved blindly without the request in its prompt, the
	// decision would have left a note instead of running the child.
	for _, note := range d.Runner.Notebook("t1").Get("notes") {
		if strings.Contains(note.Value, "No pending spawn request") {
			t.Fatalf("unexpected decision note: %+v", note)
		}
	}
}
