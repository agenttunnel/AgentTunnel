package orchestrator

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestRunnerRefusesDisabledHarness(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)

	// The executor role defaults to opencode; disabling it must fail the task.
	r.settings.SetHarnessEnabled(contracts.HarnessOpenCode, false)

	tasks := []contracts.DispatchTask{
		{AgentID: "agent-a", Name: "a", Role: contracts.RoleExecutor, Task: "x"},
	}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}

	state, _ := eng.State(ctx, "t1")
	if state.Agents["agent-a"].Status != contracts.AgentFailed {
		t.Fatalf("status = %s, want failed", state.Agents["agent-a"].Status)
	}
}
