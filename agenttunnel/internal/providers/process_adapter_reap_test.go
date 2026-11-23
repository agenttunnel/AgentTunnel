package providers

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// A cancelled session must end its stream even when the first group signal
// misses. The first call here is a no-op, as a signal racing a fork is; the
// reaper has to sweep the group again until the pipes close.
func TestProcessAdapterCancelEndsStreamAfterAMissedGroupKill(t *testing.T) {
	a := NewProcessAdapter(scriptDriver(`sleep 30`))

	var mu sync.Mutex
	attempts := 0
	a.killTree = func(cmd *exec.Cmd) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			return
		}
		killProcessGroup(cmd)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx, SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	ch, err := a.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	closed := make(chan struct{})
	go func() {
		for range ch {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		// Failure guard only: the passing path waits on the stream closing.
		t.Fatal("stream never ended after cancel: the group was not swept again")
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts < 2 {
		t.Fatalf("group signalled %d time(s), want a retry after the missed kill", attempts)
	}
}
