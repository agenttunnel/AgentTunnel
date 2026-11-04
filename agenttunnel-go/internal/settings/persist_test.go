package settings

import (
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestOpenMissingFileYieldsDefaults(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "nope.json"), "/base")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.Workdir() != "/base" {
		t.Fatalf("workdir = %q, want the default", s.Workdir())
	}
	if s.Path() == "" {
		t.Fatal("expected a config path")
	}
}

func TestSettingsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	s.SetWorkdir("/tmp/repo")
	s.SetAutoMode(true)
	s.SetApprovedModels([]string{"gpt-5", "sonnet"})
	s.SetRoleConfig(contracts.RoleExecutor, RoleConfig{Harness: contracts.HarnessClaude, Model: "sonnet"})
	s.SetHarnessEnabled(contracts.HarnessCodex, false)
	s.AddModel(contracts.HarnessOpenCode, "my/model")

	again, err := Open(path, "ignored")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	if again.Workdir() != "/tmp/repo" {
		t.Errorf("workdir = %q", again.Workdir())
	}
	if !again.AutoMode() {
		t.Error("auto mode not persisted")
	}
	if _, ok := again.ApprovedModels()["gpt-5"]; !ok {
		t.Error("approved models not persisted")
	}
	rc, ok := again.RoleConfig(contracts.RoleExecutor)
	if !ok || rc.Harness != contracts.HarnessClaude || rc.Model != "sonnet" {
		t.Errorf("role override not persisted: %+v ok=%v", rc, ok)
	}
	if again.HarnessEnabled(contracts.HarnessCodex) {
		t.Error("disabled harness not persisted")
	}
	if got := again.CustomModels(contracts.HarnessOpenCode); len(got) != 1 || got[0] != "my/model" {
		t.Errorf("custom models not persisted: %v", got)
	}
}
