package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// reviveFixture dispatches a subagent named hub with the tester role, runs it
// to completion, and leaves one output line in its log the follow-up can
// reference.
func reviveFixture(t *testing.T) (*Runner, context.Context) {
	t.Helper()
	r, _, ctx := newHarness(t, `printf '{"type":"text","text":"hub wrote the config loader"}\n'`)
	tasks := []contracts.DispatchTask{{
		AgentID: "agent-hub-1", Name: "hub",
		Role: contracts.RoleTester, Task: "write the config loader",
	}}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatalf("dispatch hub: %v", err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatalf("run hub: %v", err)
	}
	return r, ctx
}

func reviveLogLines(t *testing.T, r *Runner, agent contracts.AgentID) []string {
	t.Helper()
	var lines []string
	for _, e := range r.Logs().Entries("t1") {
		if e.AgentID == agent && e.Kind == contracts.LogError {
			lines = append(lines, e.Text)
		}
	}
	return lines
}

// A subagent cannot revive a teammate: the call is refused in the caller's
// log and no task is minted.
func TestReviveFromSubagentIsRefused(t *testing.T) {
	r, ctx := reviveFixture(t)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindRevive, AgentID: "agent-hub-1", To: "hub", Text: "why?",
	})

	tasks := New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 0 {
		t.Fatalf("a refused revive minted tasks: %+v", tasks)
	}
	lines := reviveLogLines(t, r, "agent-hub-1")
	if len(lines) != 1 || !strings.Contains(lines[0], "only the team lead can revive") {
		t.Fatalf("caller log = %v, want one refusal line", lines)
	}
}

func TestReviveBlankToRecordsLeadErrorAndMintsNoTask(t *testing.T) {
	r, ctx := reviveFixture(t)
	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindRevive, AgentID: "agent-orchestrator", To: "  ", Text: "why?"})

	tasks := New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 0 {
		t.Fatalf("a revive with no target minted tasks: %+v", tasks)
	}
	lines := reviveLogLines(t, r, "agent-orchestrator")
	if len(lines) != 1 || !strings.Contains(lines[0], "--to is required") {
		t.Fatalf("lead log = %v, want one --to error line", lines)
	}
}

func TestReviveUnknownTargetNamesIt(t *testing.T) {
	r, ctx := reviveFixture(t)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindRevive, AgentID: "agent-orchestrator", To: "ghost", Text: "why?",
	})

	tasks := New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 0 {
		t.Fatalf("a revive of an unknown agent minted tasks: %+v", tasks)
	}
	lines := reviveLogLines(t, r, "agent-orchestrator")
	if len(lines) != 1 || !strings.Contains(lines[0], "unknown agent ghost") {
		t.Fatalf("lead log = %v, want one line naming ghost", lines)
	}
}

// A running (not yet finished) agent cannot be revived: the lead is told to
// message it instead.
func TestReviveRunningTargetIsRefusedWithMessageHint(t *testing.T) {
	r, ctx := reviveFixture(t)
	r.setStatus(ctx, "t1", "agent-hub-1", contracts.AgentRunning, "")
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindRevive, AgentID: "agent-orchestrator", To: "hub", Text: "why?",
	})

	tasks := New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 0 {
		t.Fatalf("a revive of a running agent minted tasks: %+v", tasks)
	}
	lines := reviveLogLines(t, r, "agent-orchestrator")
	if len(lines) != 1 {
		t.Fatalf("lead log = %v, want one refusal line", lines)
	}
	if !strings.Contains(lines[0], "still running") || !strings.Contains(lines[0], "tool message --to hub") {
		t.Fatalf("refusal should point at the message tool:\n%s", lines[0])
	}
}

// A finished target is re-dispatched under its own name and role with a fresh
// agent id, no dependencies, and a task that carries the original task, the
// prior log, and the lead's question.
func TestReviveFinishedTargetMintsSameNameFreshIDTask(t *testing.T) {
	r, ctx := reviveFixture(t)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindRevive, AgentID: "agent-orchestrator", To: "hub", Text: "which file did you touch?",
	})

	tasks := New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	got := tasks[0]
	if got.Name != "hub" {
		t.Fatalf("revived name = %q, want the same roster name", got.Name)
	}
	if got.Role != contracts.RoleTester {
		t.Fatalf("revived role = %q, want the target's role", got.Role)
	}
	if got.AgentID == "" || got.AgentID == "agent-hub-1" {
		t.Fatalf("revived id = %q, want a fresh id distinct from the target", got.AgentID)
	}
	if !strings.HasPrefix(string(got.AgentID), "agent-hub-") {
		t.Fatalf("revived id = %q, want the agent-<name>-<seq> shape", got.AgentID)
	}
	if len(got.DependsOn) != 0 {
		t.Fatalf("revived task must run immediately, dependsOn = %v", got.DependsOn)
	}
	if got.ParentAgentID != "agent-orchestrator" {
		t.Fatalf("revived parent = %q, want the lead", got.ParentAgentID)
	}
	for _, want := range []string{
		"You are subagent hub",
		"write the config loader",
		"hub wrote the config loader",
		"which file did you touch?",
	} {
		if !strings.Contains(got.Task, want) {
			t.Fatalf("revived task lacks %q:\n%s", want, got.Task)
		}
	}
}

// A revive with no question has nothing to re-dispatch for, so it is refused
// like a message with no text.
func TestReviveBlankTextIsRefused(t *testing.T) {
	r, ctx := reviveFixture(t)
	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindRevive, AgentID: "agent-orchestrator", To: "hub"})

	tasks := New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 0 {
		t.Fatalf("a revive with no question minted tasks: %+v", tasks)
	}
	lines := reviveLogLines(t, r, "agent-orchestrator")
	if len(lines) != 1 || !strings.Contains(lines[0], "--text is required") {
		t.Fatalf("lead log = %v, want one --text error line", lines)
	}
}

// The full lifecycle: pass 0 dispatches hub, it finishes, and a revive posted
// while pass 1's lead is running re-runs hub under the same name. The revived
// run sees the follow-up — pinned by its script answering from the question —
// and its result reaches the lead as hub's shared result.
func TestRunTurnRunsRevivedSubagent(t *testing.T) {
	const script = `case "$AT_ROLE" in
executor)
  case "$AT_TASK" in
  *"which file did you touch?"*) printf '{"type":"text","text":"REVIVED-ANSWER: src/config.go"}\n' ;;
  *) printf '{"type":"text","text":"hub first run done"}\n' ;;
  esac ;;
*)
  if [ "$AT_PASS" = "0" ]; then
    printf '%s\n' '{"type":"text","text":"$AT_TOOL tool dispatch --name hub --task \"write the config loader\"\n"}'
  fi
  printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS" ;;
esac`

	var r *Runner
	r, eng, ctx := newHarnessInvoking(t, func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		if spec.Role == contracts.RoleOrchestrator && passOf(spec) == 1 {
			// The lead asks its follow-up mid-run, like a real tool call
			// posted while the pass is in flight.
			r.tools.Post("t1", toolbox.Call{
				Kind: toolbox.KindRevive, AgentID: "agent-orchestrator",
				To: "hub", Text: "which file did you touch?",
			})
		}
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
	})
	if err := New(r).RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	hubs := []contracts.Agent{}
	for _, id := range state.AgentOrder {
		if state.Agents[id].Name == "hub" {
			hubs = append(hubs, state.Agents[id])
		}
	}
	if len(hubs) != 2 {
		t.Fatalf("hub runs = %d, want the original and the revived one: %+v", len(hubs), state.Agents)
	}
	if hubs[0].ID == hubs[1].ID {
		t.Fatalf("the revived run reused the original agent id %q", hubs[0].ID)
	}
	revived := hubs[1]
	if revived.Status != contracts.AgentSucceeded {
		t.Fatalf("revived hub status = %s: %+v", revived.Status, revived)
	}
	if revived.ParentAgentID != "agent-orchestrator" {
		t.Fatalf("revived hub parent = %q", revived.ParentAgentID)
	}
	if !strings.Contains(revived.Task, "hub first run done") || !strings.Contains(revived.Task, "which file did you touch?") {
		t.Fatalf("revived hub task lacks its context:\n%s", revived.Task)
	}
	// The lead sees the revived agent's result under the same shared key.
	if got := state.Shared["result_hub"]; !strings.Contains(got, "REVIVED-ANSWER") {
		t.Fatalf("result_hub = %q, want the revived run's answer", got)
	}
}
