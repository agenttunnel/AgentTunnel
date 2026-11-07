package orchestrator

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
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
		0,
		nil,
		"",
		false,
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
	prompt := BuildOrchestratorPrompt(role, "do the thing", "", "", "- explore (planner): x [succeeded]\n", 1, nil, "", false, "")
	if !strings.Contains(prompt, "pass 2") || !strings.Contains(prompt, "do not dispatch the same work again") {
		t.Fatalf("later pass should warn against re-dispatching:\n%s", prompt)
	}
}

func TestOrchestratorPromptCarriesSteering(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleOrchestrator)
	prompt := BuildOrchestratorPrompt(role, "do the thing", "", "", "", 1, []string{"stop and fix the tests first"}, "", false, "")
	if !strings.Contains(prompt, "## Steering") || !strings.Contains(prompt, "stop and fix the tests first") {
		t.Fatalf("steering missing from the prompt:\n%s", prompt)
	}
}

func TestSubagentPromptForbidsNesting(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleExecutor)
	prompt := BuildSubagentPrompt(role, "implement X", "", "", false, nil)
	if !strings.Contains(prompt, "Do not spawn more agents") {
		t.Fatalf("subagent prompt should forbid nested agents:\n%s", prompt)
	}
}

func TestSubagentPromptDocumentsMessageToolOnlyWhenEnabled(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleExecutor)
	if prompt := BuildSubagentPrompt(role, "implement X", "", "", false, nil); strings.Contains(prompt, "tool message") {
		t.Fatalf("message tool must stay undocumented while the peer mailbox is off:\n%s", prompt)
	}
	prompt := BuildSubagentPrompt(role, "implement X", "", "", true, nil)
	if !strings.Contains(prompt, `tool message --to <agent name or 'orchestrator'>`) {
		t.Fatalf("message tool missing from the prompt:\n%s", prompt)
	}
}

func TestSubagentPromptCarriesTeammateMessages(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleExecutor)
	prompt := BuildSubagentPrompt(role, "implement X", "", "", true, []contracts.MailboxPosted{
		{From: "agent-researcher", To: "agent-impl", Kind: "message", Body: "the flaky test is payment.e2e"},
	})
	if !strings.Contains(prompt, "## Messages from teammates") ||
		!strings.Contains(prompt, "from agent-researcher: the flaky test is payment.e2e") {
		t.Fatalf("teammate messages missing from the prompt:\n%s", prompt)
	}
	if plain := BuildSubagentPrompt(role, "implement X", "", "", true, nil); strings.Contains(plain, "Messages from teammates") {
		t.Fatalf("no teammates should render no section:\n%s", plain)
	}
}

func TestOrchestratorPromptDocumentsMessageToolOnlyWhenEnabled(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleOrchestrator)
	if prompt := BuildOrchestratorPrompt(role, "do the thing", "", "", "", 1, nil, "", false, ""); strings.Contains(prompt, "tool message") {
		t.Fatalf("message tool must stay undocumented while the peer mailbox is off:\n%s", prompt)
	}
	prompt := BuildOrchestratorPrompt(role, "do the thing", "", "", "", 1, nil, "", true, "")
	if !strings.Contains(prompt, `tool message --to <agent name>`) {
		t.Fatalf("message tool missing from the prompt:\n%s", prompt)
	}
}

func TestOrchestratorPromptCarriesPeerMessages(t *testing.T) {
	role, _ := contracts.NewRoleRegistry(contracts.BuiltinRoles()...).Get(contracts.RoleOrchestrator)
	prompt := BuildOrchestratorPrompt(role, "do the thing", "", "", "", 1, nil,
		"- from agent-researcher: the flaky test is payment.e2e\n- for reviewer from agent-researcher: check the retry loop\n", true, "")
	if !strings.Contains(prompt, "## Peer messages") ||
		!strings.Contains(prompt, "- from agent-researcher: the flaky test is payment.e2e") ||
		!strings.Contains(prompt, "- for reviewer from agent-researcher: check the retry loop") {
		t.Fatalf("peer messages missing from the prompt:\n%s", prompt)
	}
	if plain := BuildOrchestratorPrompt(role, "do the thing", "", "", "", 1, nil, "", false, ""); strings.Contains(plain, "Peer messages") {
		t.Fatalf("empty peer messages should render no section:\n%s", plain)
	}
}
