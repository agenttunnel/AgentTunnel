package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestReviewRequestRendersRangeAndRemovedSide(t *testing.T) {
	got := ReviewRequest([]contracts.ReviewCommentAdded{
		{File: "main.go", Line: 12, EndLine: 14, Body: "extract this block"},
		{File: "old.go", Line: 4, EndLine: 4, Side: "old", Body: "why was this removed"},
		{File: "gone.go", Line: 9, EndLine: 11, Side: "old", Body: "keep it"},
	})
	for _, want := range []string{
		"- main.go:12-14: extract this block\n",
		"- old.go:4 (removed lines): why was this removed\n",
		"- gone.go:9-11 (removed lines): keep it",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("request missing %q:\n%s", want, got)
		}
	}
}

func TestDecideReviewCommentRejectsBadRange(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	base := contracts.Command{Type: contracts.CmdReviewCommentAdd, ThreadID: "t1"}

	for _, c := range []contracts.ReviewCommentAdded{
		{File: "a.go", Line: 5, EndLine: 4, Body: "backwards"},
		{File: "a.go", Line: 5, EndLine: 6, Side: "left", Body: "bad side"},
	} {
		cmd := base
		cmd.ReviewComment = &c
		if _, err := Decide(cmd, s); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("comment %+v: want ErrInvalidCommand, got %v", c, err)
		}
	}

	cmd := base
	cmd.ReviewComment = &contracts.ReviewCommentAdded{ID: "r1", File: "a.go", Line: 5, EndLine: 9, Side: "old", Body: "ok"}
	if evs, err := Decide(cmd, s); err != nil || len(evs) != 1 {
		t.Fatalf("valid range rejected: err=%v events=%d", err, len(evs))
	}
}

func TestProjectReviewCommentWithoutEndLineSpansOneLine(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	// Comments recorded before ranges existed carry no endLine.
	if err := Project(s, contracts.Event{Seq: 1, ThreadID: "t1", Payload: contracts.ReviewCommentAdded{
		ID: "r1", File: "a.go", Line: 7, Body: "rename",
	}}); err != nil {
		t.Fatalf("project: %v", err)
	}
	if got := s.ReviewComments[0].EndLine; got != 7 {
		t.Fatalf("EndLine = %d, want 7 (single line)", got)
	}
}
