package contracts

import "testing"

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
