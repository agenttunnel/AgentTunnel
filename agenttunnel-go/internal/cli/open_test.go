package cli

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
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
	err := runOpen(context.Background(), env, []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "agenttunnel open <thread | dir>") {
		t.Fatalf("runOpen(two args) = %v, want the usage text to mention the dir form", err)
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

func TestOpenDirectoryLaunchesDesktopWithoutDaemon(t *testing.T) {
	// An unroutable daemon URL makes any Connect fail fast: a nil error here
	// proves dir mode never contacts a daemon.
	launches := stubDesktop(t)
	env, out := newUsageTestEnv()
	env.DaemonURL = "ws://127.0.0.1:1/ws"
	dir := t.TempDir()
	if err := runOpen(context.Background(), env, []string{dir}); err != nil {
		t.Fatalf("runOpen(dir): %v", err)
	}
	if len(*launches) != 1 {
		t.Fatalf("launches = %+v, want exactly one", *launches)
	}
	launch := (*launches)[0]
	if len(launch.args) != 1 || launch.args[0] != dir {
		t.Fatalf("launched %q, want the absolutized dir as the only argument (%q)", launch.args, dir)
	}
	if !strings.Contains(out.String(), "Opening "+dir+" in AgentTunnel desktop") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestOpenDirectoryAbsolutizesRelativeArg(t *testing.T) {
	launches := stubDesktop(t)
	env, _ := newUsageTestEnv()
	env.DaemonURL = "ws://127.0.0.1:1/ws"
	t.Chdir(t.TempDir())
	want, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := runOpen(context.Background(), env, []string{"."}); err != nil {
		t.Fatalf("runOpen(.): %v", err)
	}
	if len(*launches) != 1 || len((*launches)[0].args) != 1 || (*launches)[0].args[0] != want {
		t.Fatalf("launches = %+v, want a launch on %q", *launches, want)
	}
}

func TestOpenNonexistentArgFallsThroughToThreadResolution(t *testing.T) {
	launches := stubDesktop(t)
	// The scripted daemon answers thread.list with nothing, so a
	// nonexistent path resolves against threads and fails there.
	s := newScriptedServer()
	s.results["thread.list"] = []map[string]any{}
	s.results["project.list"] = []map[string]any{} // the Connect ping
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	env, _ := newUsageTestEnv()
	env.DaemonURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	err := runOpen(context.Background(), env, []string{"no-such-dir-or-thread"})
	if err == nil || !strings.Contains(err.Error(), "no thread matches") {
		t.Fatalf("runOpen(nonexistent) = %v, want the no-thread-matches error", err)
	}
	if len(*launches) != 0 {
		t.Fatalf("desktop launched for a nonexistent arg: %+v", *launches)
	}
}

func TestOpenThreadRefStillPassesThreadFlag(t *testing.T) {
	launches := stubDesktop(t)
	s := newScriptedServer()
	s.results["thread.list"] = []map[string]any{
		{"id": "th_abc123456789", "title": "nightly audit", "projectId": "p1"},
	}
	s.results["project.list"] = []map[string]any{} // the Connect ping
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	env, out := newUsageTestEnv()
	env.DaemonURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	if err := runOpen(context.Background(), env, []string{"th_abc"}); err != nil {
		t.Fatalf("runOpen(thread ref): %v", err)
	}
	if len(*launches) != 1 {
		t.Fatalf("launches = %+v, want exactly one", *launches)
	}
	launch := (*launches)[0]
	if strings.Join(launch.args, " ") != "--thread th_abc123456789" {
		t.Fatalf("launched %q, want --thread with the full id", launch.args)
	}
	if !strings.Contains(out.String(), "th_abc123456789") {
		t.Fatalf("output = %q", out.String())
	}
}
