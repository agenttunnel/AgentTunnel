package domain

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// seenThread builds a thread with one finished turn: the minimum lifecycle
// that stamps a turn's EndedAt. The dispatch with no parent agent is the lead.
func seenThread(t *testing.T) (*Engine, context.Context, func(sec int64)) {
	t.Helper()
	e, ctx := newTestEngine(t)
	clock := time.Unix(0, 0).UTC()
	e.SetClock(func() time.Time { return clock })
	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	mustDispatch(t, e, ctx, contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "hello"})
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "d1", Type: contracts.CmdTaskDispatch, ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{AgentID: "agent-orchestrator", Role: contracts.RoleOrchestrator},
	})
	advance := func(sec int64) { clock = time.Unix(sec, 0).UTC() }
	advance(10)
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "s1", Type: contracts.CmdAgentStatusSet, ThreadID: "t1",
		Status: &contracts.AgentStatusChanged{AgentID: "agent-orchestrator", Status: contracts.AgentSucceeded},
	})
	return e, ctx, advance
}

// TestThreadSeenEmitsOnceThenIdempotent pins the log-growth rule: the first
// view after a finished turn records one event, a repeat view records
// nothing, and a thread with no completed turn (draft or turn-less) marks
// nothing at all.
func TestThreadSeenEmitsOnceThenIdempotent(t *testing.T) {
	e, ctx, advance := seenThread(t)

	before, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if before.LastTurnEndedAt().IsZero() {
		t.Fatal("fixture must finish a turn before the view is recorded")
	}

	advance(20)
	mustDispatch(t, e, ctx, contracts.Command{ID: "v1", Type: contracts.CmdThreadSeen, ThreadID: "t1"})
	after, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if after.LastSeenAt == nil {
		t.Fatal("first view after a finished turn must set LastSeenAt")
	}
	if after.Version != before.Version+1 {
		t.Fatalf("version = %d, want %d (exactly one event)", after.Version, before.Version+1)
	}

	seenAt := after.LastSeenAt
	mustDispatch(t, e, ctx, contracts.Command{ID: "v2", Type: contracts.CmdThreadSeen, ThreadID: "t1"})
	repeat, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if repeat.Version != after.Version {
		t.Fatalf("repeat view appended events: version %d, want %d", repeat.Version, after.Version)
	}
	if !repeat.LastSeenAt.Equal(*seenAt) {
		t.Fatalf("repeat view moved LastSeenAt from %v to %v", seenAt, repeat.LastSeenAt)
	}

	// A draft thread has no completed turn, so a view records nothing.
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2", Draft: true, Title: "Draft",
	})
	mustDispatch(t, e, ctx, contracts.Command{ID: "v3", Type: contracts.CmdThreadSeen, ThreadID: "t2"})
	draft, err := e.State(ctx, "t2")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if draft.LastSeenAt != nil {
		t.Fatalf("draft LastSeenAt = %v, want nil (no completed turn to view)", draft.LastSeenAt)
	}

	// An unknown thread is refused outright.
	err = e.Dispatch(ctx, contracts.Command{ID: "v4", Type: contracts.CmdThreadSeen, ThreadID: "ghost"})
	if err == nil || !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("unknown thread error = %v, want ErrThreadNotFound", err)
	}
}

// TestThreadSeenDoesNotBumpUpdatedAt pins the recency rule: a view sets
// LastSeenAt but leaves UpdatedAt where the last real activity put it.
func TestThreadSeenDoesNotBumpUpdatedAt(t *testing.T) {
	e, ctx, advance := seenThread(t)

	before, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	updatedAt := before.UpdatedAt

	advance(50)
	mustDispatch(t, e, ctx, contracts.Command{ID: "v1", Type: contracts.CmdThreadSeen, ThreadID: "t1"})
	after, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !after.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("view bumped UpdatedAt from %v to %v", updatedAt, after.UpdatedAt)
	}
	if want := time.Unix(50, 0).UTC(); after.LastSeenAt == nil || !after.LastSeenAt.Equal(want) {
		t.Fatalf("LastSeenAt = %v, want %v", after.LastSeenAt, want)
	}
}

// TestThreadSeenRoundTripsJSON pins the wire replay: a persisted thread.seen
// event unmarshals back through the discriminator and replays to the same
// LastSeenAt, so a daemon restart keeps the unseen state.
func TestThreadSeenRoundTripsJSON(t *testing.T) {
	e, ctx, advance := seenThread(t)
	mustDispatch(t, e, ctx, contracts.Command{ID: "v1", Type: contracts.CmdThreadSeen, ThreadID: "t1"})
	// A later event proves the replay's LastSeenAt came from the seen stamp,
	// not from whatever landed last in the log.
	advance(90)
	mustDispatch(t, e, ctx, contracts.Command{ID: "r1", Type: contracts.CmdThreadRename, ThreadID: "t1", Title: "Renamed"})

	st, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	want := st.LastSeenAt

	events, err := e.Events(ctx, "t1")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []contracts.Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	replayed := NewThreadState("t1")
	sawSeen := false
	for _, ev := range back {
		if ev.Type() == contracts.EvtThreadSeen {
			sawSeen = true
		}
		if err := Project(replayed, ev); err != nil {
			t.Fatalf("replay %s: %v", ev.Type(), err)
		}
	}
	if !sawSeen {
		t.Fatal("replayed log carries no thread.seen event")
	}
	if !replayed.LastSeenAt.Equal(*want) {
		t.Fatalf("replayed LastSeenAt = %v, want %v", replayed.LastSeenAt, want)
	}
	if !replayed.UpdatedAt.Equal(st.UpdatedAt) {
		t.Fatalf("replayed UpdatedAt = %v, want %v (view is not activity)", replayed.UpdatedAt, st.UpdatedAt)
	}
}
