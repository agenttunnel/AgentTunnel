package providers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// fakeLimitsFetcher counts fetches. When gate is set, Fetch reports on started
// and then blocks until gate closes, so a test can overlap callers without sleeping.
type fakeLimitsFetcher struct {
	harness contracts.Harness
	windows []contracts.LimitWindow
	reason  string
	gate    chan struct{}
	started chan struct{}

	mu    sync.Mutex
	calls int
}

func (f *fakeLimitsFetcher) Harness() contracts.Harness { return f.harness }

func (f *fakeLimitsFetcher) Fetch(context.Context) ([]contracts.LimitWindow, string) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.gate != nil {
		<-f.gate
	}
	return f.windows, f.reason
}

func (f *fakeLimitsFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newTestLimitsProvider wires a provider to a clock the test moves by hand.
func newTestLimitsProvider(fetchers ...LimitsFetcher) (*LimitsProvider, *time.Time) {
	clock := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	p := NewLimitsProvider(fetchers...)
	p.now = func() time.Time { return clock }
	return p, &clock
}

func TestLimitsProviderCachesWithinTTL(t *testing.T) {
	f := &fakeLimitsFetcher{harness: contracts.HarnessClaude, windows: []contracts.LimitWindow{{ID: "five_hour"}}}
	p, clock := newTestLimitsProvider(f)
	ctx := context.Background()

	p.Get(ctx, false)
	got := p.Get(ctx, false)
	if f.count() != 1 {
		t.Fatalf("two reads inside the TTL made %d fetches, want 1", f.count())
	}
	if !got[0].Available || len(got[0].Windows) != 1 {
		t.Fatalf("cached read = %#v", got[0])
	}

	*clock = clock.Add(limitsTTL)
	p.Get(ctx, false)
	if f.count() != 2 {
		t.Fatalf("read after the TTL made %d fetches, want 2", f.count())
	}
}

func TestLimitsProviderForceBypassesCache(t *testing.T) {
	f := &fakeLimitsFetcher{harness: contracts.HarnessClaude}
	p, _ := newTestLimitsProvider(f)
	ctx := context.Background()

	p.Get(ctx, false)
	p.Get(ctx, true)
	if f.count() != 2 {
		t.Fatalf("force read made %d fetches total, want 2", f.count())
	}
}

func TestLimitsProviderFailureIsCachedShorter(t *testing.T) {
	f := &fakeLimitsFetcher{harness: contracts.HarnessClaude, reason: "token expired"}
	p, clock := newTestLimitsProvider(f)
	ctx := context.Background()

	got := p.Get(ctx, false)
	if got[0].Available || got[0].Reason != "token expired" {
		t.Fatalf("failed fetch = %#v, want unavailable with the reason", got[0])
	}

	*clock = clock.Add(limitsFailTTL - time.Second)
	p.Get(ctx, false)
	if f.count() != 1 {
		t.Fatalf("failure retried before its short TTL: %d fetches", f.count())
	}

	*clock = clock.Add(time.Second)
	p.Get(ctx, false)
	if f.count() != 2 {
		t.Fatalf("failure not retried after its short TTL: %d fetches", f.count())
	}
}

// cancelingLimitsFetcher cancels the first caller's context from inside Fetch
// and fails, like a client disconnecting mid-fetch. Later calls succeed.
type cancelingLimitsFetcher struct {
	cancel context.CancelFunc
	calls  int
}

func (f *cancelingLimitsFetcher) Harness() contracts.Harness { return contracts.HarnessClaude }

func (f *cancelingLimitsFetcher) Fetch(context.Context) ([]contracts.LimitWindow, string) {
	f.calls++
	if f.calls == 1 {
		f.cancel()
		return nil, "usage request failed"
	}
	return []contracts.LimitWindow{{ID: "five_hour"}}, ""
}

func TestLimitsProviderDoesNotCacheFailureFromCancelledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &cancelingLimitsFetcher{cancel: cancel}
	p, _ := newTestLimitsProvider(f)

	first := p.Get(ctx, false)
	if first[0].Available {
		t.Fatalf("cancelled caller got %#v, want the failure it observed", first[0])
	}

	got := p.Get(context.Background(), false)
	if f.calls != 2 {
		t.Fatalf("live caller after a cancelled failure made %d fetches total, want 2", f.calls)
	}
	if !got[0].Available || len(got[0].Windows) != 1 {
		t.Fatalf("live caller got %#v, want the fresh success", got[0])
	}
}

func TestLimitsProviderSharesOneInFlightFetch(t *testing.T) {
	f := &fakeLimitsFetcher{
		harness: contracts.HarnessClaude,
		windows: []contracts.LimitWindow{{ID: "weekly"}},
		gate:    make(chan struct{}),
		started: make(chan struct{}, 1),
	}
	p, _ := newTestLimitsProvider(f)
	ctx := context.Background()

	const callers = 4
	var wg sync.WaitGroup
	results := make([][]contracts.HarnessLimits, callers)
	for i := range callers {
		wg.Go(func() { results[i] = p.Get(ctx, false) })
	}
	<-f.started
	close(f.gate)
	wg.Wait()

	if f.count() != 1 {
		t.Fatalf("%d concurrent callers made %d fetches, want 1", callers, f.count())
	}
	for i, r := range results {
		if len(r) != 1 || !r[0].Available || r[0].Windows[0].ID != "weekly" {
			t.Fatalf("caller %d got %#v", i, r)
		}
	}
}

func TestLimitsProviderIsolatesHarnessFailures(t *testing.T) {
	ok := &fakeLimitsFetcher{harness: contracts.HarnessCodex, windows: []contracts.LimitWindow{{ID: "five_hour"}}}
	bad := &fakeLimitsFetcher{harness: contracts.HarnessClaude, reason: "not signed in to Claude"}
	p, _ := newTestLimitsProvider(bad, ok)

	got := p.Get(context.Background(), false)
	if len(got) != 2 {
		t.Fatalf("got %d harnesses, want 2", len(got))
	}
	if got[0].Harness != contracts.HarnessClaude || got[0].Available {
		t.Fatalf("claude = %#v, want unavailable and first in fetcher order", got[0])
	}
	if got[1].Harness != contracts.HarnessCodex || !got[1].Available {
		t.Fatalf("codex = %#v, want available", got[1])
	}
}

func TestLimitsProviderWithNoFetchersReturnsEmpty(t *testing.T) {
	p, _ := newTestLimitsProvider()
	if got := p.Get(context.Background(), false); got == nil || len(got) != 0 {
		t.Fatalf("Get() = %#v, want an empty non-nil slice", got)
	}
}
