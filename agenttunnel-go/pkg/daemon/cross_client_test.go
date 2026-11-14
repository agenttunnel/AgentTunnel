package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// crossGateScript prints one line, then parks on a gate file the test writes:
// while the gate is missing the turn provably cannot have finished.
func crossGateScript(gate string) string {
	return fmt.Sprintf(`printf '{"type":"text","text":"awake"}\n'
while [ ! -f %q ]; do sleep 0.01; done
printf '{"type":"text","text":"finished"}\n'`, gate)
}

// Two clients share one daemon: a turn client A starts is fully visible to
// client B while it is still running — streamed output on B's subscription,
// the running agent in B's thread list, and A's user message in B's thread
// state. The A connection's turn.start reply is the turn-end barrier.
func TestCrossClientSeesALiveTurnStartedElsewhere(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	// A failed assertion must not leave the parked child spinning: open the
	// gate before the daemon closes (cleanups run LIFO, so this registers
	// after the daemon's own close).
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })
	d := newScriptedDaemon(t, crossGateScript(gate))

	// Client A creates the thread and will start its turn.
	a, _ := dialWS(t, d)
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, a, "create", "thread.create", map[string]any{"title": "cross client"}), &created)

	// Client B subscribes before the turn starts so it cannot miss the first
	// frame the lead emits.
	b, _ := dialWS(t, d)
	call(t, b, "sub", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	// A starts the turn in the background: thread.turn.start only replies
	// once the whole turn settles, so the reply doubles as its barrier.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		data, _ := json.Marshal(transport.Request{
			ID:     "turn",
			Method: "thread.turn.start",
			Params: mustJSON(t, map[string]any{"threadId": created.ThreadID, "text": "cross client probe"}),
		})
		if err := a.Write(ctx, websocket.MessageText, data); err != nil {
			t.Errorf("turn start write: %v", err)
		}
	}()

	// B sees the lead's output while the turn runs: the child printed "awake"
	// and is parked on the gate now, so this cannot be replay-after-the-fact.
	sawOutput := false
	for !sawOutput {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 30*time.Second)
		_, msg, err := b.Read(readCtx)
		if err != nil {
			cancelRead()
			t.Fatalf("b read: %v", err)
		}
		cancelRead()
		var frame struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(msg, &frame) != nil || frame.Method != "thread.event" {
			continue
		}
		var ev struct {
			ThreadID string          `json:"threadId"`
			Type     string          `json:"type"`
			Payload  json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(frame.Params, &ev) != nil ||
			ev.ThreadID != string(created.ThreadID) || ev.Type != "agent.output" {
			continue
		}
		var out contracts.AgentOutput
		if json.Unmarshal(ev.Payload, &out) == nil && out.Text == "awake" {
			sawOutput = true
		}
	}

	// Still gated open: B's list reports the running agent.
	var listed []struct {
		ID            string `json:"id"`
		RunningAgents int    `json:"runningAgents"`
	}
	json.Unmarshal(call(t, b, "list", "thread.list", nil), &listed)
	listedRunning := -1
	for _, row := range listed {
		if row.ID == string(created.ThreadID) {
			listedRunning = row.RunningAgents
		}
	}
	if listedRunning < 1 {
		t.Fatalf("thread.list runningAgents for %s = %d, want >= 1 while the turn runs",
			created.ThreadID, listedRunning)
	}

	// And B's thread state carries A's user message.
	var st struct {
		Messages []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		} `json:"messages"`
	}
	json.Unmarshal(call(t, b, "get", "thread.get", map[string]any{"threadId": created.ThreadID}), &st)
	foundUserMessage := false
	for _, msg := range st.Messages {
		if msg.Author == "user" && msg.Text == "cross client probe" {
			foundUserMessage = true
		}
	}
	if !foundUserMessage {
		t.Fatalf("thread.get messages = %+v, want A's user message", st.Messages)
	}

	// Release the turn and wait on A's reply: the barrier that proves the
	// turn really was running through every assertion above.
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatalf("open gate: %v", err)
	}
	replyCtx, cancelReply := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelReply()
	readResult(t, a, replyCtx, "turn")
}
