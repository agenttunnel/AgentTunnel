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

	// Creation alone is provisional and emits nothing; the first durable
	// command materializes the thread and commits its events.
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	select {
	case ev := <-ch:
		t.Fatalf("provisional creation must not emit events, got %s", ev.Type())
	case <-time.After(50 * time.Millisecond):
	}
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "c2", Type: contracts.CmdMessageAppend, ThreadID: "t1",
		Message: &contracts.MessageSent{MessageID: "m1", Author: "user", Text: "hi"},
	})

	seen := 0
	want := map[contracts.EventType]bool{contracts.EvtThreadCreated: false, contracts.EvtPeerMailboxSet: false, contracts.EvtMessageSent: false}
	for seen < len(want) {
		select {
		case ev := <-ch:
			if _, expected := want[ev.Type()]; expected {
				want[ev.Type()] = true
			}
			seen++
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for materialization + message events")
		}
	}
}

// SubscribeAll hears every thread, including threads it never asked about by
// id. Committed events and Publish both reach it.
func TestEngineSubscribeAllReceivesEveryThread(t *testing.T) {
	e, ctx := newTestEngine(t)
	ch, unsub := e.SubscribeAll()
	defer unsub()

	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	mustDispatch(t, e, ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"})
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "m1", Type: contracts.CmdMessageAppend, ThreadID: "t1",
		Message: &contracts.MessageSent{MessageID: "m1", Author: "user", Text: "one"},
	})
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "m2", Type: contracts.CmdMessageAppend, ThreadID: "t2",
		Message: &contracts.MessageSent{MessageID: "m2", Author: "user", Text: "two"},
	})

	seen := map[contracts.ThreadID]bool{}
	for len(seen) < 2 {
		select {
		case ev := <-ch:
			if ev.Type() == contracts.EvtMessageSent {
				seen[ev.ThreadID] = true
			}
		case <-time.After(time.Second):
			t.Fatalf("global subscriber did not hear both threads, saw %v", seen)
		}
	}

	e.Publish("t2", contracts.QuestionAsked{QuestionID: "q1", Question: "ship?"})
	for {
		select {
		case ev := <-ch:
			if ev.Type() == contracts.EvtQuestionAsked && ev.ThreadID == "t2" {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("global subscriber did not hear the published question")
		}
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

// A thread that is created but never used leaves no durable trace: creation is
// in-memory (provisional), the event log is only written when something real
// happens, and a restart forgets the thread entirely.
func TestThreadCreateIsProvisionalUntilUsed(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e := NewEngine(store, nil)
	e.Start(ctx)
	t.Cleanup(e.Close)

	mustDispatch(t, e, ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", ProjectID: "p1",
	})

	// In memory: listed, gettable, carries the creation defaults.
	ids, err := e.Threads(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "t1" {
		t.Fatalf("threads = %v, err = %v", ids, err)
	}
	st, err := e.State(ctx, "t1")
	if err != nil || !st.Exists || st.Title == "" || !st.PeerMailbox || st.ProjectID != "p1" {
		t.Fatalf("provisional state = %+v, err = %v", st, err)
	}

	// On disk: nothing. An abandoned thread leaves no file behind.
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 0 {
		t.Fatalf("provisional thread wrote an event log: %v", files)
	}

	// The first durable command materializes it: creation events plus the
	// command's own, in one log — the same shape as if it had always existed.
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "c2", Type: contracts.CmdMessageAppend, ThreadID: "t1",
		Message: &contracts.MessageSent{MessageID: "m1", Author: "user", Text: "do something"},
	})
	st, _ = e.State(ctx, "t1")
	if st.Provisional || st.Version != 3 || len(st.Messages) != 1 {
		t.Fatalf("materialized state = %+v", st)
	}
	files, _ = filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("materialized thread has no event log: %v", files)
	}

	// Replay from disk reconstructs it; a provisional one is forgotten.
	e.Close()
	e2 := NewEngine(store, nil)
	e2.Start(ctx)
	t.Cleanup(e2.Close)
	if _, err := e2.State(ctx, "t1"); err != nil {
		t.Fatalf("materialized thread lost: %v", err)
	}
	notThere, err := e2.Threads(ctx)
	if err != nil || len(notThere) != 1 {
		t.Fatalf("after restart, threads = %v (err %v): the abandoned thread must be gone", notThere, err)
	}
}

// Deleting a provisional thread forgets it in memory without writing anything.
func TestDeleteProvisionalThreadLeavesNoTrace(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e := NewEngine(store, nil)
	e.Start(ctx)
	t.Cleanup(e.Close)

	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	mustDispatch(t, e, ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadDelete, ThreadID: "t1"})

	ids, err := e.Threads(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("threads = %v, err = %v", ids, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 0 {
		t.Fatalf("deleted provisional thread left a log: %v", files)
	}
}

// Renaming a fresh thread counts as doing something: it materializes.
func TestRenameMaterializesProvisionalThread(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e := NewEngine(store, nil)
	e.Start(ctx)
	t.Cleanup(e.Close)

	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	mustDispatch(t, e, ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadRename, ThreadID: "t1", Title: "renamed"})

	st, _ := e.State(ctx, "t1")
	if st.Provisional || st.Title != "renamed" {
		t.Fatalf("state = %+v", st)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("renamed thread not materialized: %v", files)
	}
}
