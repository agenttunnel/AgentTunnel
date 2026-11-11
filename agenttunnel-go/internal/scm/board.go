package scm

import (
	"context"
	"sync"
	"time"
)

const (
	// maxConcurrentRefreshes caps how many fetches (each git + gh process
	// chain) run at once. Opening the board with N worktrees queues the rest.
	maxConcurrentRefreshes = 4

	// failedTTL replaces the normal TTL when the forge cannot answer (no CLI,
	// not logged in, not a forge remote, or the CLI errored). Re-running git
	// every minute per thread buys nothing until the user fixes something.
	failedTTL = 10 * time.Minute

	// idleEvict drops entries no Peek has asked for this long, so deleted
	// worktrees do not accumulate for the life of the daemon.
	idleEvict = 10 * time.Minute
)

// Board caches forge snapshots per working directory so the thread board can
// read PR state without waiting on gh. Peek never blocks: a miss or a stale
// entry answers with what it has and starts one background fetch.
type Board = cache[Snapshot]

// DirtyBoard caches whether each working directory has uncommitted changes.
// It is a sibling of Board with a shorter TTL, because git status is cheap and
// the board's dirty column should track edits as the poll sees them.
type DirtyBoard = cache[bool]

// cache is the shared non-blocking, deduped, concurrency-capped TTL cache keyed by directory.
type cache[T any] struct {
	fetch   func(ctx context.Context, dir string) T
	freshAt func(T) time.Duration
	now     func() time.Time
	spawn   func(func())
	slots   chan struct{}

	mu      sync.Mutex
	entries map[string]*entry[T]
}

type entry[T any] struct {
	val      T
	at       time.Time // when the last fetch landed
	seen     time.Time // when a Peek last asked for this directory
	ok       bool
	inflight bool
}

// NewBoard caches fetch results for ttl. Refreshes run on their own goroutine;
// fetch is bounded by fetchTimeout, so a hung gh never pins an entry.
func NewBoard(fetch func(ctx context.Context, dir string) Snapshot, ttl time.Duration) *Board {
	return newBoard(fetch, ttl, time.Now, func(f func()) { go f() })
}

func newBoard(fetch func(ctx context.Context, dir string) Snapshot, ttl time.Duration, now func() time.Time, spawn func(func())) *Board {
	return newCache(fetch, func(s Snapshot) time.Duration {
		switch s.Status {
		case StatusUnavailable, StatusUnauthenticated, StatusUnsupported, StatusError:
			return failedTTL
		}
		return ttl
	}, now, spawn)
}

// NewDirtyBoard caches Dirty results for ttl, on the same goroutine model as NewBoard.
func NewDirtyBoard(fetch func(ctx context.Context, dir string) bool, ttl time.Duration) *DirtyBoard {
	return newCache(fetch, func(bool) time.Duration { return ttl }, time.Now, func(f func()) { go f() })
}

func newCache[T any](fetch func(ctx context.Context, dir string) T, freshAt func(T) time.Duration, now func() time.Time, spawn func(func())) *cache[T] {
	return &cache[T]{
		fetch:   fetch,
		freshAt: freshAt,
		now:     now,
		spawn:   spawn,
		slots:   make(chan struct{}, maxConcurrentRefreshes),
		entries: map[string]*entry[T]{},
	}
}

// Peek returns the last value for dir. ok is false until the first fetch
// lands, so callers treat the directory as having no value yet.
func (c *cache[T]) Peek(dir string) (val T, ok bool) {
	now := c.now()
	c.mu.Lock()
	c.evictIdle(now)
	e := c.entries[dir]
	if e == nil {
		e = &entry[T]{}
		c.entries[dir] = e
	}
	e.seen = now
	start := !e.inflight && (!e.ok || now.Sub(e.at) >= c.freshAt(e.val))
	if start {
		e.inflight = true
	}
	val, ok = e.val, e.ok
	c.mu.Unlock()

	if start {
		c.spawn(func() { c.refresh(dir) })
	}
	return val, ok
}

// evictIdle drops idle entries. Callers hold c.mu. Inflight entries stay so
// refresh always finds its slot.
func (c *cache[T]) evictIdle(now time.Time) {
	for dir, e := range c.entries {
		if !e.inflight && now.Sub(e.seen) >= idleEvict {
			delete(c.entries, dir)
		}
	}
}

func (c *cache[T]) refresh(dir string) {
	c.slots <- struct{}{}
	val := c.fetch(context.Background(), dir)
	<-c.slots

	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[dir]
	e.val, e.at, e.ok, e.inflight = val, c.now(), true, false
}
