package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

func TestSteeringRecordsAMessageAndQueuesIt(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "steer"}), &created)

	call(t, c, "2", "thread.steer", map[string]any{
		"threadId": created.ThreadID,
		"text":     "focus on the parser first",
	})

	// The steering message is visible in the transcript, marked as steering.
	var state struct {
		Messages []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		} `json:"messages"`
	}
	json.Unmarshal(call(t, c, "3", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)

	found := false
	for _, m := range state.Messages {
		if m.Author == "steering" && m.Text == "focus on the parser first" {
			found = true
		}
	}
	if !found {
		t.Fatalf("steering message missing from the transcript: %+v", state.Messages)
	}

	// And it is queued for the orchestrator's next pass.
	queued := d.Runner.DrainSteering(created.ThreadID)
	if len(queued) != 1 || queued[0] != "focus on the parser first" {
		t.Fatalf("queued steering = %+v", queued)
	}
	if again := d.Runner.DrainSteering(created.ThreadID); len(again) != 0 {
		t.Fatalf("steering should be consumed once, got %+v", again)
	}
}

func TestSteeringRequiresText(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "steer"}), &created)

	if _, errResp := rawCall(t, c, "2", "thread.steer", map[string]any{
		"threadId": created.ThreadID,
		"text":     "   ",
	}); errResp == nil {
		t.Fatal("expected an error for empty steering text")
	}
}

// gateScript holds the agent until the test writes the gate file, so the turn
// cannot finish before the steer lands. The gate path comes from the env the
// test set before the turn spawned the agent.
const gateScript = `while [ ! -f "$AT_TEST_STEER_GATE" ]; do sleep 0.01; done
printf '{"type":"text","text":"ok"}\n'`

// A steer sent while a turn runs is recorded twice, in order: queued (the
// steering message) and then injected into the orchestrator's round. Both are
// durable, so the replay the graph reads carries the same order.
func TestSteeringQueuedThenInjectedDuringTurn(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	t.Setenv("AT_TEST_STEER_GATE", gate)
	d := newScriptedDaemon(t, gateScript)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "steer timeline"}), &created)

	events, unsub := d.Engine.Subscribe(created.ThreadID)
	defer unsub()

	// The turn runs on its own connection; it blocks inside the agent.
	bg, _ := dialWS(t, d)
	turnParams := mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "do the work"})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		data, _ := json.Marshal(transport.Request{ID: "turn", Method: "thread.turn.start", Params: turnParams})
		_ = bg.Write(ctx, websocket.MessageText, data)
	}()

	call(t, c, "2", "thread.steer", map[string]any{
		"threadId": created.ThreadID,
		"text":     "focus on the parser first",
	})
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatalf("open gate: %v", err)
	}

	// Typed events, not sleeps: the queued message must precede the injection,
	// and the lead must finish after it.
	var queuedSeq, injectedSeq int64
	var injectedCount int
	deadline := time.After(30 * time.Second)
	for done := false; !done; {
		select {
		case ev := <-events:
			switch p := ev.Payload.(type) {
			case contracts.MessageSent:
				if p.Author == "steering" {
					queuedSeq = ev.Seq
				}
			case contracts.SteeringInjected:
				if queuedSeq == 0 {
					t.Fatalf("steering injected at seq %d before it was queued", ev.Seq)
				}
				injectedSeq, injectedCount = ev.Seq, p.Count
			case contracts.AgentStatusChanged:
				if p.AgentID == "agent-orchestrator" && p.Status == contracts.AgentSucceeded {
					done = true
				}
				if p.AgentID == "agent-orchestrator" && p.Status == contracts.AgentFailed {
					t.Fatalf("lead failed: %s", p.Reason)
				}
			}
		case <-deadline:
			t.Fatalf("turn never finished (queued=%d injected=%d)", queuedSeq, injectedSeq)
		}
	}
	if injectedSeq == 0 || injectedCount != 1 {
		t.Fatalf("injected seq=%d count=%d, want one injection of 1", injectedSeq, injectedCount)
	}
	if injectedSeq <= queuedSeq {
		t.Fatalf("injected seq %d not after queued seq %d", injectedSeq, queuedSeq)
	}

	// The same order holds in the replay the graph folds from.
	var rows []struct {
		Type    contracts.EventType `json:"type"`
		Payload struct {
			Author string `json:"author"`
			Count  int    `json:"count"`
		} `json:"payload"`
	}
	json.Unmarshal(call(t, c, "3", "thread.events", map[string]any{"threadId": created.ThreadID}), &rows)
	queuedAt, injectedAt := -1, -1
	for i, r := range rows {
		switch {
		case r.Type == contracts.EvtMessageSent && r.Payload.Author == "steering":
			queuedAt = i
		case r.Type == contracts.EvtSteeringInjected && r.Payload.Count == 1:
			injectedAt = i
		}
	}
	if queuedAt < 0 || injectedAt < 0 || injectedAt <= queuedAt {
		t.Fatalf("replay order queued=%d injected=%d, want queued before injected", queuedAt, injectedAt)
	}
}
