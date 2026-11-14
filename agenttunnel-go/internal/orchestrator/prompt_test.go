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

// The agent-logs tool is documented to a lead only when it has subagents
// whose logs it might need to read.
func TestOrchestratorPromptDocumentsAgentLogsWithSubagents(t *testing.T) {
	const line = `"$AT_TOOL" tool agent-logs --to <agent name or id>`
	role := contracts.Role{SystemPrompt: "lead"}
	with := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "go", AgentLogs: true})
	if !strings.Contains(with, "## Subagent logs") || !strings.Contains(with, line) {
		t.Fatalf("lead with subagents lacks the agent-logs section:\n%s", with)
	}
	without := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "go"})
	if strings.Contains(without, "agent-logs") {
		t.Fatalf("lead without subagents should not document agent-logs:\n%s", without)
	}
}

// The revive tool is documented to a lead only when it has subagents it might
// bring back, and its section sits right after the agent-logs one.
func TestOrchestratorPromptDocumentsReviveWithSubagents(t *testing.T) {
	const line = `"$AT_TOOL" tool revive --to <agent name or id> --text "<question>"`
	role := contracts.Role{SystemPrompt: "lead"}
	with := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "go", Revive: true, AgentLogs: true})
	if !strings.Contains(with, "## Reviving subagents") || !strings.Contains(with, line) {
		t.Fatalf("lead with subagents lacks the revive section:\n%s", with)
	}
	if logsAt, reviveAt := strings.Index(with, "## Subagent logs"), strings.Index(with, "## Reviving subagents"); logsAt > reviveAt || reviveAt < 0 {
		t.Fatalf("the revive section must follow the subagent logs section:\n%s", with)
	}
	without := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "go"})
	if strings.Contains(without, "tool revive") {
		t.Fatalf("lead without subagents should not document revive:\n%s", without)
	}
}

// The lead's --role choices and disabled-role line come from the thread's
// usable roles, not a hardcoded list.
func TestOrchestratorPromptRoleChoices(t *testing.T) {
	tests := []struct {
		name          string
		roleChoices   string
		disabledRoles string
		wantFlag      string
		wantDisabled  string
	}{
		{
			name:        "only usable roles are offered",
			roleChoices: "planner|tester|reviewer",
			wantFlag:    "--role planner|tester|reviewer to pick the role",
		},
		{
			name:          "disabled roles get one warning line",
			roleChoices:   "planner|reviewer",
			disabledRoles: "tester, context-manager",
			wantFlag:      "--role planner|reviewer to pick the role",
			wantDisabled:  "Disabled roles (do not dispatch): tester, context-manager.",
		},
		{
			name:     "empty choices fall back to executor",
			wantFlag: "--role executor to pick the role",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := BuildOrchestratorPrompt(OrchestratorPrompt{
				Role: contracts.Role{SystemPrompt: "lead"}, UserMessage: "go",
				RoleChoices: tt.roleChoices, DisabledRoles: tt.disabledRoles,
			})
			if !strings.Contains(prompt, tt.wantFlag) {
				t.Fatalf("prompt lacks %q:\n%s", tt.wantFlag, prompt)
			}
			if strings.Contains(prompt, "planner|executor|tester|reviewer") {
				t.Fatalf("prompt kept the hardcoded role list:\n%s", prompt)
			}
			if tt.wantDisabled == "" {
				if strings.Contains(prompt, "Disabled roles") {
					t.Fatalf("no disabled roles should render no line:\n%s", prompt)
				}
				return
			}
			if n := strings.Count(prompt, tt.wantDisabled); n != 1 {
				t.Fatalf("disabled line appears %d times, want 1:\n%s", n, prompt)
			}
		})
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

// The wildcard offer is documented to the lead only while experimental roles
// are on, so a lead never sees a role it may not use.
func TestOrchestratorPromptWildcardOffer(t *testing.T) {
	const line = `--role wildcard --name <short-name> --brief "<agent definition>"`
	role := contracts.Role{SystemPrompt: "lead"}
	on := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "go", Wildcard: true})
	if !strings.Contains(on, line) {
		t.Fatalf("lead with experimental roles lacks the wildcard offer:\n%s", on)
	}
	off := BuildOrchestratorPrompt(OrchestratorPrompt{Role: role, UserMessage: "go"})
	if strings.Contains(off, "wildcard") {
		t.Fatalf("lead without experimental roles should not see wildcard:\n%s", off)
	}
}
