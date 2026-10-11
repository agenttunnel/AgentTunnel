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

func TestServiceMergesCatalogCustomAndDiscovered(t *testing.T) {
	reg := providers.NewRegistry()
	reg.RegisterDiscoverer(contracts.HarnessClaude, fakeDiscoverer{found: []string{"discovered-1", "claude-sonnet-4-5"}})
	store := settings.New("")
	svc := NewService(Default(), reg, store)

	if len(svc.List(contracts.HarnessClaude)) == 0 {
		t.Fatal("catalog should provide a base list")
	}

	svc.Add(contracts.HarnessClaude, "my-custom-model")
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

func TestServiceRecordsDiscoveryError(t *testing.T) {
	reg := providers.NewRegistry()
	reg.RegisterDiscoverer(contracts.HarnessClaude, fakeDiscoverer{err: context.DeadlineExceeded})
	svc := NewService(Default(), reg, settings.New(""))

	svc.Refresh(context.Background(), contracts.HarnessClaude)
	if svc.LastError(contracts.HarnessClaude) == "" {
		t.Fatal("expected a recorded discovery error")
	}
}

func TestServicePreservesProviderPrefixedSlugs(t *testing.T) {
	// Discovered slugs keep their provider prefix so the UI can group by it.
	reg := providers.NewRegistry()
	reg.RegisterDiscoverer(contracts.HarnessOpenCode, fakeDiscoverer{found: []string{"opencode-go/deepseek-v4-flash"}})
	svc := NewService(Default(), reg, settings.New(""))

	list := svc.Refresh(context.Background(), contracts.HarnessOpenCode)
	if !has(list, "opencode-go/deepseek-v4-flash") {
		t.Fatalf("expected a prefixed slug in %v", list)
	}
}
