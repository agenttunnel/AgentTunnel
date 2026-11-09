package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestReviewRequestBulletsFileLine(t *testing.T) {
	got := ReviewRequest([]contracts.ReviewCommentAdded{
		{File: "main.go", Line: 12, Body: "rename this"},
		{File: "util.go", Line: 3, Body: "handle the error\nreturn it"},
	})
	for _, want := range []string{
		"- main.go:12: rename this\n",
		"- util.go:3: handle the error\n  return it",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("request missing %q:\n%s", want, got)
		}
	}
}

func TestDecideReviewSubmitOnlyPendingComments(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	project := func(p contracts.EventPayload) {
		t.Helper()
		if err := Project(s, contracts.Event{Seq: 1, ThreadID: "t1", Payload: p}); err != nil {
			t.Fatalf("project: %v", err)
		}
	}
	project(contracts.ReviewCommentAdded{ID: "r1", File: "a.go", Line: 1, Body: "one"})

	submit := func(ids ...string) contracts.Command {
		return contracts.Command{
			ID: "u1", Type: contracts.CmdReviewSubmit, ThreadID: "t1", Text: "review",
			ReviewSubmit: &contracts.ReviewCommentsSubmitted{CommentIDs: ids},
		}
	}
	if _, err := Decide(submit("ghost"), s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unknown comment: want ErrInvalidCommand, got %v", err)
	}
	if _, err := Decide(submit(), s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("empty submit: want ErrInvalidCommand, got %v", err)
	}

	evs, err := Decide(submit("r1"), s)
	if err != nil || len(evs) != 2 {
		t.Fatalf("valid submit: err=%v events=%d, want the turn plus the submission", err, len(evs))
	}
	if msg, ok := evs[0].(contracts.MessageSent); !ok || msg.Author != "user" {
		t.Fatalf("first event = %#v, want a user turn", evs[0])
	}
	project(evs[1])
	if !s.ReviewComments[0].Submitted {
		t.Fatalf("comment not marked submitted: %+v", s.ReviewComments)
	}

	// A submitted comment cannot be handed over twice.
	if _, err := Decide(submit("r1"), s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("resubmit: want ErrInvalidCommand, got %v", err)
	}
}
