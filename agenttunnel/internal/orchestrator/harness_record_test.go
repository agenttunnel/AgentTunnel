package orchestrator

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// TestAgentsRecordTheHarnessTheyRun checks that a lead and a dispatched subagent,
// neither of which names a harness, record the harness they run on, so harness
// stats never list them as unknown.
func TestAgentsRecordTheHarnessTheyRun(t *testing.T) {
	script := `if [ "$AT_PASS" = "0" ]; then
  printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"impl","task":"implement the feature","role":"executor"}'
fi
printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
printf '{"usage":{"input_tokens":1,"output_tokens":1}}\n'`

	r, eng, ctx := newHarness(t, script)
	orchRole, _ := r.roles.Get(contracts.RoleOrchestrator)
	execRole, _ := r.roles.Get(contracts.RoleExecutor)

	if err := New(r).RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	if got := state.Agents["agent-orchestrator"].Harness; got != orchRole.DefaultHarness {
		t.Fatalf("lead harness = %q, want the orchestrator role's %q", got, orchRole.DefaultHarness)
	}
	if got := state.Agents["agent-impl"].Harness; got != execRole.DefaultHarness {
		t.Fatalf("subagent harness = %q, want the executor role's %q", got, execRole.DefaultHarness)
	}
}

// TestLeadRecordsTheModelTheHarnessReports checks that a lead dispatched with
// no model (the harness default) records the model its harness reports, so
// model stats name the model that ran rather than "default".
func TestLeadRecordsTheModelTheHarnessReports(t *testing.T) {
	script := `printf '%s\n' '{"type":"assistant","message":{"model":"claude-test-model","role":"assistant","content":[{"type":"text","text":"hi"}]}}'
printf '{"usage":{"input_tokens":1,"output_tokens":1}}\n'`

	r, eng, ctx := newHarness(t, script)
	if err := New(r).RunTurn(ctx, "t1", "hello"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	if got := state.Agents["agent-orchestrator"].Model; got != "claude-test-model" {
		t.Fatalf("lead model = %q, want the model the harness reported", got)
	}
}
