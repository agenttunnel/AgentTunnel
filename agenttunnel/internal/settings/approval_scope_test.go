package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A model name approved for one harness must not be approved for another that
// happens to report the same name.
func TestApprovalIsScopedToHarness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatal(err)
	}
	s.SetHarnessApprovedModels(contracts.HarnessClaude, []string{"shared-model"})

	if _, ok := s.ApprovedModelsFor(contracts.HarnessClaude)["shared-model"]; !ok {
		t.Fatal("claude should approve shared-model")
	}
	if got := s.ApprovedModelsFor(contracts.HarnessCodex); len(got) != 0 {
		t.Fatalf("codex has no allowlist, got %v", got)
	}

	again, err := Open(path, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.ApprovedModelsFor(contracts.HarnessClaude)["shared-model"]; !ok {
		t.Fatal("harness-scoped approval not persisted")
	}
	if _, ok := again.ApprovedModelsFor(contracts.HarnessCodex)["shared-model"]; ok {
		t.Fatal("approval leaked to codex after reopen")
	}
}

// Files written before harness keys carry a flat approvedModels list; each
// model was approved for every harness, so the migration keeps that meaning.
func TestLegacyFlatApprovalsMigrateToEveryHarness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"approvedModels":["legacy-model"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range contracts.AllHarnesses() {
		if _, ok := s.ApprovedModelsFor(h)["legacy-model"]; !ok {
			t.Fatalf("legacy approval missing for %s", h)
		}
	}

	// The next save writes the keyed shape and drops the legacy flat key.
	s.SetAutoMode(true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"approvedModels"`) {
		t.Fatalf("legacy key still written: %s", data)
	}
	if !strings.Contains(string(data), `"approvedByHarness"`) {
		t.Fatalf("keyed approvals not written: %s", data)
	}
}
