package orchestrator

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

func TestOrchestratorRunTurnDispatchesAndReports(t *testing.T) {
	script := `if [ "$AT_PASS" = "0" ]; then
  printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"impl","task":"implement the feature","role":"executor"}'
fi
printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
printf '{"usage":{"input_tokens":1,"output_tokens":1}}\n'`

	r, eng, ctx := newHarness(t, script)
	orch := New(r)
	if err := orch.RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	if state.Agents["agent-orchestrator"].Status != contracts.AgentSucceeded {
		t.Fatalf("orchestrator status = %s", state.Agents["agent-orchestrator"].Status)
	}
	if state.Agents["agent-impl"].Status != contracts.AgentSucceeded {
		t.Fatalf("impl status = %s", state.Agents["agent-impl"].Status)
	}

	found := false
	for _, m := range state.Messages {
		if m.Author == "orchestrator" && m.Text == "report-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("orchestrator final message missing: %+v", state.Messages)
	}

	rollup := state.ThreadUsage()
	if rollup.Orchestrator.Total() == 0 {
		t.Fatal("orchestrator usage not recorded")
	}
	if rollup.Subagents.Total() == 0 {
		t.Fatal("subagent usage not recorded")
	}
}

func TestOrchestratorPromptCarriesContext(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	role, _ := r.roles.Get(contracts.RoleOrchestrator)

	prompt := BuildOrchestratorPrompt(
		role,
		"do the thing",
		"GUIDE-MARKER",
		"## Shared context\n### k\n- [a] v\n",
		"- explore (planner): x [succeeded]\n",
		"",
		0,
		nil,
		"",
	)
	for _, want := range []string{"GUIDE-MARKER", "Shared context", "Agents already dispatched", "do the thing", "$AT_TOOL"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestOrchestratorPromptWarnsOnLaterPasses(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleOrchestrator)
	prompt := BuildOrchestratorPrompt(role, "do the thing", "", "", "- explore (planner): x [succeeded]\n", "", 1, nil, "")
	if !strings.Contains(prompt, "pass 2") || !strings.Contains(prompt, "do not dispatch the same work again") {
		t.Fatalf("later pass should warn against re-dispatching:\n%s", prompt)
	}
}

func TestOrchestratorPromptCarriesSteering(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleOrchestrator)
	prompt := BuildOrchestratorPrompt(role, "do the thing", "", "", "", "", 1, []string{"stop and fix the tests first"}, "")
	if !strings.Contains(prompt, "## Steering") || !strings.Contains(prompt, "stop and fix the tests first") {
		t.Fatalf("steering missing from the prompt:\n%s", prompt)
	}
}

func TestOrchestratorPromptListsPendingSpawnRequests(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleOrchestrator)
	pending := "## Pending spawn requests\n- inner — from outer (agent-outer-1): executor — \"inner work\"\n"
	prompt := BuildOrchestratorPrompt(role, "do the thing", "", "", "", pending, 0, nil, "")
	if !strings.Contains(prompt, "## Pending spawn requests") || !strings.Contains(prompt, "inner") {
		t.Fatalf("pending requests missing from the prompt:\n%s", prompt)
	}

	bare := BuildOrchestratorPrompt(role, "do the thing", "", "", "", "", 0, nil, "")
	if strings.Contains(bare, "Pending spawn") {
		t.Fatalf("no requests should render no section:\n%s", bare)
	}
}

// The full loop, driven by printed `$AT_TOOL` lines: the executor's request
// (pre-seeded as if posted during wave 0 — the daemon tests exercise the real
// HTTP path) pends, surfaces in a later pass prompt, the lead approves it, and
// the child runs parented on the requester.
func TestRunTurnRunsApprovedSpawnProposal(t *testing.T) {
	script := `if [ "$AT_PASS" = "0" ]; then
  printf '%s\n' '{"type":"text","text":"$AT_TOOL tool dispatch --name outer --task \"outer work\"\n"}'
elif [ "$AT_PASS" = "1" ]; then
  printf '%s\n' '{"type":"text","text":"$AT_TOOL tool approve --name inner\n"}'
fi
printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"`
	r, eng, ctx := newHarness(t, script)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Role: contracts.RoleTester, Task: "inner work",
	})

	if err := New(r).RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	inner := state.Agents["agent-inner-2"]
	if inner.Status != contracts.AgentSucceeded {
		t.Fatalf("inner status = %s, agents = %+v", inner.Status, state.Agents)
	}
	if inner.ParentAgentID != "agent-outer-1" {
		t.Fatalf("inner parent = %q, want the requester agent-outer-1", inner.ParentAgentID)
	}
	if inner.Role != contracts.RoleTester || inner.Task != "inner work" {
		t.Fatalf("approved request not preserved: %+v", inner)
	}
	if state.Agents["agent-outer-1"].Status != contracts.AgentSucceeded {
		t.Fatalf("outer status = %s", state.Agents["agent-outer-1"].Status)
	}
	if _, ok := state.Shared["result_inner"]; !ok {
		t.Fatalf("inner findings missing from shared context: %+v", state.Shared)
	}
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("approved request left behind: %+v", got)
	}
}


// The subagent prompt offers gated spawn proposals instead of forbidding
// nesting outright: the proposal wording and the approval gate are both pinned.
func TestSubagentPromptOffersSpawnProposals(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleExecutor)
	prompt := BuildSubagentPrompt(role, "implement X", "", "")
	for _, want := range []string{
		`"$AT_TOOL" tool spawn --role executor --name <short-name>`,
		"only runs after the team lead approves",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("subagent prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Do not spawn more agents") {
		t.Fatalf("subagent prompt should not forbid nesting outright:\n%s", prompt)
	}
}

// The executor role prompt carries the same gated-spawn instruction, since role
// prompts are what custom harnesses see first.
func TestExecutorRolePromptOffersSpawnProposals(t *testing.T) {
	role, ok := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleExecutor)
	if !ok {
		t.Fatal("executor role missing")
	}
	if !strings.Contains(role.SystemPrompt, "tool spawn") ||
		!strings.Contains(role.SystemPrompt, "team lead approves") {
		t.Fatalf("executor role prompt should offer gated spawns:\n%s", role.SystemPrompt)
	}
}
