package daemon

import (
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
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
