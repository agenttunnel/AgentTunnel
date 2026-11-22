package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

const (
	runWaitLeadID = "agent-orchestrator"
	runWaitExecID = "agent-exec"
	runWaitThread = "th_1"
)

// finalRunState is the thread.get the scripted daemon reports once the turn is
// over: the lead and one executor, plus the orchestrator's summary when given.
func finalRunState(leadStatus, execStatus contracts.AgentStatus, summary string) domain.ThreadState {
	st := domain.ThreadState{
		Agents: map[contracts.AgentID]contracts.Agent{
			runWaitLeadID: {ID: runWaitLeadID, Name: "lead", Role: "orchestrator", ThreadID: runWaitThread, Status: leadStatus},
			runWaitExecID: {
				ID: runWaitExecID, Name: "builder", Role: "executor", ThreadID: runWaitThread,
				ParentAgentID: runWaitLeadID, Status: execStatus,
			},
		},
		AgentOrder: []contracts.AgentID{runWaitLeadID, runWaitExecID},
	}
	if summary != "" {
		st.Messages = []domain.Message{{Author: "orchestrator", Text: summary}}
	}
	return st
}

// newRunWaitServer scripts a daemon for `run --wait`: the roster the CLI reads
// when the wait starts (initial, only the agents listed) and the final state.
func newRunWaitServer(t *testing.T, initial map[string]contracts.AgentStatus, final domain.ThreadState) *scriptedServer {
	t.Helper()
	s := newScriptedServer()
	roster := []map[string]any{}
	for _, id := range []string{runWaitLeadID, runWaitExecID} {
		if status, ok := initial[id]; ok {
			roster = append(roster, map[string]any{"agent": map[string]any{"id": id, "status": status}})
		}
	}
	s.results["thread.subscribe"] = map[string]any{"subscribed": runWaitThread}
	s.results["agents.list"] = roster
	s.results["thread.get"] = final
	return s
}

func queueRunStatus(s *scriptedServer, agent contracts.AgentID, status contracts.AgentStatus) {
	s.queue(threadFrame{threadID: runWaitThread, typ: contracts.EvtAgentStatusChanged, payload: contracts.AgentStatusChanged{
		AgentID: agent, Status: status,
	}})
}

func runWaitEnv(t *testing.T) (*Env, *strings.Builder, *strings.Builder) {
	t.Helper()
	var out, errOut strings.Builder
	return &Env{Out: &out, Err: &errOut, JSON: true, HomeDir: t.TempDir()}, &out, &errOut
}

// driveRunWait runs awaitRunResult against the scripted daemon. When push is
// set, it waits for the subscription and then delivers the queued frames, so
// every status arrives as a live event rather than through a poll.
func driveRunWait(t *testing.T, s *scriptedServer, env *Env, c *Client, opts runWaitOptions, push bool) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- awaitRunResult(context.Background(), env, c, runResult{ThreadID: runWaitThread}, opts)
	}()
	if push {
		select {
		case <-s.subscribed:
		case <-time.After(5 * time.Second):
			t.Fatal("run --wait never subscribed")
		}
		s.pushNow(t)
	}
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("run --wait did not return")
		return nil
	}
}

// decodeRunWait asserts stdout is exactly one JSON document and returns it.
func decodeRunWait(t *testing.T, out *strings.Builder) runWaitResult {
	t.Helper()
	var got runWaitResult
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out.String())
	}
	return got
}

// usageCode returns the exit code an error maps to, or -1 for a plain error.
func usageCode(err error) int {
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return -1
}

func TestRunWaitSucceededPrintsOneJSONDocumentAndExitsZero(t *testing.T) {
	s := newRunWaitServer(t,
		map[string]contracts.AgentStatus{runWaitLeadID: contracts.AgentRunning, runWaitExecID: contracts.AgentRunning},
		finalRunState(contracts.AgentSucceeded, contracts.AgentSucceeded, "all done"))
	queueRunStatus(s, runWaitExecID, contracts.AgentSucceeded)
	queueRunStatus(s, runWaitLeadID, contracts.AgentSucceeded)
	c := dialScripted(t, s)
	env, out, errOut := runWaitEnv(t)

	// The poll and the start grace never fire: the end comes from events alone.
	err := driveRunWait(t, s, env, c, runWaitOptions{pollEvery: time.Hour, startBound: time.Hour}, true)
	if code := exitCode(env, err); code != 0 {
		t.Fatalf("exit = %d, want 0 (err %v)", code, err)
	}
	got := decodeRunWait(t, out)
	if got.ThreadID != runWaitThread || got.Status != "succeeded" || got.Summary != "all done" || got.Note != "" {
		t.Fatalf("result = %+v", got)
	}
	if len(got.Agents) != 2 || got.Agents[0].ID != runWaitLeadID || got.Agents[0].Role != "orchestrator" ||
		got.Agents[1].Name != "builder" || got.Agents[1].Status != "succeeded" {
		t.Fatalf("agents = %+v", got.Agents)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

func TestRunWaitFailedAgentExitsOneWithTheFailure(t *testing.T) {
	s := newRunWaitServer(t,
		map[string]contracts.AgentStatus{runWaitLeadID: contracts.AgentRunning, runWaitExecID: contracts.AgentRunning},
		finalRunState(contracts.AgentSucceeded, contracts.AgentFailed, "builder failed"))
	queueRunStatus(s, runWaitExecID, contracts.AgentFailed)
	queueRunStatus(s, runWaitLeadID, contracts.AgentSucceeded)
	c := dialScripted(t, s)
	env, out, _ := runWaitEnv(t)

	err := driveRunWait(t, s, env, c, runWaitOptions{pollEvery: time.Hour, startBound: time.Hour}, true)
	if code := exitCode(env, err); code != 1 {
		t.Fatalf("exit = %d, want 1 (err %v)", code, err)
	}
	got := decodeRunWait(t, out)
	if got.Status != "failed" || got.Summary != "builder failed" {
		t.Fatalf("result = %+v", got)
	}
	if len(got.Agents) != 2 || got.Agents[1].Status != "failed" {
		t.Fatalf("agents = %+v, want the failed executor listed", got.Agents)
	}
}

func TestRunWaitTimeoutLeavesTheTurnRunningAndSaysSo(t *testing.T) {
	s := newRunWaitServer(t,
		map[string]contracts.AgentStatus{runWaitLeadID: contracts.AgentRunning},
		finalRunState(contracts.AgentRunning, contracts.AgentPending, ""))
	c := dialScripted(t, s)
	env, out, _ := runWaitEnv(t)

	// No event is ever queued, so only the injected timeout can end the wait.
	err := driveRunWait(t, s, env, c, runWaitOptions{
		timeout: 20 * time.Millisecond, pollEvery: time.Hour, startBound: time.Hour,
	}, false)
	if code := exitCode(env, err); code != 1 {
		t.Fatalf("exit = %d, want 1 (err %v)", code, err)
	}
	got := decodeRunWait(t, out)
	if got.Status != "timeout" {
		t.Fatalf("status = %q, want timeout", got.Status)
	}
	if !strings.Contains(got.Note, "still running") || !strings.Contains(got.Note, runWaitThread) {
		t.Fatalf("note = %q, want it to say the turn is still running", got.Note)
	}
}

func TestRunWaitNoTurnStartedFailsWithAReason(t *testing.T) {
	s := newRunWaitServer(t, nil, domain.ThreadState{})
	c := dialScripted(t, s)
	env, out, _ := runWaitEnv(t)

	err := driveRunWait(t, s, env, c, runWaitOptions{
		startBound: 20 * time.Millisecond, pollEvery: time.Hour,
	}, false)
	if code := exitCode(env, err); code != 1 {
		t.Fatalf("exit = %d, want 1 (err %v)", code, err)
	}
	got := decodeRunWait(t, out)
	if got.Status != "failed" || !strings.Contains(got.Note, "did not start") {
		t.Fatalf("result = %+v", got)
	}
	if got.Agents == nil || len(got.Agents) != 0 {
		t.Fatalf("agents = %#v, want an empty array", got.Agents)
	}
}

// A start the daemon has recorded (Turn 1) whose lead is not on the roster yet
// when the start bound fires must not be reported as never started: the bound
// asks the daemon, sees the turn, and keeps waiting until the lead's events end it.
func TestRunWaitSlowStartIsNotReportedAsNoTurn(t *testing.T) {
	final := finalRunState(contracts.AgentSucceeded, contracts.AgentSucceeded, "late start finished")
	final.Turn = 1
	s := newRunWaitServer(t, nil, final)
	queueRunStatus(s, runWaitExecID, contracts.AgentSucceeded)
	queueRunStatus(s, runWaitLeadID, contracts.AgentSucceeded)
	c := dialScripted(t, s)
	env, out, _ := runWaitEnv(t)

	done := make(chan error, 1)
	go func() {
		done <- awaitRunResult(context.Background(), env, c, runResult{ThreadID: runWaitThread},
			runWaitOptions{pollEvery: time.Hour, startBound: time.Millisecond})
	}()
	select {
	case <-s.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("run --wait never subscribed")
	}
	// The bound fired and the daemon was asked. Only then do the lead's events
	// arrive, so the wait must have survived the bound to see them.
	s.waitCall(t, "thread.get")
	s.pushNow(t)

	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run --wait did not return")
	}
	if code := exitCode(env, err); code != 0 {
		t.Fatalf("exit = %d, want 0 (err %v)", code, err)
	}
	got := decodeRunWait(t, out)
	if got.Status != "succeeded" || got.Note != "" {
		t.Fatalf("result = %+v, want a succeeded turn", got)
	}
}

// The production bound must cover a healthy start on a large repo, which the old
// 15s grace did not.
func TestRunStartBoundCoversASlowWorktreeCut(t *testing.T) {
	if runStartBound < 2*time.Minute {
		t.Fatalf("runStartBound = %v, want at least 2m", runStartBound)
	}
}

func TestRunWaitUsageRejectionsNeverConnect(t *testing.T) {
	// A closed port: any path that reaches Connect fails with a connect error
	// (exit 1), so exit 2 proves the rejection happened first.
	srv := httptest.NewServer(nil)
	closed := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	srv.Close()

	tmpl := filepath.Join(t.TempDir(), "nightly.md")
	if err := os.WriteFile(tmpl, []byte("audit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		json bool
		args []string
		want string
	}{
		{"wait without json", false, []string{"--wait", tmpl}, "add --json"},
		{"wait with follow", true, []string{"--wait", "-f", tmpl}, "already follows"},
		{"timeout without wait", true, []string{"--timeout", "5m", tmpl}, "only applies with --wait"},
		{"bad timeout", true, []string{"--wait", "--timeout", "soon", tmpl}, "--timeout wants a duration"},
		{"negative timeout", true, []string{"--wait", "--timeout", "-5s", tmpl}, "--timeout wants a duration"},
		{"timeout missing value", true, []string{"--wait", "--timeout"}, "requires a value"},
		{"json follow stays rejected", true, []string{"-f", tmpl}, "cannot follow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut strings.Builder
			env := &Env{Out: &out, Err: &errOut, JSON: tc.json, HomeDir: t.TempDir(), DaemonURL: closed}
			err := runJob(context.Background(), env, tc.args)
			if code := usageCode(err); code != 2 {
				t.Fatalf("exit = %d, want 2 (err %v)", code, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestRunWaitTimeoutFlagParsesDurations(t *testing.T) {
	cases := []struct {
		args []string
		want time.Duration
	}{
		{[]string{"--wait", "t"}, defaultRunWaitTimeout},
		{[]string{"--wait", "--timeout", "90s", "t"}, 90 * time.Second},
		{[]string{"--wait", "--timeout=2m", "t"}, 2 * time.Minute},
		{[]string{"--wait", "--timeout", "0", "t"}, 0},
	}
	for _, tc := range cases {
		opts, err := parseRunArgs(tc.args)
		if err != nil {
			t.Fatalf("parseRunArgs(%v): %v", tc.args, err)
		}
		if !opts.wait || opts.timeout != tc.want {
			t.Fatalf("parseRunArgs(%v) = wait %v timeout %v, want %v", tc.args, opts.wait, opts.timeout, tc.want)
		}
	}
}

// TestRunWaitThroughRunJobPrintsTheResult drives the whole command: the template
// is read, the project and thread are created, the turn starts detached, and the
// wait reports the final state as one JSON document.
func TestRunWaitThroughRunJobPrintsTheResult(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	tmpl := filepath.Join(dir, "nightly.md")
	if err := os.WriteFile(tmpl, []byte("---\nname: nightly\n---\naudit the dependencies\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newRunWaitServer(t,
		map[string]contracts.AgentStatus{runWaitLeadID: contracts.AgentSucceeded, runWaitExecID: contracts.AgentSucceeded},
		finalRunState(contracts.AgentSucceeded, contracts.AgentSucceeded, "job finished"))
	s.results["project.list"] = []projectsRow{}
	s.results["project.create"] = projectsCreateReply{Project: projectsRow{ID: "proj_1", Name: "scratch", Path: dir}}
	s.results["thread.create"] = runThreadCreateReply{ThreadID: runWaitThread}
	s.results["thread.turn.start"] = map[string]any{}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	var out, errOut strings.Builder
	env := &Env{
		Out: &out, Err: &errOut, JSON: true, HomeDir: t.TempDir(),
		DaemonURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws",
	}
	err := runJob(context.Background(), env, []string{"--wait", "--timeout", "1m", tmpl})
	if code := exitCode(env, err); code != 0 {
		t.Fatalf("exit = %d, want 0 (err %v, stderr %q)", code, err, errOut.String())
	}
	got := decodeRunWait(t, &out)
	if got.ThreadID != runWaitThread || got.Status != "succeeded" || got.Summary != "job finished" {
		t.Fatalf("result = %+v", got)
	}
	turnStart := s.waitCall(t, "thread.turn.start")
	if !strings.Contains(string(turnStart.params), `"detach":true`) {
		t.Fatalf("turn.start params = %s, want a detached turn", turnStart.params)
	}
}
