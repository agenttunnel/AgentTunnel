package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/daemonlock"
	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

// With no daemon on the state dir, the start takes the lock and owns it.
func TestClaimStartOwnsLockWithoutDaemon(t *testing.T) {
	state := t.TempDir()
	lock, _, attach, err := claimStart(context.Background(), state, time.Second)
	if err != nil {
		t.Fatalf("claimStart: %v", err)
	}
	if attach || lock == nil {
		t.Fatalf("attach=%v lock=%v, want owned lock", attach, lock)
	}
	if _, err := daemonlock.TryAcquire(state); !errors.Is(err, daemonlock.ErrHeld) {
		t.Fatalf("second acquire while owned = %v, want ErrHeld", err)
	}
	lock.Release()
}

// A start waiting on the lock must not start a daemon while another holds it.
// When that holder publishes a live daemon and releases, the waiter re-checks
// under the lock and attaches instead of owning a second daemon.
func TestClaimStartAttachesToDaemonPublishedUnderTheLock(t *testing.T) {
	srv, _ := fakeDaemon(t)
	state := t.TempDir()

	holder, err := daemonlock.TryAcquire(state)
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}

	type result struct {
		lock   *daemonlock.Lock
		target daemonTarget
		attach bool
		err    error
	}
	done := make(chan result, 1)
	go func() {
		lock, target, attach, err := claimStart(context.Background(), state, 10*time.Second)
		done <- result{lock, target, attach, err}
	}()

	// The waiter cannot finish while the holder owns the lock.
	select {
	case r := <-done:
		t.Fatalf("claimStart returned while the lock was held: %+v", r)
	case <-time.After(30 * time.Millisecond):
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	if err := discovery.Write(state, discovery.Info{WSURL: wsURL, Token: "tok"}); err != nil {
		t.Fatalf("publish discovery: %v", err)
	}
	holder.Release()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("claimStart: %v", r.err)
		}
		if !r.attach || r.lock != nil {
			t.Fatalf("attach=%v lock=%v, want attach with no lock", r.attach, r.lock)
		}
		if r.target.wsURL != wsURL {
			t.Fatalf("attached to %q, want %q", r.target.wsURL, wsURL)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("claimStart did not return after the lock was released")
	}
	// Attaching must not leave the lock held.
	again, err := daemonlock.TryAcquire(state)
	if err != nil {
		t.Fatalf("lock still held after attach: %v", err)
	}
	again.Release()
}
