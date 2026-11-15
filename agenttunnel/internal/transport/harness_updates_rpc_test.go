package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// rpcHarnessEnv fakes PATH, --version, and upgrade runs for the RPC tests.
type rpcHarnessEnv struct {
	mu            sync.Mutex
	bins          map[string]string
	harnessByPath map[string]contracts.Harness
	outputs       map[contracts.Harness]string
	latestOutputs map[contracts.Harness]string
	runs          []string
}

func newRPCHarnessEnv() *rpcHarnessEnv {
	return &rpcHarnessEnv{
		bins:          map[string]string{},
		harnessByPath: map[string]contracts.Harness{},
		outputs:       map[contracts.Harness]string{},
		latestOutputs: map[contracts.Harness]string{},
	}
}

func (e *rpcHarnessEnv) install(t *testing.T, h contracts.Harness, name, path, output string) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bins[name] = path
	e.harnessByPath[path] = h
	e.outputs[h] = output
}

func (e *rpcHarnessEnv) lookPath(name string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if path, ok := e.bins[name]; ok {
		return path, nil
	}
	return "", exec.ErrNotFound
}

func (e *rpcHarnessEnv) probe(_ context.Context, bin string, _ ...string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, ok := e.harnessByPath[bin]
	if !ok {
		return nil, fmt.Errorf("unexpected probe of %q", bin)
	}
	return []byte(e.outputs[h]), nil
}

func (e *rpcHarnessEnv) run(_ context.Context, bin string, args ...string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runs = append(e.runs, filepath.Base(bin)+" "+strings.Join(args, " "))
	if h, ok := e.harnessByPath[bin]; ok {
		if out, ok := e.latestOutputs[h]; ok {
			e.outputs[h] = out
		}
	}
	return nil, nil
}

func (e *rpcHarnessEnv) runCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.runs...)
}

func rpcNpmRegistry(t *testing.T, versions map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	return srv
}

func rpcUpdater(env *rpcHarnessEnv, npmBase string) *providers.HarnessUpdater {
	return providers.NewHarnessUpdater(providers.HarnessUpdaterOptions{
		HTTP:             http.DefaultClient,
		NpmBase:          npmBase,
		CursorInstallURL: "http://invalid",
		LookPath:         env.lookPath,
		Probe:            env.probe,
		Run:              env.run,
	})
}

type harnessUpdateRow struct {
	Harness         string `json:"harness"`
	Installed       string `json:"installed"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Updatable       bool   `json:"updatable"`
	Reason          string `json:"reason"`
	Command         string `json:"command"`
}

func TestHarnessUpdatesRPCReportsVersions(t *testing.T) {
	env := newRPCHarnessEnv()
	env.install(t, contracts.HarnessClaude, "claude", filepath.Join(t.TempDir(), ".local", "bin", "claude"), "2.1.322")
	env.install(t, contracts.HarnessPi, "pi", "/Users/u/.bun/bin/pi", "1.5.2")
	srv := rpcNpmRegistry(t, map[string]string{
		"@anthropic-ai/claude-code":       "2.1.323",
		"@earendil-works/pi-coding-agent": "1.5.2",
	})
	disp := NewDispatcher(DispatcherDeps{HarnessUpdates: rpcUpdater(env, srv.URL)})

	res, err := disp.Handle(context.Background(), Request{ID: "1", Method: "harness.updates"})
	if err != nil {
		t.Fatalf("harness.updates: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CheckedAt string             `json:"checkedAt"`
		Harnesses []harnessUpdateRow `json:"harnesses"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if got.CheckedAt == "" {
		t.Fatalf("checkedAt is empty: %s", raw)
	}
	if len(got.Harnesses) != 2 {
		t.Fatalf("rows = %s, want claude then pi", raw)
	}
	claude := got.Harnesses[0]
	if claude.Harness != "claude" || claude.Installed != "2.1.322" || claude.Latest != "2.1.323" {
		t.Fatalf("claude row = %+v", claude)
	}
	if !claude.UpdateAvailable || !claude.Updatable || claude.Command != "claude update" {
		t.Fatalf("claude flags = %+v", claude)
	}
	pi := got.Harnesses[1]
	if pi.Harness != "pi" || pi.UpdateAvailable || !pi.Updatable {
		t.Fatalf("pi row = %+v", pi)
	}
}

func TestHarnessUpdateRPCRunsOnlyAffected(t *testing.T) {
	env := newRPCHarnessEnv()
	env.install(t, contracts.HarnessClaude, "claude", filepath.Join(t.TempDir(), ".local", "bin", "claude"), "2.1.322")
	env.latestOutputs[contracts.HarnessClaude] = "2.1.323"
	srv := rpcNpmRegistry(t, map[string]string{"@anthropic-ai/claude-code": "2.1.323"})
	disp := NewDispatcher(DispatcherDeps{HarnessUpdates: rpcUpdater(env, srv.URL)})

	res, err := disp.Handle(context.Background(), Request{
		ID:     "1",
		Method: "harness.update",
		Params: json.RawMessage(`{"harnesses":[]}`),
	})
	if err != nil {
		t.Fatalf("harness.update: %v", err)
	}
	result, ok := res.(providers.UpdateResult)
	if !ok {
		t.Fatalf("res = %#v, want providers.UpdateResult", res)
	}
	if len(result.Updated) != 1 || result.Updated[0] != contracts.HarnessClaude {
		t.Fatalf("updated = %v", result.Updated)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("failed = %+v", result.Failed)
	}
	if calls := env.runCalls(); len(calls) != 1 || calls[0] != "claude update" {
		t.Fatalf("runs = %q, want one claude update", calls)
	}
	if len(result.Report.Harnesses) != 1 || result.Report.Harnesses[0].UpdateAvailable {
		t.Fatalf("report not refreshed: %+v", result.Report.Harnesses)
	}
}

func TestHarnessUpdateRPCRejectsUnknownHarness(t *testing.T) {
	env := newRPCHarnessEnv()
	disp := NewDispatcher(DispatcherDeps{HarnessUpdates: rpcUpdater(env, "http://invalid")})

	_, err := disp.Handle(context.Background(), Request{
		ID:     "1",
		Method: "harness.update",
		Params: json.RawMessage(`{"harnesses":["bogus"]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("err = %v, want an unknown harness error", err)
	}
	if calls := env.runCalls(); len(calls) != 0 {
		t.Fatalf("runs = %q, want none", calls)
	}
}

func TestHarnessUpdatesRPCWithoutService(t *testing.T) {
	disp := NewDispatcher(DispatcherDeps{})

	res, err := disp.Handle(context.Background(), Request{ID: "1", Method: "harness.updates"})
	if err != nil {
		t.Fatalf("harness.updates: %v", err)
	}
	report, ok := res.(providers.UpdateReport)
	if !ok {
		t.Fatalf("res = %#v, want providers.UpdateReport", res)
	}
	if report.Harnesses == nil || len(report.Harnesses) != 0 {
		t.Fatalf("harnesses = %#v, want an empty list", report.Harnesses)
	}
	if report.CheckedAt.IsZero() {
		t.Fatal("checkedAt is zero")
	}

	if _, err := disp.Handle(context.Background(), Request{ID: "2", Method: "harness.update"}); err == nil {
		t.Fatal("harness.update should fail when no updater is configured")
	}
}
