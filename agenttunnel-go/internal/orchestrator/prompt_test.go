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
		sub := BuildSubagentPrompt(contracts.Role{SystemPrompt: "role"}, "task", "", "", mailbox, nil)
		if !strings.Contains(sub, line) {
			t.Fatalf("subagent prompt (peer mailbox %v) lacks the ask line:\n%s", mailbox, sub)
		}
		lead := BuildOrchestratorPrompt(OrchestratorPrompt{Role: contracts.Role{SystemPrompt: "lead"}, UserMessage: "go", PeerMailbox: mailbox})
		if !strings.Contains(lead, line) {
			t.Fatalf("orchestrator prompt (peer mailbox %v) lacks the ask line:\n%s", mailbox, lead)
		}
	}
}
