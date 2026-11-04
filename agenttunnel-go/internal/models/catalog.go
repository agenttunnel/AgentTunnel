// Package models holds the model catalog: which models each harness can run.
//
// The seed list is a fallback for harnesses that cannot enumerate their own
// models. Harnesses with a real listing command (opencode, pi) are discovered
// live instead, so they carry no seed.
package models

import (
	"context"
	"sort"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ModelLister is an optional interface a provider Driver may implement to report
// the models it actually supports (e.g. by querying the local CLI).
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// Catalog maps a harness to its fallback models.
type Catalog struct {
	mu        sync.RWMutex
	byHarness map[contracts.Harness][]string
}

// Default returns a catalog seeded for the harnesses with no listing command:
// codex's list is behind its app-server handshake, and claude/cursor/grok report
// through their own SDKs or ACP.
func Default() *Catalog {
	return &Catalog{byHarness: map[contracts.Harness][]string{
		contracts.HarnessClaude: {
			"claude-sonnet-4-5",
			"claude-opus-4-1",
			"claude-haiku-4-5",
		},
		contracts.HarnessCodex: {
			"gpt-5-codex",
			"gpt-5",
			"o3",
		},
		contracts.HarnessGrok: {
			"grok-4",
			"grok-4-fast",
			"grok-code-fast-1",
		},
	}}
}

// Models returns a copy of a harness's fallback model list.
func (c *Catalog) Models(h contracts.Harness) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.byHarness[h]...)
}

// Set replaces a harness's model list.
func (c *Catalog) Set(h contracts.Harness, list []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byHarness[h] = append([]string(nil), list...)
}

// All returns every harness's models, keyed by harness id.
func (c *Catalog) All() map[contracts.Harness][]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[contracts.Harness][]string, len(c.byHarness))
	for h, list := range c.byHarness {
		out[h] = append([]string(nil), list...)
	}
	return out
}

// HarnessIDs returns the harnesses the catalog knows about, sorted.
func (c *Catalog) HarnessIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.byHarness))
	for h := range c.byHarness {
		out = append(out, string(h))
	}
	sort.Strings(out)
	return out
}
