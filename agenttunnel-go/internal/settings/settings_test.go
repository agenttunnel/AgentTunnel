package settings

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestStoreDefaultsAndMutations(t *testing.T) {
	s := New("/tmp/repo")

	if s.Workdir() != "/tmp/repo" || s.AutoMode() {
		t.Fatalf("unexpected defaults: workdir=%q auto=%v", s.Workdir(), s.AutoMode())
	}
	if len(s.ApprovedModels()) != 0 {
		t.Fatal("approved models should start empty")
	}

	s.SetWorkdir("  /tmp/other  ")
	if s.Workdir() != "/tmp/other" {
		t.Fatalf("workdir = %q, want trimmed", s.Workdir())
	}

	s.SetAutoMode(true)
	if !s.AutoMode() {
		t.Fatal("auto mode should be on")
	}

	s.SetApprovedModels([]string{"gpt-5", " ", "sonnet"})
	approved := s.ApprovedModels()
	if len(approved) != 2 {
		t.Fatalf("approved = %v", approved)
	}
	if _, ok := approved["gpt-5"]; !ok {
		t.Fatal("gpt-5 missing")
	}
}

func TestStoreRoleOverride(t *testing.T) {
	s := New("")
	if _, ok := s.RoleConfig(contracts.RoleExecutor); ok {
		t.Fatal("no override should exist initially")
	}
	s.SetRoleConfig(contracts.RoleExecutor, RoleConfig{Harness: contracts.HarnessClaude, Model: "sonnet"})
	cfg, ok := s.RoleConfig(contracts.RoleExecutor)
	if !ok || cfg.Harness != contracts.HarnessClaude || cfg.Model != "sonnet" {
		t.Fatalf("role override = %+v ok=%v", cfg, ok)
	}
}
