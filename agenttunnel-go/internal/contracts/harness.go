package contracts

import "slices"

// Harness identifies a provider CLI/runtime that can back an agent. Any harness
// can serve any role; the orchestrator and each role choose independently.
type Harness string

const (
	HarnessOpenCode    Harness = "opencode"
	HarnessCodex       Harness = "codex"
	HarnessClaude      Harness = "claude"
	HarnessCursor      Harness = "cursor"
	HarnessGrok        Harness = "grok"
	HarnessPi          Harness = "pi"
	HarnessCommandCode Harness = "commandcode"
)

// AllHarnesses lists every supported provider runtime in stable order.
func AllHarnesses() []Harness {
	return []Harness{
		HarnessOpenCode,
		HarnessCodex,
		HarnessClaude,
		HarnessCursor,
		HarnessGrok,
		HarnessPi,
		HarnessCommandCode,
	}
}

// Valid reports whether the harness is one we ship an adapter for.
func (h Harness) Valid() bool { return slices.Contains(AllHarnesses(), h) }

func (h Harness) String() string { return string(h) }
