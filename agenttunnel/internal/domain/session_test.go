package domain

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// TestObservedSessionTracksLatestRun checks that an agent keeps the harness
// session of its latest run, that a repeated id is not a new event, and that
// an observation for an unknown agent is refused.
func TestObservedSessionTracksLatestRun(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c-s1", Type: contracts.CmdThreadCreate, ThreadID: "s1"})
	dispatchOn(t, e, "s1", "agent-solo", "", contracts.HarnessClaude)

	observe := func(agent, session string) error {
		return e.Dispatch(ctx, contracts.Command{
			ID: "ss-" + agent + session, Type: contracts.CmdAgentSessionObserve, ThreadID: "s1",
			SessionObserved: &contracts.AgentSessionObserved{AgentID: contracts.AgentID(agent), Session: session},
		})
	}
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "ss-first", Type: contracts.CmdAgentSessionObserve, ThreadID: "s1",
		SessionObserved: &contracts.AgentSessionObserved{AgentID: "agent-solo", Session: "run-1"},
	})
	if err := observe("agent-solo", "run-1"); err != nil {
		t.Fatalf("repeat observe: %v", err)
	}
	if err := observe("agent-ghost", "run-1"); err == nil {
		t.Fatal("observing an unknown agent should be refused")
	}

	st, err := e.State(ctx, "s1")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if got := st.Agents["agent-solo"].HarnessSession; got != "run-1" {
		t.Fatalf("HarnessSession = %q, want run-1", got)
	}

	mustDispatch(t, e, ctx, contracts.Command{
		ID: "ss-second", Type: contracts.CmdAgentSessionObserve, ThreadID: "s1",
		SessionObserved: &contracts.AgentSessionObserved{AgentID: "agent-solo", Session: "run-2"},
	})
	st, err = e.State(ctx, "s1")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if got := st.Agents["agent-solo"].HarnessSession; got != "run-2" {
		t.Fatalf("HarnessSession after a new run = %q, want run-2", got)
	}
}
