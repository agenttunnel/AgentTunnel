package models

import (
	"context"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// Service merges three sources into one dynamic per-harness model list: live CLI
// discovery, the seed catalog, and models the user added by hand.
type Service struct {
	catalog  *Catalog
	registry *providers.Registry
	settings *settings.Store

	mu         sync.Mutex
	discovered map[contracts.Harness][]string
	lastErr    map[contracts.Harness]string
}

// NewService wires a model service.
func NewService(catalog *Catalog, registry *providers.Registry, store *settings.Store) *Service {
	return &Service{
		catalog:    catalog,
		registry:   registry,
		settings:   store,
		discovered: map[contracts.Harness][]string{},
		lastErr:    map[contracts.Harness]string{},
	}
}

// List returns the merged model list for a harness without running discovery.
func (s *Service) List(h contracts.Harness) []string { return s.merge(h) }

// Refresh runs live discovery for a harness, then returns the merged list.
func (s *Service) Refresh(ctx context.Context, h contracts.Harness) []string {
	if s.registry != nil {
		if disc, ok := s.registry.Discoverer(h); ok {
			found, err := disc.Discover(ctx)
			s.mu.Lock()
			if err != nil {
				s.lastErr[h] = err.Error()
			} else {
				s.discovered[h] = found
				delete(s.lastErr, h)
			}
			s.mu.Unlock()
		}
	}
	return s.merge(h)
}

// Add records a user-provided model for a harness.
func (s *Service) Add(h contracts.Harness, model string) {
	if s.settings != nil {
		s.settings.AddModel(h, model)
	}
}

// LastError returns the most recent discovery error for a harness, if any.
func (s *Service) LastError(h contracts.Harness) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr[h]
}

func (s *Service) merge(h contracts.Harness) []string {
	seen := map[string]bool{}
	var out []string
	add := func(list []string) {
		for _, m := range list {
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}

	s.mu.Lock()
	discovered := append([]string(nil), s.discovered[h]...)
	s.mu.Unlock()
	add(discovered)

	if s.catalog != nil {
		add(s.catalog.Models(h))
	}
	if s.settings != nil {
		add(s.settings.CustomModels(h))
	}
	return out
}
