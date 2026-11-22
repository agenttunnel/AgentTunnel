package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// headlessEnv returns an Env with separate stdout and stderr buffers, so a
// test can assert which stream a line landed on.
func headlessEnv() (*Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	return &Env{Out: &out, Err: &errBuf}, &out, &errBuf
}

// stampedFrame is a pushed thread.event with its envelope time. The
// scriptedServer's own frames carry no "at", so these tests push their own.
type stampedFrame struct {
	at    time.Time
	frame threadFrame
}

// pushStamped waits for the follower to subscribe, then writes the frames in
// order to every connected client.
func pushStamped(t *testing.T, s *scriptedServer, frames ...stampedFrame) {
	t.Helper()
	select {
	case <-s.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("client never subscribed")
	}
	s.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, f := range frames {
		params, _ := json.Marshal(map[string]any{
			"threadId": f.frame.threadID, "type": f.frame.typ, "payload": f.frame.payload, "at": f.at,
		})
		out, _ := json.Marshal(map[string]any{"method": "thread.event", "params": json.RawMessage(params)})
		for _, c := range conns {
			_ = c.Write(context.Background(), websocket.MessageText, out)
		}
	}
}

// awaitFollow returns the follow's result, failing rather than hanging if it
// never ends.
func awaitFollow(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not end")
		return nil
	}
}

func TestFollowExitsNonZeroWhenAnAgentFails(t *testing.T) {
	for _, status := range []contracts.AgentStatus{contracts.AgentFailed, contracts.AgentCancelled} {
		t.Run(string(status), func(t *testing.T) {
			s := newScriptedServer()
			s.results["agents.list"] = []map[string]any{
				{"agent": map[string]any{"id": "agent-exec", "status": "running"}},
			}
			s.results["thread.subscribe"] = map[string]any{"subscribed": "th_1"}
			s.results["thread.logs"] = []contracts.LogEntry{}
			c := dialScripted(t, s)
			env, out, _ := headlessEnv()

			envelope := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
			done := make(chan error, 1)
			go func() {
				done <- streamLogs(context.Background(), env, c, threadListRow{ID: "th_1"}, followOptions{lines: 10, follow: true})
			}()
			pushStamped(t, s,
				stampedFrame{at: envelope, frame: threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
					AgentID: "agent-exec", Kind: contracts.LogText, Text: "tests failed",
				}}},
				stampedFrame{at: envelope, frame: threadFrame{threadID: "th_1", typ: contracts.EvtAgentStatusChanged, payload: contracts.AgentStatusChanged{
					AgentID: "agent-exec", Status: status, Reason: "exit 1",
				}}},
			)

			err := awaitFollow(t, done)
			if got := exitCode(env, err); got != 1 {
				t.Fatalf("exit code = %d, want 1 (err %v)", got, err)
			}
			// Lines carry the envelope time, not the time the client printed them.
			stamp := envelope.Local().Format("15:04:05")
			for _, want := range []string{
				stamp + " agent-exec text tests failed",
				stamp + " agent-exec status " + string(status) + " (exit 1)",
			} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestFollowWithNoTurnWritesToStderrAndExitsNonZero(t *testing.T) {
	s := newScriptedServer()
	s.results["agents.list"] = []map[string]any{}
	s.results["thread.subscribe"] = map[string]any{"subscribed": "th_1"}
	s.results["thread.logs"] = []contracts.LogEntry{}
	c := dialScripted(t, s)
	env, out, errBuf := headlessEnv()

	// Shortened intervals take the no-turn path without waiting out the
	// production grace; the poll ticks are the only clock involved.
	done := make(chan error, 1)
	go func() {
		done <- streamLogs(context.Background(), env, c, threadListRow{ID: "th_1"}, followOptions{
			lines: 10, follow: true, pollEvery: time.Millisecond, startGrace: time.Nanosecond,
		})
	}()

	err := awaitFollow(t, done)
	if got := exitCode(env, err); got != 1 {
		t.Fatalf("exit code = %d, want 1 (err %v)", got, err)
	}
	if !strings.Contains(errBuf.String(), "no turn started") {
		t.Fatalf("stderr missing the no-turn line:\n%s", errBuf.String())
	}
	if strings.Contains(out.String(), "no turn started") {
		t.Fatalf("the no-turn line must not reach stdout:\n%s", out.String())
	}
}

func TestJSONWithFollowIsRejectedBeforeAnyWork(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(env *Env) error{
		"logs": func(env *Env) error { return runLogs(ctx, env, []string{"th_1", "-f"}) },
		"run":  func(env *Env) error { return runJob(ctx, env, []string{"nightly", "--follow"}) },
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			env, _, errBuf := headlessEnv()
			env.JSON = true
			// Isolate from any real daemon: an empty state dir and a closed
			// port. The rejection must come before any connect; if it ever
			// stops doing so, the connect fails here and the code is 1, not 2.
			env.HomeDir = t.TempDir()
			env.DaemonURL = "ws://127.0.0.1:1/ws"
			t.Setenv("AT_DAEMON_URL", "")
			t.Setenv("AT_TOKEN", "")
			err := run(env)
			if got := exitCode(env, err); got != 2 {
				t.Fatalf("exit code = %d, want 2 usage (err %v)", got, err)
			}
			if !strings.Contains(errBuf.String(), "cannot follow") {
				t.Fatalf("usage message missing:\n%s", errBuf.String())
			}
		})
	}
}

func TestAttachExitsNonZeroWhenTheTurnFails(t *testing.T) {
	s := attachTestServer()
	c := dialScripted(t, s)
	env, out, _ := headlessEnv()

	in, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	envelope := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	done := make(chan error, 1)
	go func() {
		done <- attachSession(context.Background(), env, c, threadListRow{ID: "th_1", Title: "job"}, in)
	}()
	pushStamped(t, s,
		stampedFrame{at: envelope, frame: threadFrame{threadID: "th_1", typ: contracts.EvtAgentStatusChanged, payload: contracts.AgentStatusChanged{
			AgentID: "agent-orchestrator", Status: contracts.AgentFailed,
		}}},
	)

	err := awaitFollow(t, done)
	if got := exitCode(env, err); got != 1 {
		t.Fatalf("exit code = %d, want 1 (err %v)", got, err)
	}
	stamp := envelope.Local().Format("15:04:05")
	for _, want := range []string{stamp + " agent-orchestrator status failed", "-- turn finished --"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("attach output missing %q:\n%s", want, out.String())
		}
	}
}

// A failure already recorded when the follow starts belongs to an earlier turn
// on the same thread. It must not fail this follow unless the agent moves again
// during the follow.
func TestTurnOutcomeIgnoresAFailureThatPredatesTheFollow(t *testing.T) {
	lead := contracts.AgentID("agent-orchestrator")
	tracker := newTurnTracker(map[string]contracts.AgentStatus{string(lead): contracts.AgentFailed}, true)
	if tracker.failed() {
		t.Fatal("a failure recorded at follow start must not fail this follow")
	}

	// The next turn's lead runs and succeeds: the old failure is gone.
	tracker.observe(string(lead), contracts.AgentRunning)
	tracker.observe(string(lead), contracts.AgentSucceeded)
	if tracker.failed() {
		t.Fatal("a succeeded lead must not fail the follow")
	}

	// The next turn's lead runs and fails: it moved during the follow, so the
	// follow fails even though its final status equals the start status.
	retry := newTurnTracker(map[string]contracts.AgentStatus{string(lead): contracts.AgentFailed}, true)
	retry.observe(string(lead), contracts.AgentRunning)
	retry.observe(string(lead), contracts.AgentFailed)
	if !retry.failed() {
		t.Fatal("a lead that ran and failed during the follow must fail it")
	}
}

// dropConnections closes every client connection from the daemon side, the way
// a daemon restart or a network drop would, leaving the server listening.
func dropConnections(s *scriptedServer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		_ = c.CloseNow()
	}
}

// A turn whose event stream drops before any terminal status has an unknown
// outcome. It must exit non-zero with a stderr line, never read as success.
func TestFollowFailsWhenTheEventStreamDropsMidTurn(t *testing.T) {
	s := newScriptedServer()
	s.results["agents.list"] = []map[string]any{
		{"agent": map[string]any{"id": "agent-orchestrator", "status": "running"}},
	}
	s.results["thread.subscribe"] = map[string]any{"subscribed": "th_1"}
	s.results["thread.logs"] = []contracts.LogEntry{}
	c := dialScripted(t, s)
	env, out, errBuf := headlessEnv()

	done := make(chan error, 1)
	go func() {
		done <- streamLogs(context.Background(), env, c, threadListRow{ID: "th_1"}, followOptions{lines: 10, follow: true})
	}()

	// agents.list is the last call before the live loop, so once it is recorded
	// the follower is listening. The turn is still running when the link drops.
	s.waitCall(t, "agents.list")
	pushStamped(t, s, stampedFrame{at: time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC), frame: threadFrame{
		threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
			AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "mid-turn line",
		},
	}})
	dropConnections(s)

	err := awaitFollow(t, done)
	if got := exitCode(env, err); got != 1 {
		t.Fatalf("exit code = %d, want 1 (err %v)", got, err)
	}
	if !strings.Contains(errBuf.String(), "event stream closed before the turn ended") {
		t.Fatalf("stderr missing the dropped-stream line:\n%s", errBuf.String())
	}
	if !strings.Contains(out.String(), "mid-turn line") {
		t.Fatalf("output missing the line sent before the drop:\n%s", out.String())
	}
}
