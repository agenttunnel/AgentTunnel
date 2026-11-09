package daemon

import (
	"encoding/json"
	"strings"
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

func TestReviewSubmitSendsPendingCommentsAsTurn(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "review"}), &created)
	comment := func(id, file string, line int, body string) {
		call(t, c, id, "review.comment", map[string]any{
			"threadId": created.ThreadID, "file": file, "line": line, "body": body,
		})
	}
	comment("2", "main.go", 12, "rename this")
	comment("3", "util.go", 3, "handle the error")

	// review.submit runs the turn before it returns, so the state read after it
	// already reflects the turn.
	call(t, c, "4", "review.submit", map[string]any{"threadId": created.ThreadID})

	var state struct {
		Messages []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		} `json:"messages"`
		ReviewComments []struct {
			ID        string `json:"id"`
			Submitted bool   `json:"submitted"`
		} `json:"reviewComments"`
	}
	json.Unmarshal(call(t, c, "5", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)

	if len(state.Messages) == 0 || state.Messages[0].Author != "user" {
		t.Fatalf("messages = %+v, want the review as the first user turn", state.Messages)
	}
	for _, want := range []string{"- main.go:12: rename this", "- util.go:3: handle the error"} {
		if !strings.Contains(state.Messages[0].Text, want) {
			t.Fatalf("review turn missing %q:\n%s", want, state.Messages[0].Text)
		}
	}
	for _, rc := range state.ReviewComments {
		if !rc.Submitted {
			t.Fatalf("comment %s not marked submitted", rc.ID)
		}
	}

	// A comment added after a submission is pending until the next submit.
	comment("6", "api.go", 7, "add a test")
	call(t, c, "7", "review.submit", map[string]any{"threadId": created.ThreadID})
	json.Unmarshal(call(t, c, "8", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)
	var userTurns []string
	for _, m := range state.Messages {
		if m.Author == "user" {
			userTurns = append(userTurns, m.Text)
		}
	}
	if len(userTurns) != 2 || !strings.Contains(userTurns[1], "- api.go:7: add a test") {
		t.Fatalf("second review turn missing the new comment: %q", userTurns)
	}
}
