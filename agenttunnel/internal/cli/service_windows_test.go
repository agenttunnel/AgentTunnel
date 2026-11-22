//go:build windows

package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestTaskSchedulerInstallArgvSequence(t *testing.T) {
	fake := &fakeRunner{}
	mgr, _ := newPlatformService("", fake)
	spec := goldenSpec
	spec.StateDir = t.TempDir()
	if err := mgr.install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("argv = %v", fake.calls)
	}
	create := fake.calls[0]
	if !reflect.DeepEqual(create[:5], []string{"schtasks", "/Create", "/TN", serviceTaskName, "/XML"}) || create[len(create)-1] != "/F" {
		t.Fatalf("create argv = %v", create)
	}
	want := []string{"schtasks", "/Run", "/TN", serviceTaskName}
	if !reflect.DeepEqual(fake.calls[1], want) {
		t.Fatalf("run argv = %v, want %v", fake.calls[1], want)
	}
}

func TestTaskSchedulerInstallUnavailableWithoutSchtasks(t *testing.T) {
	fake := &fakeRunner{reply: func([]string) (string, error) {
		return "", errors.New(`exec: "schtasks": executable file not found in $PATH`)
	}}
	mgr, _ := newPlatformService("", fake)
	spec := goldenSpec
	spec.StateDir = t.TempDir()
	err := mgr.install(context.Background(), spec)
	if !errors.Is(err, errServiceManagerUnavailable) {
		t.Fatalf("err = %v, want errServiceManagerUnavailable", err)
	}
}

func TestTaskSchedulerUninstallWhenAbsentIsNoop(t *testing.T) {
	fake := &fakeRunner{reply: func([]string) (string, error) { return "ERROR: not found", errors.New("exit 1") }}
	mgr, _ := newPlatformService("", fake)
	if err := mgr.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("argv = %v, want only the query", fake.calls)
	}
}

func TestTaskSchedulerStatusRunning(t *testing.T) {
	fake := &fakeRunner{reply: func([]string) (string, error) { return "Status: Running\n", nil }}
	mgr, _ := newPlatformService("", fake)
	got, _ := mgr.status(context.Background())
	if !got.Installed || !got.Loaded || !got.Running || !strings.Contains(fake.calls[0][2], "/Query") {
		t.Fatalf("status = %+v, argv %v", got, fake.calls)
	}
}
