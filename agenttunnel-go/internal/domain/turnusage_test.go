package domain

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Usage must be attributable per turn, for the orchestrator and for each
// subagent, and must survive a snapshot.
func TestUsageIsRolledUpPerTurn(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	dispatch := func(id, parent string, role contracts.RoleID) {
		mustDispatch(t, e, ctx, contracts.Command{
			ID:       "d-" + id,
			Type:     contracts.CmdTaskDispatch,
			ThreadID: "t1",
			Dispatch: &contracts.DispatchTask{
				AgentID:       contracts.AgentID(id),
				ParentAgentID: contracts.AgentID(parent),
				Role:          role,
				Task:          "work",
			},
		})
	}
	record := func(agent string, role contracts.RoleID, turn int, in int64) {
		mustDispatch(t, e, ctx, contracts.Command{
			ID:       "u-" + agent,
			Type:     contracts.CmdUsageRecord,
			ThreadID: "t1",
			Usage: &contracts.UsageRecorded{
				AgentID: contracts.AgentID(agent),
				Role:    role,
				Usage:   contracts.TokenUsage{InputTokens: in},
				Turn:    turn,
			},
		})
	}

	dispatch("orch", "", contracts.RoleOrchestrator)
	dispatch("exec-a", "orch", contracts.RoleExecutor)
	dispatch("exec-b", "orch", contracts.RoleExecutor)

	// Turn 1.
	mustDispatch(t, e, ctx, contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "first"})
	record("orch", contracts.RoleOrchestrator, 1, 10)
	record("exec-a", contracts.RoleExecutor, 1, 100)

	// Turn 2.
	mustDispatch(t, e, ctx, contracts.Command{ID: "m2", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "second"})
	record("orch", contracts.RoleOrchestrator, 2, 20)
	record("exec-b", contracts.RoleExecutor, 2, 200)

	state, _ := e.State(ctx, "t1")
	if state.Turn != 2 {
		t.Fatalf("current turn = %d, want 2", state.Turn)
	}
	if len(state.Turns) != 2 {
		t.Fatalf("turns = %+v", state.Turns)
	}

	byTurn := map[int]TurnUsage{}
	for _, t := range state.Turns {
		byTurn[t.Turn] = t
	}

	if got := byTurn[1].Agents["orch"].InputTokens; got != 10 {
		t.Errorf("turn 1 orchestrator = %d, want 10", got)
	}
	if got := byTurn[1].Agents["exec-a"].InputTokens; got != 100 {
		t.Errorf("turn 1 exec-a = %d, want 100", got)
	}
	if _, ok := byTurn[1].Agents["exec-b"]; ok {
		t.Error("turn 1 should not carry exec-b's later spend")
	}
	if got := byTurn[2].Agents["exec-b"].InputTokens; got != 200 {
		t.Errorf("turn 2 exec-b = %d, want 200", got)
	}

	// Snapshots must not share the per-turn maps.
	clone := state.Clone()
	clone.Turns[0].Agents["orch"] = contracts.TokenUsage{InputTokens: 999}
	again, _ := e.State(ctx, "t1")
	if again.Turns[0].Agents["orch"].InputTokens == 999 {
		t.Fatal("clone shares per-turn maps with the read model")
	}
}
