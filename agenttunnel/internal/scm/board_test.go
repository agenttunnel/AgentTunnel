package scm

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBoardPeekMissStartsOneFetchThenServesCache(t *testing.T) {
	var calls int
	fetch := func(context.Context, string) Snapshot {
		calls++
		return Snapshot{Status: StatusReady, Request: &Request{Number: 7, State: "merged"}}
	}
	b := newBoard(fetch, time.Minute, time.Now, func(f func()) { f() })

	if _, ok := b.Peek("/wt/a"); ok {
		t.Fatal("first peek should miss while the fetch is pending")
	}
	snap, ok := b.Peek("/wt/a")
	if !ok || snap.Request == nil || snap.Request.State != "merged" {
		t.Fatalf("second peek = %+v, %v; want the merged request", snap, ok)
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1 (fresh entries must not refetch)", calls)
	}
}

func TestBoardRefreshesOnlyAfterTTL(t *testing.T) {
	var calls int
	clock := time.Unix(0, 0)
	fetch := func(context.Context, string) Snapshot {
		calls++
		return Snapshot{Status: StatusNone}
	}
	b := newBoard(fetch, time.Minute, func() time.Time { return clock }, func(f func()) { f() })

	b.Peek("/wt/a")
	clock = clock.Add(30 * time.Second)
	b.Peek("/wt/a")
	if calls != 1 {
		t.Fatalf("calls within ttl = %d, want 1", calls)
	}
	clock = clock.Add(31 * time.Second)
	snap, ok := b.Peek("/wt/a")
	if !ok || snap.Status != StatusNone {
		t.Fatalf("stale peek should still answer the last snapshot, got %+v, %v", snap, ok)
	}
	if calls != 2 {
		t.Fatalf("calls after ttl = %d, want 2", calls)
	}
}

func TestBoardDedupesInflightFetches(t *testing.T) {
	var queued []func()
	b := newBoard(func(context.Context, string) Snapshot { return Snapshot{} }, time.Minute,
		time.Now, func(f func()) { queued = append(queued, f) })

	b.Peek("/wt/a")
	b.Peek("/wt/a")
	b.Peek("/wt/a")
	if len(queued) != 1 {
		t.Fatalf("queued refreshes = %d, want 1 while one is in flight", len(queued))
	}
}

func TestBoardBacksOffWhenForgeFails(t *testing.T) {
	var calls int
	clock := time.Unix(0, 0)
	fetch := func(context.Context, string) Snapshot {
		calls++
		return Snapshot{Status: StatusUnavailable, Message: "gh not installed"}
	}
	b := newBoard(fetch, time.Minute, func() time.Time { return clock }, func(f func()) { f() })

	b.Peek("/wt/a")
	clock = clock.Add(2 * time.Minute)
	b.Peek("/wt/a")
	if calls != 1 {
		t.Fatalf("calls after 2m with gh missing = %d, want 1 (failures back off past the normal ttl)", calls)
	}
	clock = clock.Add(failedTTL)
	b.Peek("/wt/a")
	if calls != 2 {
		t.Fatalf("calls after failedTTL = %d, want 2", calls)
	}
}

func TestBoardCapsConcurrentRefreshes(t *testing.T) {
	const dirs = maxConcurrentRefreshes + 2
	var (
		mu       sync.Mutex
		running  int
		peak     int
		entered  = make(chan struct{}, dirs)
		finished = make(chan struct{}, dirs)
		release  = make(chan struct{})
	)
	fetch := func(context.Context, string) Snapshot {
		mu.Lock()
		running++
		peak = max(peak, running)
		mu.Unlock()
		entered <- struct{}{}
		<-release
		mu.Lock()
		running--
		mu.Unlock()
		finished <- struct{}{}
		return Snapshot{Status: StatusNone}
	}
	b := newBoard(fetch, time.Minute, time.Now, func(f func()) { go f() })

	for i := range dirs {
		b.Peek(fmt.Sprintf("/wt/%d", i))
	}
	for range maxConcurrentRefreshes {
		<-entered
	}
	close(release)
	for range dirs {
		<-finished
	}
	if peak != maxConcurrentRefreshes {
		t.Fatalf("peak concurrent fetches = %d, want %d", peak, maxConcurrentRefreshes)
	}
}

func TestBoardEvictsIdleDirectories(t *testing.T) {
	clock := time.Unix(0, 0)
	fetch := func(context.Context, string) Snapshot { return Snapshot{Status: StatusNone} }
	b := newBoard(fetch, time.Minute, func() time.Time { return clock }, func(f func()) { f() })

	b.Peek("/wt/gone")
	b.Peek("/wt/open")
	clock = clock.Add(idleEvict)
	b.Peek("/wt/open")

	if _, ok := b.entries["/wt/gone"]; ok {
		t.Fatal("entry for a directory no longer requested was kept")
	}
	if _, ok := b.entries["/wt/open"]; !ok {
		t.Fatal("entry for a directory still on the board was evicted")
	}
}
