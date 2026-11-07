package domain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	if state.Version != 3 {
		t.Fatalf("version = %d, want 3", state.Version)
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

func TestEnginePeerMailboxOnByDefaultAndToggles(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	state, _ := e.State(ctx, "t1")
	if !state.PeerMailbox {
		t.Fatal("peer mailbox must default to on")
	}

	off := false
	mustDispatch(t, e, ctx, contracts.Command{ID: "p1", Type: contracts.CmdPeerMailboxSet, ThreadID: "t1", PeerMailbox: &off})
	state, _ = e.State(ctx, "t1")
	if state.PeerMailbox {
		t.Fatal("peer mailbox should be off after toggle")
	}
	on := true
	mustDispatch(t, e, ctx, contracts.Command{ID: "p2", Type: contracts.CmdPeerMailboxSet, ThreadID: "t1", PeerMailbox: &on})
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

func TestEngineDispatchPersistsDependsOn(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	mustDispatch(t, e, ctx, contracts.Command{
		ID:       "d1",
		Type:     contracts.CmdTaskDispatch,
		ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{
			AgentID:   "a1",
			Role:      contracts.RoleExecutor,
			Task:      "work",
			DependsOn: []string{"plan", "review"},
		},
	})

	state, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	agent, ok := state.Agents["a1"]
	if !ok {
		t.Fatal("agent a1 missing from read model")
	}
	if !reflect.DeepEqual(agent.DependsOn, []string{"plan", "review"}) {
		t.Fatalf("dependsOn = %v, want [plan review]", agent.DependsOn)
	}
}

func TestEngineReplaysLegacyDispatchWithoutDependsOn(t *testing.T) {
	// An event log written before dependsOn existed: the payload has no
	// dependsOn key, which must decode as nil and still project.
	dir := t.TempDir()
	log := `{"seq":1,"threadId":"t1","type":"thread.created","at":"2024-01-01T00:00:00Z","payload":{"title":"legacy"}}
{"seq":2,"threadId":"t1","type":"agent.task-dispatched","at":"2024-01-01T00:00:01Z","payload":{"agentId":"a1","role":"executor","task":"old work","model":""}}
`
	if err := os.WriteFile(filepath.Join(dir, "t1.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	e := NewEngine(store, nil)
	e.Start(ctx)
	t.Cleanup(e.Close)

	state, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	agent, ok := state.Agents["a1"]
	if !ok {
		t.Fatal("legacy dispatch did not project")
	}
	if agent.DependsOn != nil {
		t.Fatalf("dependsOn = %v, want nil for legacy event", agent.DependsOn)
	}
	if agent.Task != "old work" {
		t.Fatalf("task = %q", agent.Task)
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

// Unsubscribing must never panic a concurrent publish: senders snapshot the
// listener set under the lock and deliver after releasing it, so unsubscribe
// removes the channel without closing it. This hammers both sides at once; a
// close-on-unsubscribe send panics under the race detector.
func TestEngineUnsubscribeConcurrentWithDispatch(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c0", Type: contracts.CmdThreadCreate, ThreadID: "t1"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			mustDispatch(t, e, ctx, contracts.Command{
				ID: fmt.Sprintf("c-%d", i), Type: contracts.CmdMessageAppend, ThreadID: "t1",
				Message: &contracts.MessageSent{MessageID: fmt.Sprintf("m-%d", i), Author: "user", Text: "hi"},
			})
		}
	}()
	for i := 0; i < 50; i++ {
		_, unsub := e.Subscribe("t1")
		e.Publish("t1", contracts.AgentOutput{AgentID: "a1", Kind: contracts.LogText, Text: "tick"})
		// Overlap the in-flight listener snapshot: unsubscribe while events
		// are still being delivered to the set this subscription was part of.
		unsub()
	}
	<-done
}
