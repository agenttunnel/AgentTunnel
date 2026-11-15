package models

import (
	"context"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

type fakeDiscoverer struct {
	found []string
	err   error
}

func (f fakeDiscoverer) Discover(context.Context) ([]string, error) { return f.found, f.err }

func has(list []string, want string) bool {
	for _, m := range list {
		if m == want {
			return true
		}
	}
	return false
}

func TestServiceMergesDiscoveredAndCustom(t *testing.T) {
	reg := providers.NewRegistry()
	reg.RegisterDiscoverer(contracts.HarnessClaude, fakeDiscoverer{found: []string{"discovered-1", "shared-1"}})
	svc := NewService(reg, settings.New(""))

	svc.Add(contracts.HarnessClaude, "my-custom-model")
	svc.Add(contracts.HarnessClaude, "shared-1")
	list := svc.Refresh(context.Background(), contracts.HarnessClaude)

	if !has(list, "discovered-1") || !has(list, "my-custom-model") {
		t.Fatalf("merged list missing entries: %v", list)
	}

	seen := map[string]bool{}
	for _, m := range list {
		if seen[m] {
			t.Fatalf("duplicate %q in %v", m, list)
		}
		seen[m] = true
	}
}

// Nothing is hardcoded: a harness with no discoverer lists only what the user
// added, so the dropdown never offers a model the CLI did not report.
func TestServiceHasNoModelsWithoutDiscoveryOrCustom(t *testing.T) {
	svc := NewService(providers.NewRegistry(), settings.New(""))
	if got := svc.Refresh(context.Background(), contracts.HarnessClaude); len(got) != 0 {
		t.Fatalf("expected no models, got %v", got)
	}
}

// A refresh replaces the discovered list, so models the CLI no longer reports
// drop out of the dropdown.
func TestServiceRefreshReplacesDiscoveredList(t *testing.T) {
	reg := providers.NewRegistry()
	d := &swappableDiscoverer{found: []string{"old-model"}}
	reg.RegisterDiscoverer(contracts.HarnessOpenCode, d)
	svc := NewService(reg, settings.New(""))

	svc.Refresh(context.Background(), contracts.HarnessOpenCode)
	d.found = []string{"new-model"}
	list := svc.Refresh(context.Background(), contracts.HarnessOpenCode)

	if has(list, "old-model") || !has(list, "new-model") {
		t.Fatalf("refresh kept a stale model: %v", list)
	}
}

func TestServiceRecordsDiscoveryError(t *testing.T) {
	reg := providers.NewRegistry()
	reg.RegisterDiscoverer(contracts.HarnessClaude, fakeDiscoverer{err: context.DeadlineExceeded})
	svc := NewService(reg, settings.New(""))

	svc.Refresh(context.Background(), contracts.HarnessClaude)
	if svc.LastError(contracts.HarnessClaude) == "" {
		t.Fatal("expected a recorded discovery error")
	}
}

func TestServicePreservesProviderPrefixedSlugs(t *testing.T) {
	// Discovered slugs keep their provider prefix so the UI can group by it.
	reg := providers.NewRegistry()
	reg.RegisterDiscoverer(contracts.HarnessOpenCode, fakeDiscoverer{found: []string{"opencode-go/deepseek-v4-flash"}})
	svc := NewService(reg, settings.New(""))

	list := svc.Refresh(context.Background(), contracts.HarnessOpenCode)
	if !has(list, "opencode-go/deepseek-v4-flash") {
		t.Fatalf("expected a prefixed slug in %v", list)
	}
}

// swappableDiscoverer reports whatever found currently holds.
type swappableDiscoverer struct{ found []string }

func (s *swappableDiscoverer) Discover(context.Context) ([]string, error) { return s.found, nil }
