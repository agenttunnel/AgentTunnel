package domain

import (
	"reflect"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func dispatchOn(t *testing.T, e *Engine, thread contracts.ThreadID, agent, parent string, h contracts.Harness) {
	t.Helper()
	ctx := t.Context()
	mustDispatch(t, e, ctx, contracts.Command{
		ID:       "d-" + string(thread) + "-" + agent,
		Type:     contracts.CmdTaskDispatch,
		ThreadID: thread,
		Dispatch: &contracts.DispatchTask{
			AgentID:       contracts.AgentID(agent),
			ParentAgentID: contracts.AgentID(parent),
			Role:          contracts.RoleExecutor,
			Task:          "work",
			Harness:       h,
		},
	})
}

func recordOn(t *testing.T, e *Engine, thread contracts.ThreadID, agent string, u contracts.TokenUsage) {
	t.Helper()
	mustDispatch(t, e, t.Context(), contracts.Command{
		ID:       "u-" + string(thread) + "-" + agent,
		Type:     contracts.CmdUsageRecord,
		ThreadID: thread,
		Usage: &contracts.UsageRecorded{
			AgentID: contracts.AgentID(agent),
			Role:    contracts.RoleExecutor,
			Usage:   u,
		},
	})
}

func TestHarnessStatsRollsUpAcrossThreads(t *testing.T) {
	e, ctx := newTestEngine(t)
	for _, id := range []contracts.ThreadID{"t1", "t2"} {
		mustDispatch(t, e, ctx, contracts.Command{ID: "c-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id})
	}

	// t1: a claude lead with one codex subagent. t2: another claude lead, which
	// the same agent ids would collide with if stats were keyed by agent id.
	dispatchOn(t, e, "t1", "agent-orchestrator", "", contracts.HarnessClaude)
	dispatchOn(t, e, "t1", "agent-a-1", "agent-orchestrator", contracts.HarnessCodex)
	dispatchOn(t, e, "t2", "agent-orchestrator", "", contracts.HarnessClaude)

	recordOn(t, e, "t1", "agent-orchestrator", contracts.TokenUsage{InputTokens: 100, OutputTokens: 10, CachedTokens: 40})
	recordOn(t, e, "t1", "agent-a-1", contracts.TokenUsage{InputTokens: 300, OutputTokens: 30, CostUSD: 0.25})
	recordOn(t, e, "t2", "agent-orchestrator", contracts.TokenUsage{InputTokens: 1, OutputTokens: 2})

	got, err := e.HarnessStats(ctx)
	if err != nil {
		t.Fatalf("HarnessStats: %v", err)
	}
	want := []HarnessStat{
		{
			Harness: contracts.HarnessClaude, Threads: 2, Agents: 2, Subagents: 0,
			Usage: contracts.TokenUsage{InputTokens: 101, OutputTokens: 12, CachedTokens: 40},
		},
		{
			Harness: contracts.HarnessCodex, Threads: 1, Agents: 1, Subagents: 1,
			Usage: contracts.TokenUsage{InputTokens: 300, OutputTokens: 30, CostUSD: 0.25},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HarnessStats =\n%+v\nwant\n%+v", got, want)
	}
}

func TestHarnessStatsEmptyEnvironment(t *testing.T) {
	e, ctx := newTestEngine(t)
	got, err := e.HarnessStats(ctx)
	if err != nil {
		t.Fatalf("HarnessStats: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("HarnessStats on empty environment = %+v, want none", got)
	}
}
