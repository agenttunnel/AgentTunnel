package contracts

import (
	"strings"
	"testing"
)

func TestBuiltinRolesMarkRequiredAndExperimental(t *testing.T) {
	byID := map[RoleID]Role{}
	for _, role := range BuiltinRoles() {
		byID[role.ID] = role
	}

	for _, id := range []RoleID{RoleOrchestrator, RoleExecutor, RoleReviewer} {
		role, ok := byID[id]
		if !ok {
			t.Fatalf("builtin %q missing", id)
		}
		if !role.Required {
			t.Fatalf("%q must be required", id)
		}
		if role.Experimental {
			t.Fatalf("%q must not be experimental", id)
		}
	}

	cm, ok := byID[RoleContextManager]
	if !ok {
		t.Fatal("context-manager builtin missing")
	}
	if !cm.Experimental {
		t.Fatal("context-manager must be experimental")
	}
	if cm.Required {
		t.Fatal("context-manager must not be required")
	}
}

func TestBuiltinPromptsAskForDefinitionOfDone(t *testing.T) {
	byID := map[RoleID]Role{}
	for _, role := range BuiltinRoles() {
		byID[role.ID] = role
	}
	for _, id := range []RoleID{RoleExecutor, RoleTester} {
		for _, marker := range []string{"CHECKS:", "CHANGED:", "STATUS:"} {
			if !strings.Contains(byID[id].SystemPrompt, marker) {
				t.Fatalf("%q prompt missing %s", id, marker)
			}
		}
	}
	if p := byID[RoleTester].SystemPrompt; !strings.Contains(p, "never claim success without running something") {
		t.Fatalf("tester prompt must forbid unverified success: %s", p)
	}
	if p := byID[RoleOrchestrator].SystemPrompt; !strings.Contains(p, "dispatch a tester against any code changes") {
		t.Fatalf("orchestrator prompt must require a tester before finishing: %s", p)
	}
}
