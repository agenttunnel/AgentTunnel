package providers

import (
	"context"
	"sync"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// LimitsFetcher reads one harness's subscription usage. It returns the windows
// it found, or a non-empty reason when none are available. A reason is shown
// to the user verbatim, so it must never contain credentials.
type LimitsFetcher interface {
	Harness() contracts.Harness
	Fetch(ctx context.Context) (windows []contracts.LimitWindow, reason string)
}

const (
	// limitsTTL is how long a successful read is reused.
	limitsTTL = 60 * time.Second
	// limitsFailTTL is how long a failed read is reused, so a bad or expired
	// token is not retried on every poll.
	limitsFailTTL = 30 * time.Second
)

// LimitsProvider caches subscription usage per harness. Concurrent callers
// share one in-flight fetch for a harness, and each fetcher's failure stays
// local to its own harness.
type LimitsProvider struct {
	fetchers []LimitsFetcher
	entries  []*limitsEntry
	now      func() time.Time
}

type limitsEntry struct {
	mu      sync.Mutex
	loaded  bool
	expires time.Time
	limits  contracts.HarnessLimits
}

// NewLimitsProvider builds a provider over the given fetchers, in report order.
func NewLimitsProvider(fetchers ...LimitsFetcher) *LimitsProvider {
	entries := make([]*limitsEntry, len(fetchers))
	for i := range entries {
		entries[i] = &limitsEntry{}
	}
	return &LimitsProvider{fetchers: fetchers, entries: entries, now: time.Now}
}

// Get returns every harness's limits, in fetcher order. force bypasses the
// cache. A fetcher that fails is reported as unavailable, never as an error.
func (p *LimitsProvider) Get(ctx context.Context, force bool) []contracts.HarnessLimits {
	out := make([]contracts.HarnessLimits, len(p.fetchers))
	var wg sync.WaitGroup
	for i := range p.fetchers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = p.entries[i].get(ctx, p.fetchers[i], force, p.now)
		}()
	}
	wg.Wait()
	return out
}

func (e *limitsEntry) get(ctx context.Context, f LimitsFetcher, force bool, now func() time.Time) contracts.HarnessLimits {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !force && e.loaded && now().Before(e.expires) {
		return e.limits
	}
	at := now()
	windows, reason := f.Fetch(ctx)
	limits := contracts.HarnessLimits{
		Harness:   f.Harness(),
		Available: reason == "",
		Reason:    reason,
		Windows:   windows,
		FetchedAt: at,
	}
	// A failure after the caller's ctx was cancelled (e.g. the client
	// disconnected) says nothing about the harness, so it must not be
	// cached for everyone else.
	if reason != "" && ctx.Err() != nil {
		return limits
	}
	e.limits = limits
	ttl := limitsTTL
	if reason != "" {
		ttl = limitsFailTTL
	}
	e.loaded, e.expires = true, at.Add(ttl)
	return e.limits
}
