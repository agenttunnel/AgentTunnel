package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// desktopLaunch is one call to the desktop launcher.
type desktopLaunch struct {
	path string
	args []string
}

// stubDesktop puts an executable agenttunnel-desktop on PATH and replaces the
// launcher with a recorder, so no window opens.
func stubDesktop(t *testing.T) *[]desktopLaunch {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, desktopBinary), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var launches []desktopLaunch
	orig := startDesktop
	startDesktop = func(path string, args ...string) error {
		launches = append(launches, desktopLaunch{path: path, args: args})
		return nil
	}
	t.Cleanup(func() { startDesktop = orig })
	return &launches
}

func TestDesktopFlagRejectsBadArgs(t *testing.T) {
	launches := stubDesktop(t)
	env, _ := newUsageTestEnv()
	for _, args := range [][]string{{""}, {"-x"}, {"a", "b"}} {
		err := runDesktop(context.Background(), env, args)
		var ee exitError
		if !errors.As(err, &ee) || ee.code != 2 {
			t.Fatalf("runDesktop(%q) = %v, want a usage error", args, err)
		}
	}
	if len(*launches) != 0 {
		t.Fatalf("desktop launched on bad args: %+v", *launches)
	}
}

func TestDesktopFlagExplainsMissingDesktopBeforeConnecting(t *testing.T) {
	// No daemon URL and no state dir: reaching Connect would autostart a
	// daemon, so the install hint must come first, for both forms.
	t.Setenv("PATH", t.TempDir())
	env, _ := newUsageTestEnv()
	for _, args := range [][]string{nil, {t.TempDir()}} {
		err := runDesktop(context.Background(), env, args)
		if err == nil || !strings.Contains(err.Error(), "agenttunnel-desktop is not on PATH") {
			t.Fatalf("runDesktop(%q) = %v, want the install hint", args, err)
		}
	}
}

func TestDesktopFlagWithoutPathLaunchesBareDesktop(t *testing.T) {
	launches := stubDesktop(t)
	env, out := newUsageTestEnv()
	if err := runDesktop(context.Background(), env, nil); err != nil {
		t.Fatalf("runDesktop: %v", err)
	}
	if len(*launches) != 1 || len((*launches)[0].args) != 0 {
		t.Fatalf("launches = %+v, want one launch with no arguments", *launches)
	}
	if !strings.Contains(out.String(), "Opening AgentTunnel desktop") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDesktopFlagRejectsFileArgWithoutLaunching(t *testing.T) {
	launches := stubDesktop(t)
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, _ := newUsageTestEnv()
	err := runDesktop(context.Background(), env, []string{file})
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("runDesktop(file) = %v, want a not-a-directory error", err)
	}
	if len(*launches) != 0 {
		t.Fatalf("desktop launched for a file: %+v", *launches)
	}
}

func TestOpenProjectInDesktopRegistersUnknownDirectory(t *testing.T) {
	dir := resolvedDir(t, t.TempDir())
	fake := &fakeCLICaller{results: map[string]string{
		"project.list":   `[]`,
		"project.create": `{"project":{"id":"pr_1","name":"app","path":"` + dir + `","threads":0},"projects":[]}`,
	}}
	var launched []string
	start := func(path string, args ...string) error {
		launched = append([]string{path}, args...)
		return nil
	}
	project, created, err := openProjectInDesktop(context.Background(), fake, dir, "/bin/agenttunnel-desktop", start)
	if err != nil {
		t.Fatalf("openProjectInDesktop: %v", err)
	}
	if !created || project.ID != "pr_1" {
		t.Fatalf("project = %+v created = %v, want the new project", project, created)
	}
	if strings.Join(fake.calls, " ") != "project.list project.create" {
		t.Fatalf("RPC calls = %v", fake.calls)
	}
	if strings.Join(launched, " ") != "/bin/agenttunnel-desktop "+dir {
		t.Fatalf("launched %q, want the resolved directory", launched)
	}
}

func TestOpenProjectInDesktopReusesRegisteredProjectThroughSymlink(t *testing.T) {
	real := resolvedDir(t, t.TempDir())
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved := resolvedDir(t, link)
	fake := &fakeCLICaller{
		results: map[string]string{
			"project.list": `[{"id":"pr_9","name":"app","path":"` + real + `","threads":2}]`,
		},
		errs: map[string]error{"project.create": errors.New("existing project must not be re-created")},
	}
	start := func(string, ...string) error { return nil }
	project, created, err := openProjectInDesktop(context.Background(), fake, resolved, "/bin/x", start)
	if err != nil {
		t.Fatalf("openProjectInDesktop: %v", err)
	}
	if created || project.ID != "pr_9" {
		t.Fatalf("project = %+v created = %v, want the existing pr_9", project, created)
	}
}

func TestDesktopFlagDirectoryRegistersOnceAcrossSpellings(t *testing.T) {
	launches := stubDesktop(t)
	fake := &fakeProjects{}
	env, closeServer := newFakeProjectsEnv(t, fake)
	defer closeServer()
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	want := resolvedDir(t, dir)

	if err := runDesktop(context.Background(), env, []string{dir}); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := runDesktop(context.Background(), env, []string{link}); err != nil {
		t.Fatalf("second open through symlink: %v", err)
	}
	if len(fake.creates) != 1 || fake.creates[0]["path"] != want {
		t.Fatalf("creates = %v, want one project at %s", fake.creates, want)
	}
	if len(*launches) != 2 || (*launches)[1].args[0] != want {
		t.Fatalf("launches = %+v, want both opens on %s", *launches, want)
	}
	if got := env.Out.(*bytes.Buffer).String(); !strings.Contains(got, "added project") {
		t.Fatalf("output = %q, want the add notice", got)
	}
}

func TestDesktopFlagDispatchesThroughRun(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, errOut bytes.Buffer
	if code := Run([]string{"--desktop", "-x"}, "dev", &out, &errOut); code != 2 {
		t.Fatalf("Run(--desktop -x) = %d, want 2; stderr %q", code, errOut.String())
	}
}
