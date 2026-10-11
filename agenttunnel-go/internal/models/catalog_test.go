package models

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestCatalogSeedsListinglessHarnesses(t *testing.T) {
	c := Default()
	for _, h := range []contracts.Harness{
		contracts.HarnessClaude,
		contracts.HarnessCodex,
		contracts.HarnessGrok,
	} {
		if len(c.Models(h)) == 0 {
			t.Errorf("expected a fallback seed for %q", h)
		}
	}
}

func TestCatalogIsEmptyForDiscoveryHarnesses(t *testing.T) {
	// These are discovered from their CLI, so they carry no seed — a placeholder
	// like "default" would be a lie.
	c := Default()
	for _, h := range []contracts.Harness{
		contracts.HarnessOpenCode,
		contracts.HarnessPi,
		contracts.HarnessCursor,
		contracts.HarnessCommandCode,
	} {
		if got := c.Models(h); len(got) != 0 {
			t.Errorf("%q should rely on discovery, got %v", h, got)
		}
	}
}

func TestCatalogModelsAreCopiesAndMutable(t *testing.T) {
	c := Default()
	got := c.Models(contracts.HarnessClaude)
	got[0] = "mutated"
	if c.Models(contracts.HarnessClaude)[0] == "mutated" {
		t.Fatal("Models should return a copy")
	}

	c.Set(contracts.HarnessClaude, []string{"only-one"})
	if list := c.Models(contracts.HarnessClaude); len(list) != 1 || list[0] != "only-one" {
		t.Fatalf("Set did not replace: %v", list)
	}
}
