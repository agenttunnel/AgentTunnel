package orchestrator

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The lead is told how to list and delete other threads, and the prompt says
// it may not delete its own. Subagents never get this section.
func TestOrchestratorPromptDocumentsThreadTools(t *testing.T) {
	lead := BuildOrchestratorPrompt(OrchestratorPrompt{Role: contracts.Role{SystemPrompt: "lead"}, UserMessage: "go"})
	for _, line := range []string{
		`"$AT_TOOL" tool thread-list`,
		`"$AT_TOOL" tool thread-delete --id <thread id> [--remove-worktree]`,
		"You cannot delete this thread",
	} {
		if !strings.Contains(lead, line) {
			t.Fatalf("orchestrator prompt lacks %q:\n%s", line, lead)
		}
	}
	sub := BuildSubagentPrompt(contracts.Role{SystemPrompt: "sub"}, "task", "", "", "", false, nil, nil)
	if strings.Contains(sub, "thread-delete") {
		t.Fatalf("subagent prompt offers thread-delete:\n%s", sub)
	}
}
