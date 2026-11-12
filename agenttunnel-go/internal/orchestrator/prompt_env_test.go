package orchestrator

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The lead is told how to read, write and run commands in the worktree; a
// subagent is not, because its calls to those kinds are refused.
func TestOrchestratorPromptDocumentsEnvironmentTools(t *testing.T) {
	lead := BuildOrchestratorPrompt(OrchestratorPrompt{Role: contracts.Role{SystemPrompt: "lead"}, UserMessage: "go"})
	for _, line := range []string{
		`"$AT_TOOL" tool read-file --path <path>`,
		`"$AT_TOOL" tool write-file --path <path> --text "<content>"`,
		`"$AT_TOOL" tool shell --command "<command>" [--cwd <dir>] [--timeout <seconds>]`,
	} {
		if !strings.Contains(lead, line) {
			t.Fatalf("orchestrator prompt lacks %q:\n%s", line, lead)
		}
	}
	sub := BuildSubagentPrompt(contracts.Role{SystemPrompt: "sub"}, "task", "", "", "", false, nil, nil)
	if strings.Contains(sub, "tool shell") || strings.Contains(sub, "tool read-file") {
		t.Fatalf("subagent prompt offers environment tools:\n%s", sub)
	}
}
