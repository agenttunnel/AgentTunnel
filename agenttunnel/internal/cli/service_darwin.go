//go:build darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// serviceNote is printed after install; empty on macOS, where a LaunchAgent
// starts at login with nothing else to configure.
const serviceNote = ""

// launchdPID matches the pid line `launchctl print` shows for a running job.
var launchdPID = regexp.MustCompile(`\bpid = [1-9][0-9]*`)

// Bounds for the re-install path. bootout only queues the teardown, so install
// polls for the job to disappear (about 5s), then retries a bootstrap that still
// reports "Bootstrap failed: 5" (Input/output error) a few times.
const (
	bootoutPolls     = 20
	bootoutPollEvery = 250 * time.Millisecond
	bootstrapTries   = 5
	bootstrapEvery   = 500 * time.Millisecond
)

// launchd manages the com.agenttunnel.atd LaunchAgent in the user's gui domain.
type launchd struct {
	home string
	uid  int
	run  serviceRunner
	// pause is the clock seam for the bounded waits; tests replace it.
	pause func(context.Context, time.Duration) error
}

func newPlatformService(home string, r serviceRunner) (serviceManager, error) {
	if home == "" {
		return nil, errors.New("cannot locate the home directory for the LaunchAgent")
	}
	return &launchd{home: home, uid: os.Getuid(), run: r, pause: servicePause}, nil
}

func (l *launchd) plistPath() string {
	return filepath.Join(l.home, "Library", "LaunchAgents", serviceLabel+".plist")
}

func (l *launchd) domain() string { return fmt.Sprintf("gui/%d", l.uid) }
func (l *launchd) target() string { return l.domain() + "/" + serviceLabel }

// install writes the plist, then boots out any loaded copy and bootstraps the
// new one, so re-running install replaces the agent in place. Booting out a
// loaded copy stops a running daemon and cancels its turns; that is the cost of
// an upgrade, and it is why install waits for the teardown before bootstrapping.
func (l *launchd) install(ctx context.Context, s serviceSpec) error {
	path := l.plistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderLaunchAgent(s)), 0o644); err != nil {
		return err
	}
	// Not loaded yet is the normal first-install case, so bootout's refusal is
	// ignored. Only a missing launchctl stops the install.
	if out, err := l.run.run(ctx, "launchctl", "bootout", l.target()); isServiceManagerMissing(out, err) {
		_ = os.Remove(path)
		return unavailableOrErr("launchctl bootout", out, err)
	}
	if err := l.waitUnloaded(ctx); err != nil {
		return err
	}
	return l.bootstrap(ctx, path)
}

// waitUnloaded polls `launchctl print` until the job is gone. bootout returns
// once teardown is queued, so the old job can still be loaded when bootstrap
// runs. A job still loaded after the bound is left to bootstrap's retry.
func (l *launchd) waitUnloaded(ctx context.Context) error {
	for i := 0; i < bootoutPolls; i++ {
		if _, err := l.run.run(ctx, "launchctl", "print", l.target()); err != nil {
			return nil
		}
		if err := l.pause(ctx, bootoutPollEvery); err != nil {
			return err
		}
	}
	return nil
}

// bootstrap loads the plist, retrying a bounded number of times because a
// teardown that is still finishing makes bootstrap fail with "Bootstrap failed: 5".
func (l *launchd) bootstrap(ctx context.Context, path string) error {
	var out string
	var err error
	for attempt := 1; attempt <= bootstrapTries; attempt++ {
		out, err = l.run.run(ctx, "launchctl", "bootstrap", l.domain(), path)
		if err == nil {
			return nil
		}
		if attempt == bootstrapTries {
			break
		}
		if perr := l.pause(ctx, bootstrapEvery); perr != nil {
			return perr
		}
	}
	return commandErr("launchctl bootstrap", out, err)
}

func (l *launchd) uninstall(ctx context.Context) error {
	// bootout stops a running daemon; not being loaded is not an error here.
	_, _ = l.run.run(ctx, "launchctl", "bootout", l.target())
	if err := os.Remove(l.plistPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (l *launchd) status(ctx context.Context) (serviceStatus, error) {
	st := serviceStatus{Service: l.plistPath()}
	_, err := os.Stat(l.plistPath())
	st.Installed = err == nil
	out, err := l.run.run(ctx, "launchctl", "print", l.target())
	st.Loaded = err == nil
	st.Running = st.Loaded && launchdPID.MatchString(out)
	return st, nil
}
