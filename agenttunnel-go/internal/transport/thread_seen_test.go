package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// seenFixture builds a dispatcher over an engine with a settable clock, plus
// helpers to create threads with finished and running turns.
type seenFixture struct {
	t      *testing.T
	ctx    context.Context
	eng    *domain.Engine
	disp   *Dispatcher
	clock  *time.Time
	nextID int
}

func newSeenFixture(t *testing.T) *seenFixture {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	clock := time.Unix(0, 0).UTC()
	eng.SetClock(func() time.Time { return clock })
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	return &seenFixture{
		t: t, ctx: ctx, eng: eng,
		disp:  NewDispatcher(DispatcherDeps{Engine: eng}),
		clock: &clock,
	}
}

func (f *seenFixture) cmd(cmd contracts.Command) {
	f.t.Helper()
	f.nextID++
	cmd.ID = fmt.Sprintf("c%d", f.nextID)
	if err := f.eng.Dispatch(f.ctx, cmd); err != nil {
		f.t.Fatalf("dispatch %s: %v", cmd.Type, err)
	}
}

// finishTurn creates a thread whose turn completes at the current clock.
func (f *seenFixture) finishTurn(id contracts.ThreadID, title string) {
	f.t.Helper()
	f.cmd(contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: id, Title: title})
	f.cmd(contracts.Command{Type: contracts.CmdTurnStart, ThreadID: id, Text: "do work"})
	f.cmd(contracts.Command{
		Type: contracts.CmdTaskDispatch, ThreadID: id,
		Dispatch: &contracts.DispatchTask{AgentID: contracts.AgentID("agent-" + string(id)), Role: contracts.RoleOrchestrator},
	})
	f.cmd(contracts.Command{
		Type: contracts.CmdAgentStatusSet, ThreadID: id,
		Status: &contracts.AgentStatusChanged{AgentID: contracts.AgentID("agent-" + string(id)), Status: contracts.AgentSucceeded},
	})
}

// runningTurn creates a thread whose lead is mid-turn at the current clock.
func (f *seenFixture) runningTurn(id contracts.ThreadID, title string) {
	f.t.Helper()
	f.cmd(contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: id, Title: title})
	f.cmd(contracts.Command{Type: contracts.CmdTurnStart, ThreadID: id, Text: "do work"})
	f.cmd(contracts.Command{
		Type: contracts.CmdTaskDispatch, ThreadID: id,
		Dispatch: &contracts.DispatchTask{AgentID: contracts.AgentID("agent-" + string(id)), Role: contracts.RoleOrchestrator},
	})
	f.cmd(contracts.Command{
		Type: contracts.CmdAgentStatusSet, ThreadID: id,
		Status: &contracts.AgentStatusChanged{AgentID: contracts.AgentID("agent-" + string(id)), Status: contracts.AgentRunning},
	})
}

func (f *seenFixture) markSeen(id contracts.ThreadID) {
	f.t.Helper()
	if _, err := f.disp.Handle(f.ctx, Request{ID: "seen", Method: "thread.markSeen", Params: json.RawMessage(`{"threadId":"` + string(id) + `"}`)}); err != nil {
		f.t.Fatalf("thread.markSeen: %v", err)
	}
}

func (f *seenFixture) list() []ThreadSummary {
	f.t.Helper()
	res, err := f.disp.Handle(f.ctx, Request{ID: "list", Method: "thread.list"})
	if err != nil {
		f.t.Fatalf("thread.list: %v", err)
	}
	list, ok := res.([]ThreadSummary)
	if !ok {
		f.t.Fatalf("thread.list result = %T, want []ThreadSummary", res)
	}
	return list
}

func (f *seenFixture) byID(list []ThreadSummary) map[contracts.ThreadID]ThreadSummary {
	f.t.Helper()
	out := make(map[contracts.ThreadID]ThreadSummary, len(list))
	for _, s := range list {
		out[s.ID] = s
	}
	return out
}

// TestThreadListMarksUnseenAndRunning pins the derived flag: a never-viewed
// finished thread is unseen; marking it seen clears the flag and stamps the
// view; a running thread is never unseen though it has no view; and a draft
// (no completed turn) is never unseen.
func TestThreadListMarksUnseenAndRunning(t *testing.T) {
	f := newSeenFixture(t)
	f.finishTurn("t1", "Finished, never viewed")
	f.runningTurn("t2", "Running, never viewed")
	f.finishTurn("t3", "Finished, then viewed")
	f.cmd(contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t4", Draft: true, Title: "Draft"})

	*f.clock = f.clock.Add(time.Hour)
	f.markSeen("t3")

	rows := f.byID(f.list())
	if got := rows["t1"]; !got.Unseen {
		t.Fatal("finished thread with no view must be unseen")
	}
	if got := rows["t3"]; got.Unseen {
		t.Fatal("finished thread viewed after completion must not be unseen")
	}
	if rows["t3"].LastSeenAt == nil || rows["t3"].LastSeenAt.IsZero() {
		t.Fatal("markSeen must stamp LastSeenAt")
	}
	if got := rows["t2"]; got.Unseen {
		t.Fatal("a running thread must not be unseen")
	}
	if rows["t2"].RunningAgents == 0 {
		t.Fatal("running thread must report its running agents")
	}
	if got := rows["t4"]; got.Unseen {
		t.Fatal("a draft with no completed turn must not be unseen")
	}
}

// TestMarkSeenKeepsRecencyOrder pins the UpdatedAt exemption end to end: the
// thread.list recency order does not change when a thread is marked seen,
// even though the clock moved past every activity.
func TestMarkSeenKeepsRecencyOrder(t *testing.T) {
	f := newSeenFixture(t)
	f.finishTurn("t1", "Finished first")
	*f.clock = f.clock.Add(time.Minute)
	f.finishTurn("t2", "Finished later")

	if order := threadIDs(f.list()); order[0] != "t2" || order[1] != "t1" {
		t.Fatalf("fixture order = %v, want t2 before t1 (finished later)", order)
	}

	*f.clock = f.clock.Add(time.Hour)
	f.markSeen("t2")

	if order := threadIDs(f.list()); order[0] != "t2" || order[1] != "t1" {
		t.Fatalf("order after markSeen = %v, want t2 still first", order)
	}
}

func threadIDs(list []ThreadSummary) []contracts.ThreadID {
	out := make([]contracts.ThreadID, len(list))
	for i, s := range list {
		out[i] = s.ID
	}
	return out
}

// TestThreadSeenWireShape pins the JSON wire form: unseen rides every row as
// an explicit boolean, and lastSeenAt is omitted for a never-viewed thread
// (struct-level asserts cannot see wire bytes).
func TestThreadSeenWireShape(t *testing.T) {
	f := newSeenFixture(t)
	f.finishTurn("t1", "Never viewed")
	f.finishTurn("t2", "Viewed")
	*f.clock = f.clock.Add(time.Hour)
	f.markSeen("t2")

	raw, err := json.Marshal(f.list())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal rows: %v", err)
	}
	byID := make(map[string]map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		byID[strings.Trim(string(row["id"]), `"`)] = row
	}
	if len(byID) != 2 {
		t.Fatalf("wire rows = %d, want 2", len(byID))
	}
	never, viewed := byID["t1"], byID["t2"]
	if string(never["unseen"]) != "true" {
		t.Fatalf("never-viewed unseen = %s, want true", never["unseen"])
	}
	if string(viewed["unseen"]) != "false" {
		t.Fatalf("viewed unseen = %s, want false", viewed["unseen"])
	}
	if _, ok := never["lastSeenAt"]; ok {
		t.Fatal("lastSeenAt must be absent for a never-viewed thread")
	}
	if v := strings.TrimSpace(string(viewed["lastSeenAt"])); v == "" || v == "null" {
		t.Fatalf("viewed lastSeenAt = %s, want a timestamp", v)
	} else if !strings.Contains(v, "T") {
		t.Fatalf("viewed lastSeenAt = %s, want RFC3339", v)
	}
}

// TestMarkSeenRearmsAfterAnotherTurn pins the feature's daily cycle: a viewed
// thread gets a further turn that finishes -> unseen flips back true -> a
// second markSeen clears it again, stamping the later view.
func TestMarkSeenRearmsAfterAnotherTurn(t *testing.T) {
	f := newSeenFixture(t)
	f.finishTurn("t1", "First turn")
	*f.clock = f.clock.Add(time.Hour)
	f.markSeen("t1")

	rows := f.byID(f.list())
	if rows["t1"].Unseen {
		t.Fatal("viewed thread must not be unseen before the second turn")
	}
	firstSeen := rows["t1"].LastSeenAt
	if firstSeen == nil {
		t.Fatal("first markSeen must stamp LastSeenAt")
	}

	// A further turn completes after the view: a new lead dispatched and
	// finished at a later clock.
	*f.clock = f.clock.Add(time.Hour)
	f.cmd(contracts.Command{Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "more work"})
	f.cmd(contracts.Command{
		Type: contracts.CmdTaskDispatch, ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{AgentID: "agent-t1-r2", Role: contracts.RoleOrchestrator},
	})
	// Snapshot after the second turn's setup: only the terminal status and
	// the re-view may append events from here.
	before, err := f.eng.State(f.ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	f.cmd(contracts.Command{
		Type: contracts.CmdAgentStatusSet, ThreadID: "t1",
		Status: &contracts.AgentStatusChanged{AgentID: "agent-t1-r2", Status: contracts.AgentSucceeded},
	})

	if rows := f.byID(f.list()); !rows["t1"].Unseen {
		t.Fatal("a turn completed after the last view must re-arm unseen")
	}

	*f.clock = f.clock.Add(time.Minute)
	f.markSeen("t1")

	after, err := f.eng.State(f.ctx, "t1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if after.Version != before.Version+2 {
		t.Fatalf("second cycle version = %d, want %d (terminal status + exactly one re-view event)",
			after.Version, before.Version+2)
	}
	rows = f.byID(f.list())
	if rows["t1"].Unseen {
		t.Fatal("second markSeen must clear the re-armed flag")
	}
	if rows["t1"].LastSeenAt == nil || !rows["t1"].LastSeenAt.After(*firstSeen) {
		t.Fatalf("second view stamp = %v, want later than %v", rows["t1"].LastSeenAt, firstSeen)
	}
}

// TestThreadGetSeenWireShape pins thread.get's "never" sentinel so it matches
// thread.list: lastSeenAt is key-absent for a never-viewed thread and a
// timestamp after markSeen, never a 0001-01-01 zero time.
func TestThreadGetSeenWireShape(t *testing.T) {
	f := newSeenFixture(t)
	f.finishTurn("t1", "Never viewed")

	state, err := f.disp.Handle(f.ctx, Request{ID: "get1", Method: "thread.get", Params: json.RawMessage(`{"threadId":"t1"}`)})
	if err != nil {
		t.Fatalf("thread.get: %v", err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if _, ok := row["lastSeenAt"]; ok {
		t.Fatalf("never-viewed thread.get must omit lastSeenAt, got %s", row["lastSeenAt"])
	}

	f.markSeen("t1")
	state, err = f.disp.Handle(f.ctx, Request{ID: "get2", Method: "thread.get", Params: json.RawMessage(`{"threadId":"t1"}`)})
	if err != nil {
		t.Fatalf("thread.get after markSeen: %v", err)
	}
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	viewedRow := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &viewedRow); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if v := strings.TrimSpace(string(viewedRow["lastSeenAt"])); v == "" || v == "null" || !strings.Contains(v, "T") {
		t.Fatalf("viewed thread.get lastSeenAt = %s, want an RFC3339 timestamp", v)
	}
}
