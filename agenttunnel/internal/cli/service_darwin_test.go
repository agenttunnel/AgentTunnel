//go:build darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// launchdFake models the job's loaded state. bootout only queues the teardown:
// the job stays loaded for teardownPolls more `print` calls after it.
type launchdFake struct {
	loaded        bool
	teardownPolls int
	bootstrapErrs int // bootstrap calls that fail with "Bootstrap failed: 5" first
	bootoutOut    string
	bootoutErr    error
	calls         [][]string
}

func (f *launchdFake) run(_ context.Context, name string, args ...string) (string, error) {
	argv := append([]string{name}, args...)
	f.calls = append(f.calls, argv)
	switch argv[1] {
	case "bootout":
		if f.bootoutErr != nil {
			return f.bootoutOut, f.bootoutErr
		}
		f.loaded = false
		return "", nil
	case "print":
		if f.teardownPolls > 0 {
			f.teardownPolls--
			return "state = running\n\tpid = 4242\n", nil
		}
		if f.loaded {
			return "state = running\n\tpid = 4242\n", nil
		}
		return "", errors.New("exit 113")
	case "bootstrap":
		if f.bootstrapErrs > 0 {
			f.bootstrapErrs--
			return "Bootstrap failed: 5: Input/output error", errors.New("exit 5")
		}
		f.loaded = true
		return "", nil
	}
	return "", nil
}

// testLaunchd builds the darwin manager over a fake runner with a recording,
// non-sleeping pause, so the bounded waits are asserted without real time.
func testLaunchd(t *testing.T, home string, r serviceRunner) (*launchd, *[]time.Duration) {
	t.Helper()
	mgr, err := newPlatformService(home, r)
	if err != nil {
		t.Fatal(err)
	}
	l := mgr.(*launchd)
	var waits []time.Duration
	l.pause = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return l, &waits
}

func TestLaunchdInstallArgvSequence(t *testing.T) {
	home := t.TempDir()
	fake := &launchdFake{loaded: true}
	l, waits := testLaunchd(t, home, fake)
	if err := l.install(context.Background(), goldenSpec); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
	if l.plistPath() != plist {
		t.Fatalf("plist path = %s, want %s", l.plistPath(), plist)
	}
	body, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != renderLaunchAgent(goldenSpec) {
		t.Fatal("installed plist differs from the renderer output")
	}
	want := [][]string{
		{"launchctl", "bootout", l.target()},
		{"launchctl", "print", l.target()},
		{"launchctl", "bootstrap", l.domain(), plist},
	}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("argv = %v\nwant    %v", fake.calls, want)
	}
	if len(*waits) != 0 {
		t.Fatalf("an already-gone job waited %v, want no pause", *waits)
	}
}

// bootout returns before launchd finishes the teardown: install polls print
// until the job is gone, then bootstraps once, with no bootstrap retry.
func TestLaunchdInstallWaitsForSlowTeardown(t *testing.T) {
	home := t.TempDir()
	fake := &launchdFake{loaded: true, teardownPolls: 3}
	l, waits := testLaunchd(t, home, fake)
	if err := l.install(context.Background(), goldenSpec); err != nil {
		t.Fatal(err)
	}
	var bootstraps int
	for _, c := range fake.calls {
		if c[1] == "bootstrap" {
			bootstraps++
		}
	}
	if bootstraps != 1 {
		t.Fatalf("bootstrap ran %d times, want 1 after the teardown finished", bootstraps)
	}
	if len(fake.calls) != 6 { // bootout, 4× print, bootstrap
		t.Fatalf("argv count = %d, want 6: %v", len(fake.calls), fake.calls)
	}
	wantWaits := []time.Duration{bootoutPollEvery, bootoutPollEvery, bootoutPollEvery}
	if !reflect.DeepEqual(*waits, wantWaits) {
		t.Fatalf("waits = %v, want %v", *waits, wantWaits)
	}
}

// A job that never finishes tearing down is bounded: install stops polling after
// bootoutPolls and lets bootstrap decide, so a stuck launchd cannot hang install.
func TestLaunchdInstallBoundsTheTeardownWait(t *testing.T) {
	fake := &launchdFake{loaded: true, teardownPolls: 1 << 30}
	l, waits := testLaunchd(t, t.TempDir(), fake)
	if err := l.install(context.Background(), goldenSpec); err != nil {
		t.Fatalf("install with a stuck teardown: %v", err)
	}
	if len(*waits) != bootoutPolls {
		t.Fatalf("waited %d times, want the bound %d", len(*waits), bootoutPolls)
	}
}

// A transient "Bootstrap failed: 5" is retried, and install succeeds on the
// attempt that lands.
func TestLaunchdInstallRetriesTransientBootstrapFailure(t *testing.T) {
	fake := &launchdFake{loaded: true, bootstrapErrs: 2}
	l, waits := testLaunchd(t, t.TempDir(), fake)
	if err := l.install(context.Background(), goldenSpec); err != nil {
		t.Fatalf("install with two transient bootstrap failures: %v", err)
	}
	var bootstraps int
	for _, c := range fake.calls {
		if c[1] == "bootstrap" {
			bootstraps++
		}
	}
	if bootstraps != 3 {
		t.Fatalf("bootstrap attempts = %d, want 3", bootstraps)
	}
	// Teardown is instant here (bootout clears loaded), so the only waits are the
	// two pauses between the three bootstrap attempts.
	wantWaits := []time.Duration{bootstrapEvery, bootstrapEvery}
	if !reflect.DeepEqual(*waits, wantWaits) {
		t.Fatalf("waits = %v, want %v", *waits, wantWaits)
	}
}

// Bootstrap that never succeeds gives up after bootstrapTries and reports the
// launchd output, not a hang.
func TestLaunchdInstallGivesUpAfterBootstrapRetries(t *testing.T) {
	fake := &launchdFake{loaded: true, bootstrapErrs: 1 << 30}
	l, _ := testLaunchd(t, t.TempDir(), fake)
	err := l.install(context.Background(), goldenSpec)
	if err == nil || !strings.Contains(err.Error(), "Bootstrap failed: 5") {
		t.Fatalf("err = %v, want the bootstrap output", err)
	}
	var bootstraps int
	for _, c := range fake.calls {
		if c[1] == "bootstrap" {
			bootstraps++
		}
	}
	if bootstraps != bootstrapTries {
		t.Fatalf("bootstrap attempts = %d, want %d", bootstraps, bootstrapTries)
	}
}

// A first install has nothing loaded, so bootout fails with exit 3; install
// must carry on, and print confirms the job is absent so there is no wait.
func TestLaunchdInstallIgnoresBootoutWhenNotLoaded(t *testing.T) {
	fake := &launchdFake{bootoutOut: "Boot-out failed: 3: No such process", bootoutErr: errors.New("exit 3")}
	l, waits := testLaunchd(t, t.TempDir(), fake)
	if err := l.install(context.Background(), goldenSpec); err != nil {
		t.Fatalf("install failed on a not-loaded bootout: %v", err)
	}
	if len(*waits) != 0 {
		t.Fatalf("a not-loaded job waited %v", *waits)
	}
}

// Without launchctl the install reports the unavailable manager, not a stack or
// a bootstrap error, and leaves no plist behind for `daemon status` to report.
func TestLaunchdInstallUnavailableWithoutLaunchctl(t *testing.T) {
	home := t.TempDir()
	fake := &fakeRunner{reply: func([]string) (string, error) {
		return "", fmt.Errorf("exec: %q: executable file not found in $PATH", "launchctl")
	}}
	l, _ := testLaunchd(t, home, fake)
	err := l.install(context.Background(), goldenSpec)
	if !errors.Is(err, errServiceManagerUnavailable) {
		t.Fatalf("err = %v, want errServiceManagerUnavailable", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("argv = %v, want only the bootout probe", fake.calls)
	}
	if _, statErr := os.Stat(l.plistPath()); !os.IsNotExist(statErr) {
		t.Fatalf("plist left behind after an unavailable install: %v", statErr)
	}
}

func TestLaunchdInstallReportsBootstrapFailure(t *testing.T) {
	fake := &launchdFake{bootstrapErrs: 1 << 30}
	l, _ := testLaunchd(t, t.TempDir(), fake)
	err := l.install(context.Background(), goldenSpec)
	if err == nil || !strings.Contains(err.Error(), "Bootstrap failed: 5") {
		t.Fatalf("err = %v, want the bootstrap output", err)
	}
	if errors.Is(err, errServiceManagerUnavailable) {
		t.Fatal("a refused bootstrap must not read as an unavailable service manager")
	}
}

func TestLaunchdUninstallArgvSequence(t *testing.T) {
	home := t.TempDir()
	plist := filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeRunner{}
	mgr, _ := newPlatformService(home, fake)
	if err := mgr.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"launchctl", "bootout", "gui/" + fmt.Sprint(os.Getuid()) + "/" + serviceLabel}}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("argv = %v, want %v", fake.calls, want)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("plist still present after uninstall: %v", err)
	}
}

// Uninstalling a service that was never installed is a no-op, not an error.
func TestLaunchdUninstallWhenAbsentIsNoop(t *testing.T) {
	fake := &fakeRunner{reply: func([]string) (string, error) { return "", errors.New("not loaded") }}
	mgr, _ := newPlatformService(t.TempDir(), fake)
	if err := mgr.uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall of absent service: %v", err)
	}
}

func TestLaunchdStatusStates(t *testing.T) {
	home := t.TempDir()
	plist := filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
	_ = os.MkdirAll(filepath.Dir(plist), 0o755)

	cases := []struct {
		name      string
		installed bool
		printOut  string
		printErr  error
		want      serviceStatus
	}{
		{name: "absent", printErr: errors.New("exit 113"), want: serviceStatus{Service: plist}},
		{name: "installed not loaded", installed: true, printErr: errors.New("exit 113"),
			want: serviceStatus{Installed: true, Service: plist}},
		{name: "loaded not running", installed: true, printOut: "state = waiting\n",
			want: serviceStatus{Installed: true, Loaded: true, Service: plist}},
		{name: "running", installed: true, printOut: "\tstate = running\n\tpid = 4242\n",
			want: serviceStatus{Installed: true, Loaded: true, Running: true, Service: plist}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(plist)
			if tc.installed {
				if err := os.WriteFile(plist, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			fake := &fakeRunner{reply: func([]string) (string, error) { return tc.printOut, tc.printErr }}
			mgr, _ := newPlatformService(home, fake)
			got, err := mgr.status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("status = %+v, want %+v", got, tc.want)
			}
			wantArgv := []string{"launchctl", "print", "gui/" + fmt.Sprint(os.Getuid()) + "/" + serviceLabel}
			if len(fake.calls) != 1 || !reflect.DeepEqual(fake.calls[0], wantArgv) {
				t.Fatalf("argv = %v, want one call %v", fake.calls, wantArgv)
			}
		})
	}
}

func TestLaunchdNeedsHome(t *testing.T) {
	if _, err := newPlatformService("", &fakeRunner{}); err == nil {
		t.Fatal("empty home must fail, not write relative to the working directory")
	}
}
