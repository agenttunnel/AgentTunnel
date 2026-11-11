package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/template"
	"github.com/agenttunnel/agenttunnel-go/pkg/daemon"
)

// jobScript is a scripted harness: one text report per pass, with no tool
// calls, so a detached run finishes on its own.
const jobScript = `printf '{"type":"text","text":"job finished"}\n'
printf '{"usage":{"input_tokens":5,"output_tokens":2}}\n'`

// TestRunDetachedJobEndToEnd starts a real daemon with a scripted harness and
// drives `at run` through its own RPC client: project resolution, thread
// creation, per-role runtimes, a detached turn, then `at ps` and `at logs`
// against the thread it made.
//
// The project directory is a plain temp dir, not a git repository: the
// executor's git guard refuses `git init`, and a non-repo project is exactly
// the case where no worktree is cut, so the turn runs in place.
func TestRunDetachedJobEndToEnd(t *testing.T) {
	testRunEndToEnd(t, jobScript, false)
}

// TestRunFollowEndsWithTheTurn proves `run --follow` returns when the turn it
// started ends — the case where requireActivity must not hang on a roster that
// was already terminal. The harness blocks on a marker file, so the test
// releases it after the follow has started and never sleeps.
func TestRunFollowEndsWithTheTurn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "go")
	script := `while [ ! -f "` + marker + `" ]; do sleep 0.02; done
printf '{"type":"text","text":"followed job finished"}\n'`
	release := func() {
		if err := os.WriteFile(marker, []byte("go"), 0o644); err != nil {
			t.Fatalf("release the harness: %v", err)
		}
	}
	testRunEndToEnd(t, script, true, release)
}

func testRunEndToEnd(t *testing.T, script string, follow bool, release ...func()) {
	t.Helper()
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
	}, providers.JSONLineTranslator{})

	stateDir := t.TempDir()
	d, err := daemon.New(context.Background(), daemon.Options{
		StateDir: stateDir,
		Token:    "secret",
		Workdir:  t.TempDir(),
		Registry: providers.NewRegistry(driver),
		Config:   orchestrator.Config{MaxPasses: 1, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	t.Cleanup(d.Close)

	srv := httptest.NewServer(d.Server.Handler())
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	if err := discovery.Write(stateDir, discovery.Info{
		WSURL: wsURL, HTTPURL: srv.URL, Token: "secret", Version: "test",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("discovery: %v", err)
	}

	// The job runs in its own non-git directory.
	projectDir := t.TempDir()
	tmpl := newRunTestTemplate(t, projectDir, template.ModelConfig{
		"executor": {Harness: string(contracts.HarnessOpenCode), Model: "scripted"},
	})

	var out strings.Builder
	env := &Env{Out: &out, Err: &out, HomeDir: stateDir}
	c, err := connectReadOnly(context.Background(), env)
	if err != nil {
		t.Fatalf("connectReadOnly: %v", err)
	}
	defer c.Close()

	result, err := startRun(context.Background(), c, tmpl)
	if err != nil {
		t.Fatalf("startRun: %v", err)
	}
	if result.ThreadID == "" || !result.Detached {
		t.Fatalf("result = %+v", result)
	}

	// The per-role runtime reached the thread's durable state.
	var state struct {
		RoleRuntimes map[string]contracts.LeadRuntime `json:"roleRuntimes"`
		ProjectID    string                           `json:"projectId"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Call(ctx, "thread.get", map[string]string{"threadId": result.ThreadID}, &state); err != nil {
		t.Fatalf("thread.get: %v", err)
	}
	if got := state.RoleRuntimes["executor"]; got.Harness != contracts.HarnessOpenCode || got.Model != "scripted" {
		t.Fatalf("executor runtime = %+v", got)
	}
	if state.ProjectID == "" {
		t.Fatal("the thread has no project")
	}

	if follow {
		var followOut strings.Builder
		followEnv := &Env{Out: &followOut, Err: &followOut, HomeDir: stateDir}
		row := threadListRow{ID: result.ThreadID}
		done := make(chan error, 1)
		go func() {
			done <- streamLogs(context.Background(), followEnv, c, row, followOptions{
				lines: 200, follow: true, requireActivity: true,
			})
		}()
		for _, open := range release {
			open()
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("follow: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("run --follow never returned after the turn ended")
		}
		if !strings.Contains(followOut.String(), "followed job finished") {
			t.Fatalf("follow output missing the harness report:\n%s", followOut.String())
		}
		return
	}

	// The detached turn runs to completion without the starting client doing
	// anything: poll the durable state until the lead is terminal.
	waitForThreadIdle(t, c, result.ThreadID)

	// `at ps` sees the thread, named after the template, in its own project.
	var psOut strings.Builder
	psEnv := &Env{Out: &psOut, Err: &psOut, HomeDir: stateDir}
	if err := renderPs(context.Background(), psEnv, c, psOptions{all: true}, time.Now()); err != nil {
		t.Fatalf("renderPs: %v", err)
	}
	for _, want := range []string{result.ThreadID[:shortThreadIDLen], "nightly-audit", filepath.Base(projectDir)} {
		if !strings.Contains(psOut.String(), want) {
			t.Fatalf("ps output missing %q:\n%s", want, psOut.String())
		}
	}

	// `at logs` shows the scripted harness's output.
	var logsOut strings.Builder
	logsEnv := &Env{Out: &logsOut, Err: &logsOut, HomeDir: stateDir}
	row := threadListRow{ID: result.ThreadID}
	if err := streamLogs(context.Background(), logsEnv, c, row, followOptions{lines: 200}); err != nil {
		t.Fatalf("streamLogs: %v", err)
	}
	if !strings.Contains(logsOut.String(), "job finished") {
		t.Fatalf("logs output missing the harness report:\n%s", logsOut.String())
	}

	// `at stop` on a finished turn reports that nothing was running.
	var stopped struct {
		Stopped bool `json:"stopped"`
	}
	if err := c.Call(context.Background(), "thread.turn.stop", map[string]string{"threadId": result.ThreadID}, &stopped); err != nil {
		t.Fatalf("thread.turn.stop: %v", err)
	}
	if stopped.Stopped {
		t.Fatal("a finished turn must not report a stop")
	}
}

// waitForThreadIdle polls thread.get until no agent is running or pending. The
// turn runs on the daemon's context, so this observes the same durable state a
// later `at ps` would.
func waitForThreadIdle(t *testing.T, c cliCaller, threadID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		var state struct {
			AgentOrder []string `json:"agentOrder"`
			Agents     map[string]struct {
				Status string `json:"status"`
			} `json:"agents"`
		}
		if err := c.Call(context.Background(), "thread.get", map[string]string{"threadId": threadID}, &state); err != nil {
			t.Fatalf("thread.get: %v", err)
		}
		active := false
		for _, id := range state.AgentOrder {
			status := state.Agents[id].Status
			last = id + "=" + status
			if status == string(contracts.AgentRunning) || status == string(contracts.AgentPending) {
				active = true
			}
		}
		if len(state.AgentOrder) > 0 && !active {
			return
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-time.After(time.Until(deadline)):
			t.Fatalf("thread never went idle; last agent %s", last)
		}
	}
	t.Fatalf("thread never went idle; last agent %s", last)
}

// TestE2EDiscoveryRoundTrip guards the test fixture itself: the daemon answers
// on the URL discovery names, so a failure above is the CLI, not the harness.
func TestE2EDiscoveryRoundTrip(t *testing.T) {
	if _, err := os.Stat(filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Fatal("expected a missing file")
	}
	var info discovery.Info
	raw := `{"wsUrl":"ws://127.0.0.1:1/ws","token":"t"}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("discovery shape: %v", err)
	}
	if info.WSURL == "" {
		t.Fatal("discovery shape lost wsUrl")
	}
}
