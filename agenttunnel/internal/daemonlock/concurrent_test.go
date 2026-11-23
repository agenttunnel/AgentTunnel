package daemonlock

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Concurrent acquirers over one state dir take the lock one at a time: at most
// one holder is inside the critical section, and every acquirer eventually gets
// its turn once the previous holder releases.
func TestConcurrentAcquireSerializes(t *testing.T) {
	state := t.TempDir()
	const acquirers = 8

	var inside, maxInside, violations atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, acquirers)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for i := 0; i < acquirers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := Acquire(ctx, state, time.Millisecond)
			if err != nil {
				errs <- err
				return
			}
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			// While this holder owns the lock, a competing acquire must be
			// refused. Probing here is deterministic, unlike relying on the
			// critical sections happening to overlap.
			if other, err := TryAcquire(state); err == nil {
				other.Release()
				violations.Add(1)
			} else if !errors.Is(err, ErrHeld) {
				errs <- err
			}
			inside.Add(-1)
			lock.Release()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("acquire: %v", err)
	}
	if got := maxInside.Load(); got != 1 {
		t.Fatalf("max concurrent holders = %d, want 1", got)
	}
	if got := violations.Load(); got != 0 {
		t.Fatalf("%d competing acquires succeeded while the lock was held", got)
	}
}
