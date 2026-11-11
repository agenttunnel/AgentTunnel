package settings

import (
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestRoleEnabledByDefaultAndToggle(t *testing.T) {
	s := New("")

	if !s.RoleEnabled(contracts.RolePlanner) {
		t.Fatal("roles should be enabled by default")
	}

	s.SetRoleEnabled(contracts.RolePlanner, false)
	if s.RoleEnabled(contracts.RolePlanner) {
		t.Fatal("planner should be disabled")
	}
	if !s.RoleEnabled(contracts.RoleTester) {
		t.Fatal("disabling one role must not affect others")
	}

	s.SetRoleEnabled(contracts.RolePlanner, true)
	if !s.RoleEnabled(contracts.RolePlanner) {
		t.Fatal("planner should be re-enabled")
	}
}

func TestDisabledRolesPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.SetRoleEnabled(contracts.RoleFacilitator, false)

	reopened, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.RoleEnabled(contracts.RoleFacilitator) {
		t.Fatal("disabled role did not persist across reopen")
	}
	if !reopened.RoleEnabled(contracts.RolePlanner) {
		t.Fatal("untouched role should stay enabled after reopen")
	}

	reopened.SetRoleEnabled(contracts.RoleFacilitator, true)
	again, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatalf("reopen again: %v", err)
	}
	if !again.RoleEnabled(contracts.RoleFacilitator) {
		t.Fatal("re-enabled role did not persist across reopen")
	}
}

func TestExperimentalRolesFlagPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.ExperimentalRoles() {
		t.Fatal("experimental roles should be off by default")
	}

	s.SetExperimentalRoles(true)
	reopened, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened.ExperimentalRoles() {
		t.Fatal("experimental flag did not persist across reopen")
	}

	reopened.SetExperimentalRoles(false)
	again, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatalf("reopen again: %v", err)
	}
	if again.ExperimentalRoles() {
		t.Fatal("cleared experimental flag did not persist across reopen")
	}
}
