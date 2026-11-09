package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// TestThreadEventsReturnsGraphEventsOnly: the replay feed carries spawn,
// status, usage, mailbox and turn-marker events in seq order, drops other
// event types, and clips long mailbox bodies.
func TestThreadEventsReturnsGraphEventsOnly(t *testing.T) {
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	disp := NewDispatcher(DispatcherDeps{Engine: eng})

	mustEngine := func(cmd contracts.Command) {
		t.Helper()
		if err := eng.Dispatch(ctx, cmd); err != nil {
			t.Fatalf("dispatch %s: %v", cmd.Type, err)
		}
	}
	mustEngine(contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "T"})
	mustEngine(contracts.Command{ID: "r1", Type: contracts.CmdThreadRename, ThreadID: "t1", Title: "Renamed"})
	mustEngine(contracts.Command{ID: "p1", Type: contracts.CmdPeerMailboxSet, ThreadID: "t1", PeerMailbox: boolPtr(true)})
	mustEngine(contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "go"})
	mustEngine(contracts.Command{
		ID: "d1", Type: contracts.CmdTaskDispatch, ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{AgentID: "a1", Name: "parser", Role: "researcher", Task: "look", DependsOn: []string{"seed"}},
	})
	mustEngine(contracts.Command{
		ID: "s1", Type: contracts.CmdAgentStatusSet, ThreadID: "t1",
		Status: &contracts.AgentStatusChanged{AgentID: "a1", Status: contracts.AgentRunning},
	})
	long := strings.Repeat("x", 400)
	mustEngine(contracts.Command{
		ID: "x1", Type: contracts.CmdMailboxPost, ThreadID: "t1",
		Mailbox: &contracts.MailboxPosted{From: "a1", To: "agent-orchestrator", Kind: "message", Body: long},
	})

	raw, err := disp.Handle(ctx, Request{ID: "1", Method: "thread.events", Params: json.RawMessage(`{"threadId":"t1"}`)})
	if err != nil {
		t.Fatalf("thread.events: %v", err)
	}
	// Round-trip through JSON so the assertions read the wire shape.
	data, _ := json.Marshal(raw)
	var events []struct {
		Seq     int64          `json:"seq"`
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatal(err)
	}

	wantTypes := []string{"thread.message-sent", "agent.task-dispatched", "agent.status-changed", "mailbox.posted"}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events %v, want %v", len(events), events, wantTypes)
	}
	for i, ev := range events {
		if ev.Type != wantTypes[i] {
			t.Fatalf("event %d type = %s, want %s", i, ev.Type, wantTypes[i])
		}
		if i > 0 && ev.Seq <= events[i-1].Seq {
			t.Fatalf("events out of seq order at %d", i)
		}
	}
	if author := events[0].Payload["author"]; author != "user" {
		t.Fatalf("turn marker author = %v", author)
	}
	if _, leaked := events[0].Payload["text"]; leaked {
		t.Fatal("turn marker leaked message text")
	}
	if name := events[1].Payload["name"]; name != "parser" {
		t.Fatalf("dispatch name = %v", name)
	}
	body, _ := events[3].Payload["body"].(string)
	if n := len([]rune(body)); n != graphBodyMax+1 || !strings.HasSuffix(body, "…") {
		t.Fatalf("mailbox body not clipped: %d runes", n)
	}
}

func boolPtr(b bool) *bool { return &b }
