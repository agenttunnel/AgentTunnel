package domain

import (
	"reflect"
	"testing"
	"time"

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

	got, err := e.HarnessStats(ctx, HarnessScope{})
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
	got, err := e.HarnessStats(ctx, HarnessScope{})
	if err != nil {
		t.Fatalf("HarnessStats: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("HarnessStats on empty environment = %+v, want none", got)
	}
}

func TestHarnessStatsScopesToProjectAndThread(t *testing.T) {
	e, ctx := newTestEngine(t)
	for _, c := range []struct {
		id      contracts.ThreadID
		project string
	}{{"t1", "p1"}, {"t2", "p1"}, {"t3", "p2"}} {
		mustDispatch(t, e, ctx, contracts.Command{
			ID: "c-" + string(c.id), Type: contracts.CmdThreadCreate, ThreadID: c.id, ProjectID: c.project,
		})
	}
	dispatchOn(t, e, "t1", "agent-orchestrator", "", contracts.HarnessClaude)
	dispatchOn(t, e, "t2", "agent-orchestrator", "", contracts.HarnessCodex)
	dispatchOn(t, e, "t3", "agent-orchestrator", "", contracts.HarnessClaude)

	agents := func(scope HarnessScope) map[contracts.Harness]int {
		t.Helper()
		stats, err := e.HarnessStats(ctx, scope)
		if err != nil {
			t.Fatalf("HarnessStats(%+v): %v", scope, err)
		}
		out := map[contracts.Harness]int{}
		for _, s := range stats {
			out[s.Harness] = s.Agents
		}
		return out
	}
	cases := []struct {
		name  string
		scope HarnessScope
		want  map[contracts.Harness]int
	}{
		{"environment", HarnessScope{}, map[contracts.Harness]int{contracts.HarnessClaude: 2, contracts.HarnessCodex: 1}},
		{"project p1", HarnessScope{ProjectID: "p1"}, map[contracts.Harness]int{contracts.HarnessClaude: 1, contracts.HarnessCodex: 1}},
		{"thread t3", HarnessScope{ThreadID: "t3"}, map[contracts.Harness]int{contracts.HarnessClaude: 1}},
	}
	for _, c := range cases {
		if got := agents(c.scope); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestModelStatsSplitsHarnessByModel(t *testing.T) {
	e, ctx := newTestEngine(t)
	for _, id := range []contracts.ThreadID{"t1", "t2"} {
		mustDispatch(t, e, ctx, contracts.Command{ID: "c-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id})
	}
	// The same harness on two models must stay two rows, so the leaderboard can
	// rank a model against its siblings on the same harness.
	modelOn := func(thread contracts.ThreadID, agent, parent, model string) {
		t.Helper()
		mustDispatch(t, e, ctx, contracts.Command{
			ID: "d-" + string(thread) + "-" + agent, Type: contracts.CmdTaskDispatch, ThreadID: thread,
			Dispatch: &contracts.DispatchTask{
				AgentID:       contracts.AgentID(agent),
				ParentAgentID: contracts.AgentID(parent),
				Role:          contracts.RoleExecutor,
				Task:          "work",
				Harness:       contracts.HarnessClaude,
				Model:         model,
			},
		})
	}
	modelOn("t1", "agent-orchestrator", "", "opus")
	modelOn("t1", "agent-a-1", "agent-orchestrator", "sonnet")
	modelOn("t2", "agent-orchestrator", "", "opus")

	recordOn(t, e, "t1", "agent-orchestrator", contracts.TokenUsage{InputTokens: 100, OutputTokens: 10, CachedTokens: 40})
	recordOn(t, e, "t1", "agent-a-1", contracts.TokenUsage{InputTokens: 300, OutputTokens: 30})
	recordOn(t, e, "t2", "agent-orchestrator", contracts.TokenUsage{InputTokens: 1, OutputTokens: 2})

	got, err := e.ModelStats(ctx, HarnessScope{})
	if err != nil {
		t.Fatalf("ModelStats: %v", err)
	}
	want := []ModelStat{
		{
			Harness: contracts.HarnessClaude, Model: "opus", Threads: 2, Agents: 2, Subagents: 0,
			Usage: contracts.TokenUsage{InputTokens: 101, OutputTokens: 12, CachedTokens: 40},
		},
		{
			Harness: contracts.HarnessClaude, Model: "sonnet", Threads: 1, Agents: 1, Subagents: 1,
			Usage: contracts.TokenUsage{InputTokens: 300, OutputTokens: 30},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelStats =\n%+v\nwant\n%+v", got, want)
	}
}

func TestModelStatsTimesResolvedSubagentRuns(t *testing.T) {
	e, ctx := newTestEngine(t)
	clock := time.Unix(0, 0).UTC()
	e.SetClock(func() time.Time { return clock })
	mustDispatch(t, e, ctx, contracts.Command{ID: "c-t1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	dispatchOn(t, e, "t1", "agent-orchestrator", "", contracts.HarnessClaude)
	for _, id := range []string{"agent-a-1", "agent-a-2", "agent-a-3"} {
		dispatchOn(t, e, "t1", id, "agent-orchestrator", contracts.HarnessClaude)
	}
	setStatus := func(id string, status contracts.AgentStatus) {
		t.Helper()
		mustDispatch(t, e, ctx, contracts.Command{
			ID: "s-" + id + string(status), Type: contracts.CmdAgentStatusSet, ThreadID: "t1",
			Status: &contracts.AgentStatusChanged{AgentID: contracts.AgentID(id), Status: status},
		})
	}
	// a-1 runs 0s..10s and succeeds; a-2 runs 0s..30s and succeeds; a-3 fails at
	// 40s, so it is counted as a run but not as a resolved task. The lead ends at
	// 100s and must not enter the timing at all.
	clock = time.Unix(10, 0).UTC()
	setStatus("agent-a-1", contracts.AgentRunning)
	setStatus("agent-a-1", contracts.AgentSucceeded)
	clock = time.Unix(30, 0).UTC()
	setStatus("agent-a-2", contracts.AgentRunning)
	setStatus("agent-a-2", contracts.AgentSucceeded)
	clock = time.Unix(40, 0).UTC()
	setStatus("agent-a-3", contracts.AgentRunning)
	setStatus("agent-a-3", contracts.AgentFailed)
	clock = time.Unix(100, 0).UTC()
	setStatus("agent-orchestrator", contracts.AgentSucceeded)

	got, err := e.ModelStats(ctx, HarnessScope{})
	if err != nil {
		t.Fatalf("ModelStats: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ModelStats = %+v, want one claude row", got)
	}
	s := got[0]
	if s.Agents != 4 || s.Subagents != 3 || s.Resolved != 2 {
		t.Fatalf("counts = agents %d subagents %d resolved %d, want 4/3/2", s.Agents, s.Subagents, s.Resolved)
	}
	// Resolved durations are 10s and 30s: the median is their mean, and p90
	// interpolates 90% of the way from the faster run to the slower one.
	if s.MedianSeconds != 20 || s.P90Seconds != 28 {
		t.Fatalf("median %.2f p90 %.2f, want 20 and 28", s.MedianSeconds, s.P90Seconds)
	}
}
