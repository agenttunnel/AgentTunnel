package domain

import (
	"context"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func newTestEngine(t *testing.T) (*Engine, context.Context) {
	t.Helper()
	ctx := context.Background()
	e := NewEngine(NewMemoryStore(), nil)
	e.SetClock(func() time.Time { return time.Unix(0, 0).UTC() })
	e.Start(ctx)
	t.Cleanup(e.Close)
	return e, ctx
}

func mustDispatch(t *testing.T, e *Engine, ctx context.Context, cmd contracts.Command) {
	t.Helper()
	if err := e.Dispatch(ctx, cmd); err != nil {
		t.Fatalf("dispatch %s: %v", cmd.Type, err)
	}
}

func TestEngineCreateTurnAndProject(t *testing.T) {
	e, ctx := newTestEngine(t)

	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "My thread"})
	mustDispatch(t, e, ctx, contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "hello world"})

	state, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !state.Exists {
		t.Fatal("thread should exist")
	}
	if state.Title != "My thread" {
		t.Fatalf("title = %q", state.Title)
	}
	if len(state.Messages) != 1 || state.Messages[0].Text != "hello world" {
		t.Fatalf("messages = %+v", state.Messages)
	}
	if state.Version != 2 {
		t.Fatalf("version = %d, want 2", state.Version)
	}
}

func TestEngineSubscribeReceivesEvents(t *testing.T) {
	e, ctx := newTestEngine(t)
	ch, unsub := e.Subscribe("t1")
	defer unsub()

	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	select {
	case ev := <-ch:
		if ev.Type() != contracts.EvtThreadCreated {
			t.Fatalf("got event %s", ev.Type())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEngineUsageRollupSplitsOrchestratorAndSubagents(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	dispatch := func(agent string, parent string, role contracts.RoleID) {
		mustDispatch(t, e, ctx, contracts.Command{
			ID:       "d-" + agent,
			Type:     contracts.CmdTaskDispatch,
			ThreadID: "t1",
			Dispatch: &contracts.DispatchTask{
				AgentID:       contracts.AgentID(agent),
				ParentAgentID: contracts.AgentID(parent),
				Role:          role,
				Task:          "work",
			},
		})
	}
	dispatch("orch", "", contracts.RoleOrchestrator)
	dispatch("exec", "orch", contracts.RoleExecutor)

	record := func(agent string, role contracts.RoleID, in, out int64) {
		mustDispatch(t, e, ctx, contracts.Command{
			ID:       "u-" + agent,
			Type:     contracts.CmdUsageRecord,
			ThreadID: "t1",
			Usage: &contracts.UsageRecorded{
				AgentID: contracts.AgentID(agent),
				Role:    role,
				Usage:   contracts.TokenUsage{InputTokens: in, OutputTokens: out, CostUSD: 0.5},
			},
		})
	}
	record("orch", contracts.RoleOrchestrator, 100, 50)
	record("orch", contracts.RoleOrchestrator, 10, 5) // second step accumulates
	record("exec", contracts.RoleExecutor, 200, 100)

	state, _ := e.State(ctx, "t1")
	usage := state.ThreadUsage()

	if usage.Orchestrator.Total() != 165 {
		t.Fatalf("orchestrator total = %d, want 165", usage.Orchestrator.Total())
	}
	if usage.Subagents.Total() != 300 {
		t.Fatalf("subagent total = %d, want 300", usage.Subagents.Total())
	}
	if usage.Total().Total() != 465 {
		t.Fatalf("grand total = %d, want 465", usage.Total().Total())
	}
	if len(usage.ByAgent) != 2 {
		t.Fatalf("byAgent len = %d, want 2", len(usage.ByAgent))
	}
}

func TestEnginePeerMailboxDefaultsOffAndToggles(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	state, _ := e.State(ctx, "t1")
	if state.PeerMailbox {
		t.Fatal("peer mailbox must default to off")
	}

	on := true
	mustDispatch(t, e, ctx, contracts.Command{ID: "p1", Type: contracts.CmdPeerMailboxSet, ThreadID: "t1", PeerMailbox: &on})
	state, _ = e.State(ctx, "t1")
	if !state.PeerMailbox {
		t.Fatal("peer mailbox should be on after toggle")
	}
}

func TestEngineAutoModeToggle(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	on := true
	mustDispatch(t, e, ctx, contracts.Command{ID: "a1", Type: contracts.CmdAutoModeSet, ThreadID: "t1", AutoMode: &on})
	state, _ := e.State(ctx, "t1")
	if !state.AutoMode {
		t.Fatal("auto mode should be on")
	}
}

func TestEngineStateIsIsolatedFromWorker(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	snap, _ := e.State(ctx, "t1")
	snap.Title = "mutated"
	again, _ := e.State(ctx, "t1")
	if again.Title != contracts.DefaultThreadTitle {
		t.Fatalf("snapshot mutation leaked: %q", again.Title)
	}
}
