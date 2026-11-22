package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// A follower whose bound fires while the daemon already shows a begun turn must
// keep waiting, then report the turn's real outcome. The old fixed grace ended
// the follow at the first poll after 15s with "no turn started".
func TestFollowSlowStartIsNotReportedAsNoTurn(t *testing.T) {
	s := newScriptedServer()
	s.results["agents.list"] = []map[string]any{}
	s.results["thread.subscribe"] = map[string]any{"subscribed": "th_1"}
	s.results["thread.logs"] = []contracts.LogEntry{}
	s.results["thread.get"] = domain.ThreadState{Turn: 1}
	c := dialScripted(t, s)
	env, out, errBuf := headlessEnv()

	done := make(chan error, 1)
	go func() {
		done <- streamLogs(context.Background(), env, c, threadListRow{ID: "th_1"}, followOptions{
			lines: 10, follow: true, pollEvery: time.Millisecond, startGrace: time.Nanosecond,
		})
	}()
	select {
	case <-s.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("follower never subscribed")
	}
	// The bound fired and the daemon was asked. Only then does the lead finish,
	// so the follow must have survived the bound to see the outcome.
	s.waitCall(t, "thread.get")
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentStatusChanged, payload: contracts.AgentStatusChanged{
		AgentID: "agent-orchestrator", Status: contracts.AgentSucceeded,
	}})
	s.pushNow(t)

	if err := awaitFollow(t, done); err != nil {
		t.Fatalf("follow = %v, want a clean exit once the turn succeeds", err)
	}
	if strings.Contains(errBuf.String(), "no turn started") {
		t.Fatalf("a begun turn was reported as never started:\n%s", errBuf.String())
	}
	if !strings.Contains(out.String(), "status succeeded") {
		t.Fatalf("output missing the lead's terminal status:\n%s", out.String())
	}
}

// A thread the daemon never began still fails at the bound, with the reason on
// stderr only.
func TestFollowNeverStartedFailsAtTheBound(t *testing.T) {
	s := newScriptedServer()
	s.results["agents.list"] = []map[string]any{}
	s.results["thread.subscribe"] = map[string]any{"subscribed": "th_1"}
	s.results["thread.logs"] = []contracts.LogEntry{}
	s.results["thread.get"] = domain.ThreadState{}
	c := dialScripted(t, s)
	env, out, errBuf := headlessEnv()

	done := make(chan error, 1)
	go func() {
		done <- streamLogs(context.Background(), env, c, threadListRow{ID: "th_1"}, followOptions{
			lines: 10, follow: true, pollEvery: time.Millisecond, startGrace: time.Nanosecond,
		})
	}()
	err := awaitFollow(t, done)
	if got := exitCode(env, err); got != 1 {
		t.Fatalf("exit = %d, want 1 (err %v)", got, err)
	}
	if !strings.Contains(errBuf.String(), "no turn started") {
		t.Fatalf("stderr missing the no-turn line:\n%s", errBuf.String())
	}
	if strings.Contains(out.String(), "no turn started") {
		t.Fatalf("the no-turn line must not reach stdout:\n%s", out.String())
	}
}

// The production bound must cover a healthy start on a large repo, as the run
// --wait bound does.
func TestFollowStartGraceCoversASlowWorktreeCut(t *testing.T) {
	if followStartGrace < 2*time.Minute {
		t.Fatalf("followStartGrace = %v, want at least 2m", followStartGrace)
	}
}
