package orchestrator

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Every agent is told about the ask tool, whether or not the peer mailbox is on:
// a blocked agent needs the human either way.
func TestPromptsDocumentTheAskTool(t *testing.T) {
	const line = `"$AT_TOOL" tool ask --kind choice|multi|text`
	for _, mailbox := range []bool{false, true} {
		sub := BuildSubagentPrompt(contracts.Role{SystemPrompt: "role"}, "task", "", "", "", mailbox, nil, nil)
		if !strings.Contains(sub, line) {
			t.Fatalf("subagent prompt (peer mailbox %v) lacks the ask line:\n%s", mailbox, sub)
		}
		lead := BuildOrchestratorPrompt(OrchestratorPrompt{Role: contracts.Role{SystemPrompt: "lead"}, UserMessage: "go", PeerMailbox: mailbox})
		if !strings.Contains(lead, line) {
			t.Fatalf("orchestrator prompt (peer mailbox %v) lacks the ask line:\n%s", mailbox, lead)
		}
	}
}

// The steer tool is documented to a lead only when it has subagents to steer.
func TestOrchestratorPromptDocumentsSteerWithSubagents(t *testing.T) {
	const line = `"$AT_TOOL" tool steer --to <agent name>`
	role := contracts.Role{SystemPrompt: "lead"}
	with := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "go", Steer: true})
	if !strings.Contains(with, line) {
		t.Fatalf("lead with subagents lacks the steer line:\n%s", with)
	}
	without := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "go"})
	if strings.Contains(without, "tool steer") {
		t.Fatalf("lead without subagents should not document steer:\n%s", without)
	}
}

// Steers queued for a subagent render in their own labelled block, and only
// when there are some.
func TestSubagentPromptCarriesSteering(t *testing.T) {
	role := contracts.Role{SystemPrompt: "role"}
	prompt := BuildSubagentPrompt(role, "task", "", "", "", false, nil, []string{"use the smaller fixture"})
	if !strings.Contains(prompt, "## Steering from the team lead") || !strings.Contains(prompt, "- use the smaller fixture") {
		t.Fatalf("steering missing from the subagent prompt:\n%s", prompt)
	}
	if plain := BuildSubagentPrompt(role, "task", "", "", "", false, nil, nil); strings.Contains(plain, "Steering from the team lead") {
		t.Fatalf("no steers should render no section:\n%s", plain)
	}
}
