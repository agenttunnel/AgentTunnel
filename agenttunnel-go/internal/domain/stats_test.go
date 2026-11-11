package domain

import (
	"reflect"
	"strconv"
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

func TestTeamStatsRanksTeamsByResolveRate(t *testing.T) {
	e, ctx := newTestEngine(t)
	clock := time.Unix(0, 0).UTC()
	e.SetClock(func() time.Time { return clock })
	mustDispatch(t, e, ctx, contracts.Command{ID: "c-t1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	n := 0
	id := func(kind string) string { n++; return kind + "-" + strconv.Itoa(n) }
	at := func(sec int64) { clock = time.Unix(sec, 0).UTC() }
	dispatchModel := func(agent, parent string, h contracts.Harness, model string) {
		t.Helper()
		mustDispatch(t, e, ctx, contracts.Command{
			ID: id("d"), Type: contracts.CmdTaskDispatch, ThreadID: "t1",
			Dispatch: &contracts.DispatchTask{
				AgentID: contracts.AgentID(agent), ParentAgentID: contracts.AgentID(parent),
				Role: contracts.RoleExecutor, Task: "work", Harness: h, Model: model,
			},
		})
	}
	// Every turn opens with a user message, the way a real turn starts.
	turn := func(sec int64) {
		t.Helper()
		at(sec)
		mustDispatch(t, e, ctx, contracts.Command{
			ID: id("m"), Type: contracts.CmdMessageAppend, ThreadID: "t1",
			Message: &contracts.MessageSent{MessageID: id("msg"), Author: "user", Text: "go"},
		})
	}
	usage := func(turnNo int, agent string, in, out int64) {
		t.Helper()
		mustDispatch(t, e, ctx, contracts.Command{
			ID: id("u"), Type: contracts.CmdUsageRecord, ThreadID: "t1",
			Usage: &contracts.UsageRecorded{
				AgentID: contracts.AgentID(agent), Role: contracts.RoleExecutor,
				Usage: contracts.TokenUsage{InputTokens: in, OutputTokens: out}, Turn: turnNo,
			},
		})
	}
	status := func(sec int64, agent string, st contracts.AgentStatus) {
		t.Helper()
		at(sec)
		mustDispatch(t, e, ctx, contracts.Command{
			ID: id("s"), Type: contracts.CmdAgentStatusSet, ThreadID: "t1",
			Status: &contracts.AgentStatusChanged{AgentID: contracts.AgentID(agent), Status: st},
		})
	}

	// Team A: a claude/opus lead with a codex/gpt subagent, over three turns.
	dispatchModel("agent-orchestrator", "", contracts.HarnessClaude, "opus")
	dispatchModel("agent-a-1", "agent-orchestrator", contracts.HarnessCodex, "gpt-5")
	turn(0) // turn 1: resolves at 50s, 440 input+output tokens
	status(0, "agent-orchestrator", contracts.AgentRunning)
	usage(1, "agent-orchestrator", 100, 10)
	usage(1, "agent-a-1", 300, 30)
	status(50, "agent-orchestrator", contracts.AgentSucceeded)
	turn(100) // turn 2: the lead fails at 130s, so this is not resolved
	status(100, "agent-orchestrator", contracts.AgentRunning)
	usage(2, "agent-orchestrator", 100, 10)
	usage(2, "agent-a-1", 100, 10)
	status(130, "agent-orchestrator", contracts.AgentFailed)
	turn(200) // turn 3: resolves at 270s, 440 tokens
	status(200, "agent-orchestrator", contracts.AgentRunning)
	usage(3, "agent-orchestrator", 200, 20)
	usage(3, "agent-a-1", 200, 20)
	status(270, "agent-orchestrator", contracts.AgentSucceeded)

	// Team B: a claude/opus lead working alone, over two turns, both resolved.
	turn(300) // turn 4: 10s, 55 tokens
	status(300, "agent-orchestrator", contracts.AgentRunning)
	usage(4, "agent-orchestrator", 50, 5)
	status(310, "agent-orchestrator", contracts.AgentSucceeded)
	turn(400) // turn 5: 20s, 66 tokens
	status(400, "agent-orchestrator", contracts.AgentRunning)
	usage(5, "agent-orchestrator", 60, 6)
	status(420, "agent-orchestrator", contracts.AgentSucceeded)

	got, err := e.TeamStats(ctx, HarnessScope{})
	if err != nil {
		t.Fatalf("TeamStats: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("TeamStats = %+v, want two teams", got)
	}
	// Team B resolves every task, so it ranks first even though A is faster per task.
	b := got[0]
	if !reflect.DeepEqual(b.Members, []string{"claude/opus"}) || b.Tasks != 2 || b.Resolved != 2 {
		t.Fatalf("team B = %+v", b)
	}
	if b.MedianSeconds != 15 || b.MedianTokens != 60.5 {
		t.Fatalf("team B median %.2fs %.1f tokens, want 15s and 60.5", b.MedianSeconds, b.MedianTokens)
	}
	a := got[1]
	if !reflect.DeepEqual(a.Members, []string{"claude/opus", "codex/gpt-5"}) || a.Tasks != 3 || a.Resolved != 2 {
		t.Fatalf("team A = %+v", a)
	}
	// Resolved times are 50s and 70s, and resolved token totals are both 440.
	if a.MedianSeconds != 60 || a.P90Seconds != 68 || a.MedianTokens != 440 {
		t.Fatalf("team A median %.2fs p90 %.2fs tokens %.1f, want 60s, 68s, 440", a.MedianSeconds, a.P90Seconds, a.MedianTokens)
	}
}
