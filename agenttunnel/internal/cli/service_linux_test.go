//go:build linux

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSystemdInstallArgvSequence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	fake := &fakeRunner{}
	mgr, err := newPlatformService(home, fake)
	if err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", serviceUnit)
	if err := mgr.install(context.Background(), goldenSpec); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != renderSystemdUnit(goldenSpec) {
		t.Fatal("installed unit differs from the renderer output")
	}
	want := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", serviceUnit},
		{"systemctl", "--user", "restart", serviceUnit},
	}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("argv = %v\nwant    %v", fake.calls, want)
	}
}

func TestSystemdInstallHonorsXDGConfigHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mgr, err := newPlatformService(t.TempDir(), &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if got := mgr.(*systemd).unit; got != filepath.Join(xdg, "systemd", "user", serviceUnit) {
		t.Fatalf("unit path = %s", got)
	}
}

func TestSystemdInstallReportsEnableFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	fake := &fakeRunner{reply: func(argv []string) (string, error) {
		if argv[2] == "enable" {
			return "Failed to enable unit", errors.New("exit 1")
		}
		return "", nil
	}}
	mgr, _ := newPlatformService(t.TempDir(), fake)
	err := mgr.install(context.Background(), goldenSpec)
	if err == nil || !strings.Contains(err.Error(), "Failed to enable unit") {
		t.Fatalf("err = %v, want the enable output", err)
	}
}

// No systemd --user session (no bus) is an unavailable manager, not a refusal:
// install returns the classified error and removes the unit it just wrote, so
// `daemon status` does not report an installed service that nothing enabled.
func TestSystemdInstallUnavailableWithoutUserBus(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	fake := &fakeRunner{reply: func([]string) (string, error) {
		return "Failed to connect to bus: No medium found", errors.New("exit 1")
	}}
	mgr, _ := newPlatformService(home, fake)
	err := mgr.install(context.Background(), goldenSpec)
	if !errors.Is(err, errServiceManagerUnavailable) {
		t.Fatalf("err = %v, want errServiceManagerUnavailable", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("error spans lines: %q", err.Error())
	}
	if len(fake.calls) != 1 {
		t.Fatalf("argv = %v, want only daemon-reload", fake.calls)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", serviceUnit)
	if _, statErr := os.Stat(unit); !os.IsNotExist(statErr) {
		t.Fatalf("unit left behind after an unavailable install: %v", statErr)
	}
}

func TestSystemdUninstallArgvSequence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	unit := filepath.Join(home, ".config", "systemd", "user", serviceUnit)
	_ = os.MkdirAll(filepath.Dir(unit), 0o755)
	_ = os.WriteFile(unit, []byte("x"), 0o644)
	fake := &fakeRunner{}
	mgr, _ := newPlatformService(home, fake)
	if err := mgr.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"systemctl", "--user", "disable", "--now", serviceUnit},
		{"systemctl", "--user", "daemon-reload"},
	}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("argv = %v, want %v", fake.calls, want)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatalf("unit still present: %v", err)
	}
}

func TestSystemdStatusStates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	unit := filepath.Join(home, ".config", "systemd", "user", serviceUnit)
	_ = os.MkdirAll(filepath.Dir(unit), 0o755)
	_ = os.WriteFile(unit, []byte("x"), 0o644)

	fake := &fakeRunner{reply: func(argv []string) (string, error) {
		switch argv[2] {
		case "is-enabled":
			return "enabled", nil
		case "is-active":
			return "active\n", nil
		}
		return "", nil
	}}
	mgr, _ := newPlatformService(home, fake)
	got, err := mgr.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := serviceStatus{Installed: true, Loaded: true, Running: true, Service: unit}
	if got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
	wantArgv := [][]string{
		{"systemctl", "--user", "is-enabled", serviceUnit},
		{"systemctl", "--user", "is-active", serviceUnit},
	}
	if !reflect.DeepEqual(fake.calls, wantArgv) {
		t.Fatalf("argv = %v, want %v", fake.calls, wantArgv)
	}
}
