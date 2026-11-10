package orchestrator

import (
	"runtime"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Only the lead gets full git. Subagents get the read-only guard env, and the
// guard is skipped where it cannot be built (Windows), so this asserts the
// split on POSIX and the lead exemption everywhere.
func TestSubagentGitEnvConfinesOnlySubagents(t *testing.T) {
	roles := contracts.NewRoleRegistry(contracts.BuiltinRoles()...)
	lead, _ := roles.Get(contracts.RoleOrchestrator)
	if env := subagentGitEnv(lead); len(env) != 0 {
		t.Fatalf("lead must keep full git, got env %v", env)
	}
	executor, _ := roles.Get(contracts.RoleExecutor)
	env := subagentGitEnv(executor)
	if runtime.GOOS == "windows" {
		t.Skip("git guard is POSIX-only; subagent env is empty there")
	}
	if len(env) == 0 {
		t.Skip("git not on PATH; guard cannot be built")
	}
	if env[0] != "AT_GIT_POLICY=readonly" {
		t.Fatalf("subagent env missing read-only policy: %v", env)
	}
}

// The lead's prompt must say it owns commits, and subagent prompts must forbid
// state-changing git whatever their role, including custom roles.
func TestPromptsAssignGitToLead(t *testing.T) {
	roles := contracts.NewRoleRegistry(contracts.BuiltinRoles()...)
	lead, _ := roles.Get(contracts.RoleOrchestrator)
	if !strings.Contains(lead.SystemPrompt, "You own git") {
		t.Fatalf("orchestrator prompt should claim git ownership:\n%s", lead.SystemPrompt)
	}
	custom := contracts.Role{ID: "auditor", SystemPrompt: "You audit."}
	for _, role := range []contracts.Role{custom, mustRole(t, roles, contracts.RoleExecutor)} {
		prompt := BuildSubagentPrompt(role, "task", "", "", false, nil)
		if !strings.Contains(prompt, "Do not run state-changing git") || !strings.Contains(prompt, "the lead commits your work") {
			t.Fatalf("subagent prompt for %s should forbid state-changing git:\n%s", role.ID, prompt)
		}
	}
}

func mustRole(t *testing.T, roles *contracts.RoleRegistry, id contracts.RoleID) contracts.Role {
	t.Helper()
	role, ok := roles.Get(id)
	if !ok {
		t.Fatalf("role %s missing", id)
	}
	return role
}
