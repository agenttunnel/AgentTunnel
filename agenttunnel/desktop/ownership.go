package main

import (
	"context"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/daemonlock"
)

// startWait bounds how long a desktop start waits for another process to finish
// its own start. An `agenttunnel` autostart or an atd boot holds the lock for at
// most about its 10s answer window.
const startWait = 15 * time.Second

// claimStart decides, under the cross-process daemon lock, whether this process
// may start an embedded daemon on state. It is the same serialization Connect
// and atd use: a daemon a service manager started between main's unlocked check
// and now is found here and attached to, so two daemons never share one state
// dir. When it returns attach=true the lock is already released and target
// names the daemon to attach to; otherwise the caller owns the returned lock
// until its daemon has published discovery.
func claimStart(ctx context.Context, state string, wait time.Duration) (lock *daemonlock.Lock, target daemonTarget, attach bool, err error) {
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	lock, err = daemonlock.Acquire(wctx, state, 50*time.Millisecond)
	if err != nil {
		return nil, daemonTarget{}, false, err
	}
	if target, ok := findDaemon(state); ok {
		lock.Release()
		return nil, target, true, nil
	}
	return lock, daemonTarget{}, false, nil
}
