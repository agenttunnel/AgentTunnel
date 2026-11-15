package settings

import (
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The master runtime and reporter flag persist like every other setting, and a
// zero master runtime removes the override instead of pinning empty strings.
func TestMasterSettingsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(path, "/tmp/work")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	s.SetMasterRuntime(RoleConfig{Harness: contracts.HarnessClaude, Model: "claude-sonnet-4-5", Variant: "thinking"})
	s.SetMasterReporter(true)

	again, err := Open(path, "ignored")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rc, ok := again.MasterRuntime()
	if !ok || rc.Harness != contracts.HarnessClaude || rc.Model != "claude-sonnet-4-5" || rc.Variant != "thinking" {
		t.Fatalf("master runtime not persisted: %+v ok=%v", rc, ok)
	}
	if !again.MasterReporter() {
		t.Fatal("reporter flag not persisted")
	}

	again.SetMasterRuntime(RoleConfig{})
	if _, ok := again.MasterRuntime(); ok {
		t.Fatal("zero master runtime should remove the override")
	}
	again.SetMasterReporter(false)
	if again.MasterReporter() {
		t.Fatal("reporter should turn off")
	}

	final, err := Open(path, "ignored")
	if err != nil {
		t.Fatalf("reopen after clear: %v", err)
	}
	if _, ok := final.MasterRuntime(); ok {
		t.Fatal("cleared master runtime resurfaced on reopen")
	}
	if final.MasterReporter() {
		t.Fatal("cleared reporter resurfaced on reopen")
	}
}
