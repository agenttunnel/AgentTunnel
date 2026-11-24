package orchestrator

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

const failingThenOKScript = `case "$AT_TASK" in
*Ifail*) printf '{"type":"text","text":"partial work"}\n'; exit 1 ;;
esac
printf '{"type":"text","text":"ok"}\n'`

func runBatch(t *testing.T, r *Runner, tasks []contracts.DispatchTask) {
	t.Helper()
	ctx := t.Context()
	// Subagents have a parent; a parentless agent reads as the lead.
	for i := range tasks {
		tasks[i].ParentAgentID = "agent-orchestrator"
	}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
}

func TestRosterShowsFailureReason(t *testing.T) {
	r, eng, ctx := newHarness(t, failingThenOKScript)
	runBatch(t, r, []contracts.DispatchTask{
		{AgentID: "agent-i", Name: "impl", Role: contracts.RoleExecutor, Task: "Ifail"},
		{AgentID: "agent-g", Name: "good", Role: contracts.RoleExecutor, Task: "fine"},
	})
	state, _ := eng.State(ctx, "t1")
	reason := state.Agents["agent-i"].Reason
	if reason == "" {
		t.Fatal("failed agent has no reason")
	}
	var implLine, goodLine string
	for _, l := range strings.Split(r.Roster(state), "\n") {
		switch {
		case strings.HasPrefix(l, "- impl "):
			implLine = l
		case strings.HasPrefix(l, "- good "):
			goodLine = l
		}
	}
	if !strings.HasSuffix(implLine, "[failed] — "+reason) {
		t.Fatalf("impl roster line = %q, want the reason %q", implLine, reason)
	}
	if strings.Contains(goodLine, "—") {
		t.Fatalf("succeeded roster line carries a reason: %q", goodLine)
	}
}

func TestFailedRunNotebookEntryCarriesMarker(t *testing.T) {
	r, eng, ctx := newHarness(t, failingThenOKScript)
	runBatch(t, r, []contracts.DispatchTask{{AgentID: "agent-i", Name: "impl", Role: contracts.RoleExecutor, Task: "Ifail"}})
	state, _ := eng.State(ctx, "t1")
	got := state.Shared["result_impl"]
	if !strings.HasPrefix(got, "[failed: ") || !strings.HasSuffix(got, "] partial work") {
		t.Fatalf("result_impl = %q, want a failed marker then the partial output", got)
	}
}

func TestLongResultIsClippedWithMarker(t *testing.T) {
	script := `i=0; while [ $i -lt 600 ]; do printf '{"type":"text","text":"0123456789"}\n'; i=$((i+1)); done`
	r, eng, ctx := newHarness(t, script)
	runBatch(t, r, []contracts.DispatchTask{{AgentID: "agent-l", Name: "long", Role: contracts.RoleExecutor, Task: "go"}})
	state, _ := eng.State(ctx, "t1")
	got := state.Shared["result_long"]
	if !strings.Contains(got, "…[clipped, ") || !strings.HasSuffix(got, "read the full log with tool agent-logs]") {
		t.Fatalf("result_long lacks the clip marker: %q", got[max(0, len(got)-120):])
	}
	if len(got) > resultNotebookBytes+200 {
		t.Fatalf("result_long = %d bytes, want it clipped near %d", len(got), resultNotebookBytes)
	}
}

func TestLaterPassCancelsDependentOfEarlierFailure(t *testing.T) {
	r, eng, ctx := newHarness(t, failingThenOKScript)
	runBatch(t, r, []contracts.DispatchTask{{AgentID: "agent-i", Name: "impl", Role: contracts.RoleExecutor, Task: "Ifail"}})
	runBatch(t, r, []contracts.DispatchTask{{AgentID: "agent-r", Name: "review", Role: contracts.RoleExecutor, Task: "review", DependsOn: []string{"impl"}}})
	state, _ := eng.State(ctx, "t1")
	got := state.Agents["agent-r"]
	if got.Status != contracts.AgentCancelled || got.Reason != "unsatisfied dependencies" {
		t.Fatalf("review = %s (%q), want cancelled: unsatisfied dependencies", got.Status, got.Reason)
	}
}

func TestLaterPassRunsDependentOfEarlierSuccess(t *testing.T) {
	r, eng, ctx := newHarness(t, failingThenOKScript)
	runBatch(t, r, []contracts.DispatchTask{{AgentID: "agent-i", Name: "impl", Role: contracts.RoleExecutor, Task: "fine"}})
	runBatch(t, r, []contracts.DispatchTask{{AgentID: "agent-r", Name: "review", Role: contracts.RoleExecutor, Task: "review", DependsOn: []string{"impl"}}})
	state, _ := eng.State(ctx, "t1")
	if got := state.Agents["agent-r"].Status; got != contracts.AgentSucceeded {
		t.Fatalf("review = %s, want succeeded", got)
	}
}

func TestUnknownDependencyCancelsDependent(t *testing.T) {
	r, eng, ctx := newHarness(t, failingThenOKScript)
	runBatch(t, r, []contracts.DispatchTask{{AgentID: "agent-r", Name: "review", Role: contracts.RoleExecutor, Task: "review", DependsOn: []string{"imlp"}}})
	state, _ := eng.State(ctx, "t1")
	got := state.Agents["agent-r"]
	if got.Status != contracts.AgentCancelled || got.Reason != "unknown dependency imlp" {
		t.Fatalf("review = %s (%q), want cancelled: unknown dependency imlp", got.Status, got.Reason)
	}
}
