package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestMailboxProjectionCarriesEventTime(t *testing.T) {
	s := NewThreadState("t1")
	at := time.Unix(1700000000, 0).UTC()
	ev := contracts.Event{
		Seq: 1, ThreadID: "t1", At: at,
		Payload: contracts.MailboxPosted{From: "a", To: "b", Kind: "message", Body: "hi"},
	}
	if err := Project(s, ev); err != nil {
		t.Fatalf("project: %v", err)
	}
	if len(s.Mailbox) != 1 {
		t.Fatalf("mailbox = %+v", s.Mailbox)
	}
	got := s.Mailbox[0]
	if got.From != "a" || got.To != "b" || got.Kind != "message" || got.Body != "hi" || !got.At.Equal(at) {
		t.Fatalf("entry = %+v", got)
	}
}

func TestMailboxProjectionStampsTurnAndBoundsEntries(t *testing.T) {
	s := NewThreadState("t1")
	project := func(ev contracts.Event) {
		t.Helper()
		if err := Project(s, ev); err != nil {
			t.Fatalf("project: %v", err)
		}
	}
	// A user message opens turn 1; the mailbox traffic that follows is
	// stamped with the turn it belongs to.
	project(contracts.Event{Seq: 1, ThreadID: "t1", Payload: contracts.MessageSent{MessageID: "m", Author: "user", Text: "work"}})
	project(contracts.Event{Seq: 2, ThreadID: "t1", Payload: contracts.MailboxPosted{From: "a", To: "b", Kind: "message", Body: "one"}})
	if len(s.Mailbox) != 1 || s.Mailbox[0].Turn != 1 {
		t.Fatalf("entry = %+v", s.Mailbox)
	}

	// The read model keeps the newest maxMailboxEntries — thread.get ships
	// the whole list, so it must not grow with the life of the thread. The
	// durable event log keeps everything.
	for i := 0; i < maxMailboxEntries+5; i++ {
		project(contracts.Event{
			Seq: int64(3 + i), ThreadID: "t1",
			Payload: contracts.MailboxPosted{From: "a", To: "b", Kind: "message", Body: fmt.Sprintf("m%d", i)},
		})
	}
	if len(s.Mailbox) != maxMailboxEntries {
		t.Fatalf("len = %d, want %d", len(s.Mailbox), maxMailboxEntries)
	}
	if first, last := s.Mailbox[0].Body, s.Mailbox[len(s.Mailbox)-1].Body; first != "m5" || last != "m204" {
		t.Fatalf("oldest entries must drop: first=%q last=%q", first, last)
	}
}

func TestDecideMailboxPostIsGatedByPeerMailbox(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	cmd := contracts.Command{
		Type:     contracts.CmdMailboxPost,
		ThreadID: "t1",
		Mailbox:  &contracts.MailboxPosted{From: "a", To: "b", Body: "hi"},
	}

	if _, err := Decide(cmd, s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("want ErrInvalidCommand while peer mailbox is off, got %v", err)
	}

	s.PeerMailbox = true
	if _, err := Decide(cmd, s); err != nil {
		t.Fatalf("unexpected error once enabled: %v", err)
	}
}

func TestDecideReviewCommentRequiresFileAndBody(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	base := contracts.Command{Type: contracts.CmdReviewCommentAdd, ThreadID: "t1"}

	for _, c := range []*contracts.ReviewCommentAdded{
		nil,
		{File: "a.go"},
		{Body: "looks good"},
	} {
		cmd := base
		cmd.ReviewComment = c
		if _, err := Decide(cmd, s); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("comment %+v: want ErrInvalidCommand, got %v", c, err)
		}
	}

	cmd := base
	cmd.ReviewComment = &contracts.ReviewCommentAdded{ID: "r1", File: "a.go", Line: 3, Body: "please rename"}
	evs, err := Decide(cmd, s)
	if err != nil || len(evs) != 1 {
		t.Fatalf("valid comment rejected: err=%v events=%d", err, len(evs))
	}
}

func TestDecideStatusForUnknownAgentRejected(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	_, err := Decide(contracts.Command{
		Type:     contracts.CmdAgentStatusSet,
		ThreadID: "t1",
		Status:   &contracts.AgentStatusChanged{AgentID: "ghost", Status: contracts.AgentRunning},
	}, s)
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("want ErrInvalidCommand, got %v", err)
	}
}
