package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// fakeClock keeps cache tests independent of wall time.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{at: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type fakeRun struct {
	bin  string
	args []string
}

// fakeHarnessEnv is a PATH, a set of --version outputs, and a recording
// runner. No process is ever started.
type fakeHarnessEnv struct {
	mu            sync.Mutex
	bins          map[string]string
	realPaths     map[string]string
	harnessByPath map[string]contracts.Harness
	outputs       map[contracts.Harness]string
	latestOutputs map[contracts.Harness]string
	runErrors     map[contracts.Harness]error
	runOutput     map[contracts.Harness][]byte
	probes        map[contracts.Harness]int
	runs          []fakeRun
}

func newFakeHarnessEnv() *fakeHarnessEnv {
	return &fakeHarnessEnv{
		bins:          map[string]string{},
		realPaths:     map[string]string{},
		harnessByPath: map[string]contracts.Harness{},
		outputs:       map[contracts.Harness]string{},
		latestOutputs: map[contracts.Harness]string{},
		runErrors:     map[contracts.Harness]error{},
		runOutput:     map[contracts.Harness][]byte{},
		probes:        map[contracts.Harness]int{},
	}
}

func (f *fakeHarnessEnv) install(t *testing.T, h contracts.Harness, path, versionOutput string) {
	t.Helper()
	target, ok := lookupUpdateTarget(h)
	if !ok {
		t.Fatalf("no update target for %q", h)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bins[target.binaries[0]] = path
	f.harnessByPath[path] = h
	f.outputs[h] = versionOutput
}

func (f *fakeHarnessEnv) lookPath(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path, ok := f.bins[name]; ok {
		return path, nil
	}
	return "", exec.ErrNotFound
}

// realPath mirrors EvalSymlinks; unlisted paths resolve to themselves.
func (f *fakeHarnessEnv) realPath(path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if real, ok := f.realPaths[path]; ok {
		return real, nil
	}
	return path, nil
}

func (f *fakeHarnessEnv) probe(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.harnessByPath[bin]
	if !ok {
		return nil, fmt.Errorf("unexpected probe of %q", bin)
	}
	f.probes[h]++
	if len(args) != 1 || args[0] != "--version" {
		return nil, fmt.Errorf("unexpected probe args %q", args)
	}
	out, ok := f.outputs[h]
	if !ok {
		return nil, fmt.Errorf("no version output for %q", h)
	}
	return []byte(out), nil
}

func (f *fakeHarnessEnv) run(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, fakeRun{bin: bin, args: append([]string(nil), args...)})
	h := f.harnessFor(bin, args)
	if err, ok := f.runErrors[h]; ok {
		return f.runOutput[h], err
	}
	if out, ok := f.latestOutputs[h]; ok {
		f.outputs[h] = out
	}
	return nil, nil
}

func (f *fakeHarnessEnv) harnessFor(bin string, args []string) contracts.Harness {
	if h, ok := f.harnessByPath[bin]; ok {
		return h
	}
	joined := strings.Join(args, " ")
	for _, target := range updateTargets() {
		if target.npmPackage != "" && strings.Contains(joined, target.npmPackage) {
			return target.harness
		}
		if target.brewFormula != "" && strings.Contains(joined, target.brewFormula) {
			return target.harness
		}
	}
	return ""
}

func (f *fakeHarnessEnv) probeCount(h contracts.Harness) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes[h]
}

func (f *fakeHarnessEnv) runCalls() []fakeRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRun(nil), f.runs...)
}

func lookupUpdateTarget(h contracts.Harness) (updateTarget, bool) {
	for _, target := range updateTargets() {
		if target.harness == h {
			return target, true
		}
	}
	return updateTarget{}, false
}

// npmRegistry serves the versions map keyed by package name and counts hits.
func npmRegistry(t *testing.T, versions map[string]string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		pkg := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/latest")
		version, ok := versions[pkg]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		fmt.Fprintf(w, `{"version":%q}`, version)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// cursorInstallScript mimics the public installer: the version lives in the
// download URL.
func cursorInstallScript(t *testing.T, version string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#!/bin/sh\nDOWNLOAD_URL=\"https://downloads.cursor.com/lab/%s/darwin/arm64/Cursor.tar.gz\"\n", version)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testUpdater(t *testing.T, env *fakeHarnessEnv, npmBase, cursorURL string, clock *fakeClock) *HarnessUpdater {
	t.Helper()
	return NewHarnessUpdater(HarnessUpdaterOptions{
		HTTP:             http.DefaultClient,
		NpmBase:          npmBase,
		CursorInstallURL: cursorURL,
		LookPath:         env.lookPath,
		RealPath:         env.realPath,
		Probe:            env.probe,
		Run:              env.run,
		Now:              clock.now,
	})
}

func TestUpdateTargetsCoverEveryHarness(t *testing.T) {
	targets := updateTargets()
	if len(targets) != len(contracts.AllHarnesses()) {
		t.Fatalf("got %d targets, want %d", len(targets), len(contracts.AllHarnesses()))
	}
	seen := make(map[contracts.Harness]bool, len(targets))
	for _, target := range targets {
		if seen[target.harness] {
			t.Fatalf("duplicate target for %q", target.harness)
		}
		seen[target.harness] = true
		if len(target.binaries) == 0 {
			t.Fatalf("%q has no binary candidates", target.harness)
		}
		if target.latest == latestNone {
			t.Fatalf("%q has no latest-version source", target.harness)
		}
	}
	for _, h := range contracts.AllHarnesses() {
		if !seen[h] {
			t.Fatalf("harness %q has no update target", h)
		}
	}
}

func TestHarnessUpdaterReportsNewerLatest(t *testing.T) {
	env := newFakeHarnessEnv()
	bin := filepath.Join(t.TempDir(), ".local", "bin", "claude")
	env.install(t, contracts.HarnessClaude, bin, "2.1.322 (Claude Code)")
	srv, _ := npmRegistry(t, map[string]string{"@anthropic-ai/claude-code": "2.1.323"})
	clock := newFakeClock()
	u := testUpdater(t, env, srv.URL, "http://invalid", clock)

	report := u.Check(context.Background(), false)
	if len(report.Harnesses) != 1 {
		t.Fatalf("got %d rows, want 1", len(report.Harnesses))
	}
	row := report.Harnesses[0]
	if row.Harness != contracts.HarnessClaude || row.Binary != bin {
		t.Fatalf("row = %+v", row)
	}
	if row.Installed != "2.1.322" || row.Latest != "2.1.323" {
		t.Fatalf("versions = %q -> %q", row.Installed, row.Latest)
	}
	if !row.UpdateAvailable || !row.Updatable {
		t.Fatalf("flags = %+v", row)
	}
	if row.Command != "claude update" {
		t.Fatalf("command = %q, want %q", row.Command, "claude update")
	}
	if !report.CheckedAt.Equal(clock.now()) {
		t.Fatalf("checkedAt = %v, want %v", report.CheckedAt, clock.now())
	}
}

func TestHarnessUpdaterIsCurrentWhenInstalledIsAhead(t *testing.T) {
	env := newFakeHarnessEnv()
	bin := filepath.Join(t.TempDir(), ".opencode", "bin", "opencode")
	env.install(t, contracts.HarnessOpenCode, bin, "opencode v2.0.29")
	srv, _ := npmRegistry(t, map[string]string{"opencode-ai": "1.19.5"})
	u := testUpdater(t, env, srv.URL, "http://invalid", newFakeClock())

	report := u.Check(context.Background(), false)
	if len(report.Harnesses) != 1 {
		t.Fatalf("got %d rows, want 1", len(report.Harnesses))
	}
	row := report.Harnesses[0]
	if row.Installed != "2.0.29" || row.Latest != "1.19.5" {
		t.Fatalf("versions = %q -> %q, want 2.0.29 -> 1.19.5", row.Installed, row.Latest)
	}
	if row.Reason != "" {
		t.Fatalf("unexpected reason: %q", row.Reason)
	}
	if row.UpdateAvailable {
		t.Fatalf("a newer installed version must not offer a downgrade: %+v", row)
	}
	if !row.Updatable || row.Command != "opencode upgrade" {
		t.Fatalf("row = %+v", row)
	}
}

func TestHarnessUpdaterCachesWithinTTL(t *testing.T) {
	env := newFakeHarnessEnv()
	bin := filepath.Join(t.TempDir(), ".local", "bin", "claude")
	env.install(t, contracts.HarnessClaude, bin, "2.1.322")
	srv, hits := npmRegistry(t, map[string]string{"@anthropic-ai/claude-code": "2.1.322"})
	clock := newFakeClock()
	u := testUpdater(t, env, srv.URL, "http://invalid", clock)

	for i := 0; i < 2; i++ {
		u.Check(context.Background(), false)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("npm hits = %d, want 1", got)
	}
	if got := env.probeCount(contracts.HarnessClaude); got != 1 {
		t.Fatalf("probes = %d, want 1", got)
	}

	clock.advance(30 * time.Minute)
	u.Check(context.Background(), false)
	if got := hits.Load(); got != 1 {
		t.Fatalf("npm hits inside TTL = %d, want 1", got)
	}

	clock.advance(31 * time.Minute)
	u.Check(context.Background(), false)
	if got := hits.Load(); got != 2 {
		t.Fatalf("npm hits after TTL = %d, want 2", got)
	}
	if got := env.probeCount(contracts.HarnessClaude); got != 2 {
		t.Fatalf("probes after TTL = %d, want 2", got)
	}
}

func TestHarnessUpdaterRefreshBypassesCache(t *testing.T) {
	env := newFakeHarnessEnv()
	bin := filepath.Join(t.TempDir(), ".local", "bin", "claude")
	env.install(t, contracts.HarnessClaude, bin, "2.1.322")
	srv, hits := npmRegistry(t, map[string]string{"@anthropic-ai/claude-code": "2.1.322"})
	u := testUpdater(t, env, srv.URL, "http://invalid", newFakeClock())

	u.Check(context.Background(), false)
	u.Check(context.Background(), true)

	if got := hits.Load(); got != 2 {
		t.Fatalf("npm hits = %d, want 2", got)
	}
	if got := env.probeCount(contracts.HarnessClaude); got != 2 {
		t.Fatalf("probes = %d, want 2", got)
	}
}

func TestHarnessUpdaterSkipsMissingBinaries(t *testing.T) {
	env := newFakeHarnessEnv()
	srv, hits := npmRegistry(t, map[string]string{})
	u := testUpdater(t, env, srv.URL, "http://invalid", newFakeClock())

	report := u.Check(context.Background(), false)
	if len(report.Harnesses) != 0 {
		t.Fatalf("rows = %+v, want none", report.Harnesses)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("npm hits = %d, want 0", got)
	}
}

// A neutral bin dir hides the package manager, so classification must see the
// EvalSymlinks target. This exercises checkOne's realPath wiring, not just the
// resolver: drop that call and every case below degrades to
// "installed outside a known package manager".
func TestHarnessUpdaterClassifiesThroughSymlinkTarget(t *testing.T) {
	cases := []struct {
		name        string
		harness     contracts.Harness
		installed   string
		npmPackage  string
		latest      string
		bin         string
		realPath    string
		wantCommand string
	}{
		{
			name:        "bun behind a neutral symlink",
			harness:     contracts.HarnessCodex,
			installed:   "1.2.3",
			npmPackage:  "@openai/codex",
			latest:      "1.2.4",
			bin:         "/Users/u/bin/codex",
			realPath:    "/Users/u/.bun/install/global/node_modules/@openai/codex/bin/codex",
			wantCommand: "bun i -g @openai/codex@latest",
		},
		{
			name:        "vite-plus behind a neutral symlink",
			harness:     contracts.HarnessCommandCode,
			installed:   "2.0.0",
			npmPackage:  "command-code",
			latest:      "2.0.1",
			bin:         "/Users/u/bin/commandcode",
			realPath:    "/Users/u/.local/share/vite-plus/js_runtime/node/24.21.0/lib/node_modules/command-code/bin/commandcode",
			wantCommand: "vp i -g command-code",
		},
		{
			name:        "homebrew pi behind a neutral symlink",
			harness:     contracts.HarnessPi,
			installed:   "1.5.2",
			npmPackage:  "@earendil-works/pi-coding-agent",
			latest:      "1.6.0",
			bin:         "/Users/u/bin/pi",
			realPath:    "/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent/bin/pi",
			wantCommand: "pi update",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newFakeHarnessEnv()
			env.install(t, tc.harness, tc.bin, tc.installed)
			env.mu.Lock()
			env.realPaths[tc.bin] = tc.realPath
			env.mu.Unlock()
			srv, _ := npmRegistry(t, map[string]string{tc.npmPackage: tc.latest})
			u := testUpdater(t, env, srv.URL, "http://invalid", newFakeClock())

			report := u.Check(context.Background(), false)
			if len(report.Harnesses) != 1 {
				t.Fatalf("got %d rows, want 1", len(report.Harnesses))
			}
			row := report.Harnesses[0]
			if row.Binary != tc.bin {
				t.Fatalf("binary = %q, want the LookPath path %q", row.Binary, tc.bin)
			}
			if !row.Updatable {
				t.Fatalf("not updatable through the symlink target: %+v", row)
			}
			if !row.UpdateAvailable {
				t.Fatalf("expected an available update: %+v", row)
			}
			if row.Command != tc.wantCommand {
				t.Fatalf("command = %q, want %q", row.Command, tc.wantCommand)
			}
			if row.Reason != "" {
				t.Fatalf("unexpected reason: %q", row.Reason)
			}
		})
	}
}

func TestHarnessUpdaterSurvivesLatestFetchFailure(t *testing.T) {
	env := newFakeHarnessEnv()
	bin := filepath.Join(t.TempDir(), ".local", "bin", "claude")
	env.install(t, contracts.HarnessClaude, bin, "2.1.322 (Claude Code)")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "registry down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	u := testUpdater(t, env, srv.URL, "http://invalid", newFakeClock())

	row := u.Check(context.Background(), false).Harnesses[0]
	if row.Installed != "2.1.322" {
		t.Fatalf("installed = %q, want 2.1.322", row.Installed)
	}
	if row.Latest != "" || row.UpdateAvailable {
		t.Fatalf("a failed lookup must not offer an update: %+v", row)
	}
	if row.Reason == "" {
		t.Fatalf("missing reason for a failed lookup: %+v", row)
	}
	if !row.Updatable || row.Command != "claude update" {
		t.Fatalf("command resolution is independent of the lookup: %+v", row)
	}
}

func TestHarnessUpdaterComparesCursorDateVersions(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		latest    string
		want      bool
	}{
		{name: "newer date", installed: "2026.09.28-64d2043", latest: "2026.11.11-043ef28", want: true},
		{name: "same date newer hash", installed: "2026.09.28-64d2043", latest: "2026.09.28-043ef28", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newFakeHarnessEnv()
			bin := filepath.Join(t.TempDir(), ".local", "bin", "cursor-agent")
			env.install(t, contracts.HarnessCursor, bin, tc.installed)
			srv := cursorInstallScript(t, tc.latest)
			u := testUpdater(t, env, "http://invalid", srv.URL, newFakeClock())

			row := u.Check(context.Background(), false).Harnesses[0]
			if row.UpdateAvailable != tc.want {
				t.Fatalf("updateAvailable = %v, want %v (%+v)", row.UpdateAvailable, tc.want, row)
			}
			if row.Installed != tc.installed || row.Latest != tc.latest {
				t.Fatalf("versions = %q -> %q", row.Installed, row.Latest)
			}
			if !row.Updatable || row.Command != "cursor-agent update" {
				t.Fatalf("row = %+v", row)
			}
		})
	}
}

func TestResolveUpgradeCommandByInstallPath(t *testing.T) {
	cases := []struct {
		name     string
		harness  contracts.Harness
		path     string
		realPath string
		wantOK   bool
		wantExe  string
		wantArgs []string
	}{
		{
			// The realpath of claude is a versioned binary, so the native verb
			// must run through the LookPath symlink, not the target.
			name:     "claude native through symlink",
			harness:  contracts.HarnessClaude,
			path:     "/Users/u/.local/bin/claude",
			realPath: "/Users/u/.local/share/claude/versions/2.1.322/claude",
			wantOK:   true,
			wantExe:  "/Users/u/.local/bin/claude",
			wantArgs: []string{"update"},
		},
		{
			name:     "cursor native",
			harness:  contracts.HarnessCursor,
			path:     "/Users/u/.local/share/cursor-agent/cursor-agent",
			wantOK:   true,
			wantExe:  "/Users/u/.local/share/cursor-agent/cursor-agent",
			wantArgs: []string{"update"},
		},
		{
			name:     "cursor agent alias",
			harness:  contracts.HarnessCursor,
			path:     "/Users/u/.local/bin/agent",
			wantOK:   true,
			wantExe:  "/Users/u/.local/bin/agent",
			wantArgs: []string{"update"},
		},
		{
			// The vp realpath also contains /node_modules/, so the vite-plus
			// check has to win or this becomes an npm install.
			name:     "vite-plus real install",
			harness:  contracts.HarnessCommandCode,
			path:     "/Users/u/.local/share/vite-plus/bin/commandcode",
			realPath: "/Users/u/.local/share/vite-plus/js_runtime/node/24.21.0/lib/node_modules/command-code/bin/commandcode",
			wantOK:   true,
			wantExe:  "vp",
			wantArgs: []string{"i", "-g", "command-code"},
		},
		{
			name:     "vite-plus legacy dir",
			harness:  contracts.HarnessCommandCode,
			path:     "/Users/u/.vite-plus/bin/commandcode",
			realPath: "/Users/u/.vite-plus/js_runtime/node/24.21.0/lib/node_modules/command-code/bin/commandcode",
			wantOK:   true,
			wantExe:  "vp",
			wantArgs: []string{"i", "-g", "command-code"},
		},
		{
			name:     "bun",
			harness:  contracts.HarnessCodex,
			path:     "/Users/u/.bun/bin/codex",
			realPath: "/Users/u/.bun/install/global/node_modules/@openai/codex/bin/codex",
			wantOK:   true,
			wantExe:  "bun",
			wantArgs: []string{"i", "-g", "@openai/codex@latest"},
		},
		{
			// The LookPath name carries no manager marker; only the symlink
			// target reveals bun.
			name:     "bun hidden behind a neutral symlink",
			harness:  contracts.HarnessCodex,
			path:     "/Users/u/bin/codex",
			realPath: "/Users/u/.bun/install/global/node_modules/@openai/codex/bin/codex",
			wantOK:   true,
			wantExe:  "bun",
			wantArgs: []string{"i", "-g", "@openai/codex@latest"},
		},
		{
			name:     "pnpm",
			harness:  contracts.HarnessCodex,
			path:     "/Users/u/Library/pnpm/codex",
			realPath: "/Users/u/Library/pnpm/global/5/node_modules/@openai/codex/bin/codex",
			wantOK:   true,
			wantExe:  "pnpm",
			wantArgs: []string{"add", "-g", "@openai/codex@latest"},
		},
		{
			name:    "npm global",
			harness: contracts.HarnessCodex,
			path:    "/usr/local/lib/node_modules/@openai/codex/bin/codex",
			wantOK:  true,
			wantExe: "npm",
			wantArgs: []string{
				"install", "-g", "--allow-scripts=@openai/codex", "@openai/codex@latest",
			},
		},
		{
			name:     "homebrew formula",
			harness:  contracts.HarnessCodex,
			path:     "/opt/homebrew/bin/codex",
			realPath: "/opt/homebrew/Cellar/codex/1.2.3/bin/codex",
			wantOK:   true,
			wantExe:  "brew",
			wantArgs: []string{"upgrade", "codex"},
		},
		{
			// Homebrew's pi is a symlink into the formula's node_modules; the
			// native verb is what upgrades it.
			name:     "homebrew pi native",
			harness:  contracts.HarnessPi,
			path:     "/opt/homebrew/bin/pi",
			realPath: "/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent/bin/pi",
			wantOK:   true,
			wantExe:  "/opt/homebrew/bin/pi",
			wantArgs: []string{"update"},
		},
		{
			name:    "homebrew prefix without a formula",
			harness: contracts.HarnessCommandCode,
			path:    "/opt/homebrew/bin/commandcode",
			wantOK:  false,
		},
		{
			name:    "package-managed cursor has no npm package",
			harness: contracts.HarnessCursor,
			path:    "/usr/local/lib/node_modules/cursor-agent/bin/cursor-agent",
			wantOK:  false,
		},
		{
			name:    "unknown install",
			harness: contracts.HarnessCommandCode,
			path:    "/Applications/Tools/bin/commandcode",
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, ok := lookupUpdateTarget(tc.harness)
			if !ok {
				t.Fatalf("no target for %q", tc.harness)
			}
			cmd, ok := resolveUpgradeCommand(target, tc.path, tc.realPath)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (cmd %+v)", ok, tc.wantOK, cmd)
			}
			if !ok {
				return
			}
			if cmd.exe != tc.wantExe {
				t.Fatalf("exe = %q, want %q", cmd.exe, tc.wantExe)
			}
			if strings.Join(cmd.args, " ") != strings.Join(tc.wantArgs, " ") {
				t.Fatalf("args = %q, want %q", cmd.args, tc.wantArgs)
			}
		})
	}
}

func TestHarnessUpdaterUpdateRunsOnlyAffected(t *testing.T) {
	env := newFakeHarnessEnv()
	claudeBin := filepath.Join(t.TempDir(), ".local", "bin", "claude")
	env.install(t, contracts.HarnessClaude, claudeBin, "2.1.322 (Claude Code)")
	env.install(t, contracts.HarnessPi, "/Users/u/.bun/bin/pi", "1.5.2")
	env.latestOutputs[contracts.HarnessClaude] = "2.1.323 (Claude Code)"
	srv, _ := npmRegistry(t, map[string]string{
		"@anthropic-ai/claude-code":       "2.1.323",
		"@earendil-works/pi-coding-agent": "1.5.2",
	})
	u := testUpdater(t, env, srv.URL, "http://invalid", newFakeClock())

	res, err := u.Update(context.Background(), nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != contracts.HarnessClaude {
		t.Fatalf("updated = %v, want [claude]", res.Updated)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("failed = %+v, want none", res.Failed)
	}
	calls := env.runCalls()
	if len(calls) != 1 {
		t.Fatalf("runs = %+v, want one", calls)
	}
	if calls[0].bin != claudeBin || strings.Join(calls[0].args, " ") != "update" {
		t.Fatalf("run = %+v, want the claude native update", calls[0])
	}
	if len(res.Report.Harnesses) != 2 {
		t.Fatalf("report rows = %+v", res.Report.Harnesses)
	}
	for _, row := range res.Report.Harnesses {
		if row.Harness == contracts.HarnessClaude && row.UpdateAvailable {
			t.Fatalf("report not refreshed: %+v", row)
		}
	}
}

func TestHarnessUpdaterUpdateContinuesAfterFailure(t *testing.T) {
	env := newFakeHarnessEnv()
	claudeBin := filepath.Join(t.TempDir(), ".local", "bin", "claude")
	cursorBin := filepath.Join(t.TempDir(), ".local", "bin", "cursor-agent")
	env.install(t, contracts.HarnessClaude, claudeBin, "2.1.322 (Claude Code)")
	env.install(t, contracts.HarnessCursor, cursorBin, "2026.09.28-64d2043")
	env.latestOutputs[contracts.HarnessCursor] = "2026.11.11-043ef28"
	env.runErrors[contracts.HarnessClaude] = errors.New("exit status 1")
	env.runOutput[contracts.HarnessClaude] = []byte("npm ERR! boom")
	srv, _ := npmRegistry(t, map[string]string{"@anthropic-ai/claude-code": "2.1.323"})
	cursorSrv := cursorInstallScript(t, "2026.11.11-043ef28")
	u := testUpdater(t, env, srv.URL, cursorSrv.URL, newFakeClock())

	res, err := u.Update(context.Background(), nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != contracts.HarnessCursor {
		t.Fatalf("updated = %v, want [cursor]", res.Updated)
	}
	if len(res.Failed) != 1 || res.Failed[0].Harness != contracts.HarnessClaude {
		t.Fatalf("failed = %+v", res.Failed)
	}
	if msg := res.Failed[0].Error; !strings.Contains(msg, "exit status 1") || !strings.Contains(msg, "npm ERR! boom") {
		t.Fatalf("failure message = %q", msg)
	}
	if calls := env.runCalls(); len(calls) != 2 {
		t.Fatalf("runs = %+v, want both harnesses attempted", calls)
	}

	rows := map[contracts.Harness]HarnessUpdate{}
	for _, row := range res.Report.Harnesses {
		rows[row.Harness] = row
	}
	if !rows[contracts.HarnessClaude].UpdateAvailable {
		t.Fatalf("failed harness should still be behind: %+v", rows[contracts.HarnessClaude])
	}
	if rows[contracts.HarnessCursor].UpdateAvailable {
		t.Fatalf("updated harness should be current: %+v", rows[contracts.HarnessCursor])
	}
}

// An explicit request names harnesses that Update must not filter on
// availability, but a harness already known to be current still must not run
// its (potentially minutes-long) upgrade command.
func TestHarnessUpdaterUpdateSkipsExplicitCurrentHarness(t *testing.T) {
	env := newFakeHarnessEnv()
	bin := "/Users/u/.bun/bin/pi"
	env.install(t, contracts.HarnessPi, bin, "1.5.2")
	srv, _ := npmRegistry(t, map[string]string{"@earendil-works/pi-coding-agent": "1.5.2"})
	u := testUpdater(t, env, srv.URL, "http://invalid", newFakeClock())

	res, err := u.Update(context.Background(), []contracts.Harness{contracts.HarnessPi})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(res.Updated) != 0 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v, want no changes", res)
	}
	if calls := env.runCalls(); len(calls) != 0 {
		t.Fatalf("runs = %+v, want none for a current harness", calls)
	}
	if len(res.Report.Harnesses) != 1 {
		t.Fatalf("report rows = %+v", res.Report.Harnesses)
	}
}

func TestHarnessUpdaterUpdateRejectsUnknownHarness(t *testing.T) {
	env := newFakeHarnessEnv()
	u := testUpdater(t, env, "http://invalid", "http://invalid", newFakeClock())

	if _, err := u.Update(context.Background(), []contracts.Harness{"nope"}); err == nil {
		t.Fatal("expected an error for an unknown harness")
	}
	if calls := env.runCalls(); len(calls) != 0 {
		t.Fatalf("runs = %+v, want none", calls)
	}
}
