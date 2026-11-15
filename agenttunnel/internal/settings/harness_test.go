package settings

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestHarnessEnabledByDefaultAndToggle(t *testing.T) {
	s := New("")

	if !s.HarnessEnabled(contracts.HarnessClaude) {
		t.Fatal("harnesses should be enabled by default")
	}

	s.SetHarnessEnabled(contracts.HarnessClaude, false)
	if s.HarnessEnabled(contracts.HarnessClaude) {
		t.Fatal("claude should be disabled")
	}
	if !s.HarnessEnabled(contracts.HarnessCodex) {
		t.Fatal("disabling one harness must not affect others")
	}

	s.SetHarnessEnabled(contracts.HarnessClaude, true)
	if !s.HarnessEnabled(contracts.HarnessClaude) {
		t.Fatal("claude should be re-enabled")
	}
}
