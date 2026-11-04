package daemon

import (
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestReviewCommentRoundTrip(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "review"}), &created)

	call(t, c, "2", "review.comment", map[string]any{
		"threadId": created.ThreadID,
		"file":     "main.go",
		"line":     12,
		"body":     "rename this",
	})

	var state struct {
		ReviewComments []contracts.ReviewCommentAdded `json:"reviewComments"`
	}
	json.Unmarshal(call(t, c, "3", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)

	if len(state.ReviewComments) != 1 {
		t.Fatalf("review comments = %+v", state.ReviewComments)
	}
	got := state.ReviewComments[0]
	if got.File != "main.go" || got.Line != 12 || got.Body != "rename this" {
		t.Fatalf("comment = %+v", got)
	}
}
