package domain

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// TestEngineEventsReturnsLogInSeqOrder: Events is the raw durable log, oldest
// first, and reading it does not disturb the projection.
func TestEngineEventsReturnsLogInSeqOrder(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "T"})
	mustDispatch(t, e, ctx, contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "go"})
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "d1", Type: contracts.CmdTaskDispatch, ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{AgentID: "a1", Role: "researcher", Task: "look"},
	})

	events, err := e.Events(ctx, "t1")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, ev.Seq, i+1)
		}
	}
	if events[3].Type() != contracts.EvtTaskDispatched {
		t.Fatalf("last event = %s, want agent.task-dispatched", events[3].Type())
	}

	state, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Agents["a1"]; !ok {
		t.Fatal("projection lost the dispatched agent after reading events")
	}
}

func TestEngineEventsUnknownThreadIsEmpty(t *testing.T) {
	e, ctx := newTestEngine(t)
	events, err := e.Events(ctx, "nope")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("got %d events for unknown thread", len(events))
	}
}
