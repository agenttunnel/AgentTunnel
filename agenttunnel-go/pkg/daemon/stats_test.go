package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestStatsHarnessesRPCRollsUpDispatchedAgents(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var empty []map[string]any
	json.Unmarshal(call(t, c, "1", "stats.harnesses", nil), &empty)
	if len(empty) != 0 {
		t.Fatalf("stats on empty environment = %v, want none", empty)
	}

	ctx := context.Background()
	if err := d.Engine.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Engine.Dispatch(ctx, contracts.Command{
		ID: "d1", Type: contracts.CmdTaskDispatch, ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{
			AgentID: "agent-orchestrator", Role: contracts.RoleOrchestrator, Task: "lead",
			Harness: contracts.HarnessClaude,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Engine.Dispatch(ctx, contracts.Command{
		ID: "d2", Type: contracts.CmdTaskDispatch, ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{
			AgentID: "agent-a-1", ParentAgentID: "agent-orchestrator", Role: contracts.RoleExecutor,
			Task: "work", Harness: contracts.HarnessCodex,
		},
	}); err != nil {
		t.Fatal(err)
	}

	var got []struct {
		Harness   string `json:"harness"`
		Threads   int    `json:"threads"`
		Agents    int    `json:"agents"`
		Subagents int    `json:"subagents"`
	}
	json.Unmarshal(call(t, c, "2", "stats.harnesses", nil), &got)
	if len(got) != 2 {
		t.Fatalf("stats = %+v, want 2 harnesses", got)
	}
	// Both harnesses have one agent each; ties sort by harness name.
	if got[0].Harness != "claude" || got[0].Subagents != 0 || got[0].Agents != 1 {
		t.Fatalf("claude row = %+v", got[0])
	}
	if got[1].Harness != "codex" || got[1].Subagents != 1 || got[1].Threads != 1 {
		t.Fatalf("codex row = %+v", got[1])
	}
}

func TestStatsModelsRPCSplitsByModel(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)
	ctx := context.Background()
	if err := d.Engine.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Engine.Dispatch(ctx, contracts.Command{
		ID: "d1", Type: contracts.CmdTaskDispatch, ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{
			AgentID: "agent-orchestrator", Role: contracts.RoleOrchestrator, Task: "lead",
			Harness: contracts.HarnessClaude, Model: "opus",
		},
	}); err != nil {
		t.Fatal(err)
	}

	var got []struct {
		Harness string `json:"harness"`
		Model   string `json:"model"`
		Agents  int    `json:"agents"`
	}
	json.Unmarshal(call(t, c, "2", "stats.models", nil), &got)
	if len(got) != 1 || got[0].Harness != "claude" || got[0].Model != "opus" || got[0].Agents != 1 {
		t.Fatalf("stats.models = %+v, want one claude/opus row", got)
	}
}
