package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOpenRequiresOneThreadRef(t *testing.T) {
	env, _ := newUsageTestEnv()
	for _, args := range [][]string{nil, {"a", "b"}, {"--json"}} {
		err := runOpen(context.Background(), env, args)
		var ee exitError
		if !errors.As(err, &ee) || ee.code != 2 {
			t.Fatalf("runOpen(%q) = %v, want a usage error", args, err)
		}
	}
}

func TestOpenExplainsMissingDesktopBeforeConnecting(t *testing.T) {
	// An empty PATH has no desktop client; the error must name the install
	// step and must not touch a daemon (Env with no daemon URL would autostart one).
	t.Setenv("PATH", t.TempDir())
	env, _ := newUsageTestEnv()
	err := runOpen(context.Background(), env, []string{"th_abc"})
	if err == nil {
		t.Fatal("runOpen without the desktop client succeeded")
	}
	if !strings.Contains(err.Error(), "agenttunnel-desktop is not on PATH") || !strings.Contains(err.Error(), "install") {
		t.Fatalf("error = %v, want the install hint", err)
	}
}

func TestOpenThreadInDesktopPassesResolvedID(t *testing.T) {
	fake := &fakeCLICaller{results: map[string]string{
		"thread.list": `[{"id":"th_abc123456789","title":"nightly audit","projectId":"p1"}]`,
	}}
	var gotPath string
	var gotArgs []string
	start := func(path string, args ...string) error {
		gotPath, gotArgs = path, args
		return nil
	}
	row, err := openThreadInDesktop(context.Background(), fake, "th_abc", "/bin/agenttunnel-desktop", start)
	if err != nil {
		t.Fatalf("openThreadInDesktop: %v", err)
	}
	if row.ID != "th_abc123456789" || row.Title != "nightly audit" {
		t.Fatalf("row = %+v", row)
	}
	if gotPath != "/bin/agenttunnel-desktop" || strings.Join(gotArgs, " ") != "--thread th_abc123456789" {
		t.Fatalf("launched %q %q, want --thread with the full id", gotPath, gotArgs)
	}
}

func TestOpenThreadInDesktopUnknownThreadLaunchesNothing(t *testing.T) {
	fake := &fakeCLICaller{results: map[string]string{"thread.list": `[]`}}
	start := func(string, ...string) error {
		t.Fatal("desktop launched for an unknown thread")
		return nil
	}
	if _, err := openThreadInDesktop(context.Background(), fake, "th_missing", "/bin/x", start); err == nil {
		t.Fatal("unknown thread resolved")
	}
}
