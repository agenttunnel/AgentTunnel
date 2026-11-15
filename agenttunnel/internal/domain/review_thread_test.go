package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

var reviewAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// reviewThreadState is a thread with one reviewer, a second reviewer and one
// executor, all dispatched, so the role rules have agents to check against.
func reviewThreadState(t *testing.T) *ThreadState {
	t.Helper()
	s := NewThreadState("t1")
	for _, p := range []contracts.EventPayload{
		contracts.ThreadCreated{Title: "review"},
		contracts.TaskDispatched{AgentID: "rev", Role: contracts.RoleReviewer},
		contracts.TaskDispatched{AgentID: "rev2", Role: contracts.RoleReviewer},
		contracts.TaskDispatched{AgentID: "exe", Role: contracts.RoleExecutor},
	} {
		if err := Project(s, contracts.Event{Seq: s.Version + 1, ThreadID: "t1", At: reviewAt, Payload: p}); err != nil {
			t.Fatalf("project %T: %v", p, err)
		}
	}
	return s
}

// apply decides a command and projects its events, the way the engine does.
func apply(s *ThreadState, cmd contracts.Command) error {
	evs, err := Decide(cmd, s)
	if err != nil {
		return err
	}
	for _, p := range evs {
		if err := Project(s, contracts.Event{Seq: s.Version + 1, ThreadID: "t1", At: reviewAt, Payload: p}); err != nil {
			return err
		}
	}
	return nil
}

func openThread(id, author string) contracts.Command {
	return contracts.Command{Type: contracts.CmdReviewThreadOpen, ThreadID: "t1", ReviewOpen: &contracts.ReviewThreadOpened{
		ReviewCommentAdded: contracts.ReviewCommentAdded{ID: id, File: "a.go", Line: 3, Body: "rename", AuthorID: contracts.AgentID(author), CreatedAt: reviewAt},
	}}
}

func replyThread(thread, author, body string) contracts.Command {
	return contracts.Command{Type: contracts.CmdReviewThreadReply, ThreadID: "t1", ReviewReply: &contracts.ReviewThreadReplied{
		ThreadID: thread, AuthorID: contracts.AgentID(author), Body: body, CreatedAt: reviewAt,
	}}
}

func statusThread(thread, author string, status contracts.ReviewStatus, body string) contracts.Command {
	return contracts.Command{Type: contracts.CmdReviewThreadStatus, ThreadID: "t1", ReviewStatus: &contracts.ReviewThreadStatusChanged{
		ThreadID: thread, AuthorID: contracts.AgentID(author), Status: status, Body: body, CreatedAt: reviewAt,
	}}
}

func TestDecideReviewThreadOpenOnlyByReviewer(t *testing.T) {
	s := reviewThreadState(t)
	if err := apply(s, openThread("rt1", "exe")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("executor open: want ErrInvalidCommand, got %v", err)
	}
	if err := apply(s, openThread("rt1", "ghost")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unknown author open: want ErrInvalidCommand, got %v", err)
	}
	if err := apply(s, openThread("rt1", "rev")); err != nil {
		t.Fatalf("reviewer open: %v", err)
	}
	if got := s.ReviewThreads[0]; got.ID != "rt1" || got.Status != contracts.ReviewStatusOpen || got.EndLine != 3 || got.AuthorID != "rev" {
		t.Fatalf("opened thread = %+v", got)
	}
	if err := apply(s, openThread("rt1", "rev")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("duplicate id: want ErrInvalidCommand, got %v", err)
	}
}

func TestDecideReviewThreadReplyNeedsAgentAndKnownThread(t *testing.T) {
	s := reviewThreadState(t)
	if err := apply(s, openThread("rt1", "rev")); err != nil {
		t.Fatal(err)
	}
	if err := apply(s, replyThread("rt1", "exe", "fixed")); err != nil {
		t.Fatalf("executor reply: %v", err)
	}
	if err := apply(s, replyThread("rt1", "rev", "thanks")); err != nil {
		t.Fatalf("reviewer reply: %v", err)
	}
	if err := apply(s, replyThread("rt1", "user", "hi")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("non-agent reply: want ErrInvalidCommand, got %v", err)
	}
	if err := apply(s, replyThread("nope", "exe", "hi")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unknown thread reply: want ErrInvalidCommand, got %v", err)
	}
	if got := s.ReviewThreads[0]; got.Status != contracts.ReviewStatusOpen || len(got.Replies) != 2 {
		t.Fatalf("after replies: status=%s replies=%d, want open with 2", got.Status, len(got.Replies))
	}
}

func TestDecideReviewThreadStatusOnlyByOriginalReviewer(t *testing.T) {
	s := reviewThreadState(t)
	if err := apply(s, openThread("rt1", "rev")); err != nil {
		t.Fatal(err)
	}
	for _, author := range []string{"exe", "rev2"} {
		if err := apply(s, statusThread("rt1", author, contracts.ReviewStatusResolved, "")); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("status by %s: want ErrInvalidCommand, got %v", author, err)
		}
	}
	if err := apply(s, statusThread("missing", "rev", contracts.ReviewStatusResolved, "")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unknown thread status: want ErrInvalidCommand, got %v", err)
	}
	if err := apply(s, statusThread("rt1", "rev", "maybe", "")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unknown status: want ErrInvalidCommand, got %v", err)
	}
	if err := apply(s, statusThread("rt1", "rev", contracts.ReviewStatusResolved, "fixed in abc")); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := s.ReviewThreads[0]; got.Status != contracts.ReviewStatusResolved || len(got.Replies) != 1 || got.Replies[0].Body != "fixed in abc" {
		t.Fatalf("resolved thread = %+v", got)
	}
	if err := apply(s, statusThread("rt1", "rev", contracts.ReviewStatusOpen, "")); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s.ReviewThreads[0].Status; got != contracts.ReviewStatusOpen {
		t.Fatalf("status after reopen = %s", got)
	}
}

func TestOpenReviewThreadsKeepsOnlyOpen(t *testing.T) {
	s := reviewThreadState(t)
	for _, id := range []string{"rt1", "rt2", "rt3"} {
		if err := apply(s, openThread(id, "rev")); err != nil {
			t.Fatal(err)
		}
	}
	if err := apply(s, statusThread("rt2", "rev", contracts.ReviewStatusResolved, "")); err != nil {
		t.Fatal(err)
	}
	open := OpenReviewThreads(s)
	if len(open) != 2 || open[0].ID != "rt1" || open[1].ID != "rt3" {
		t.Fatalf("open threads = %+v, want rt1 then rt3", open)
	}
}

// Replaying the persisted log, decoded from its JSON, must rebuild the live
// state exactly: that is what the engine does on restart.
func TestReviewThreadReplayMatchesLive(t *testing.T) {
	live := reviewThreadState(t)
	log := []contracts.Event{}
	record := func(cmd contracts.Command) {
		t.Helper()
		evs, err := Decide(cmd, live)
		if err != nil {
			t.Fatalf("decide %s: %v", cmd.Type, err)
		}
		for _, p := range evs {
			ev := contracts.Event{Seq: live.Version + 1, ThreadID: "t1", At: reviewAt, Payload: p}
			if err := Project(live, ev); err != nil {
				t.Fatalf("project: %v", err)
			}
			log = append(log, ev)
		}
	}
	record(openThread("rt1", "rev"))
	record(replyThread("rt1", "exe", "fixed"))
	record(openThread("rt2", "rev"))
	record(statusThread("rt1", "rev", contracts.ReviewStatusResolved, "thanks"))
	record(replyThread("rt2", "rev2", "not mine"))

	replayed := NewThreadState("t1")
	for _, ev := range log {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		var back contracts.Event
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatal(err)
		}
		if err := Project(replayed, back); err != nil {
			t.Fatalf("replay %s: %v", back.Type(), err)
		}
	}
	if !reflect.DeepEqual(live.ReviewThreads, replayed.ReviewThreads) {
		t.Fatalf("replay diverged\nlive:     %+v\nreplayed: %+v", live.ReviewThreads, replayed.ReviewThreads)
	}
}

func TestCloneDoesNotAliasReviewThreads(t *testing.T) {
	s := reviewThreadState(t)
	if err := apply(s, openThread("rt1", "rev")); err != nil {
		t.Fatal(err)
	}
	if err := apply(s, replyThread("rt1", "exe", "fixed")); err != nil {
		t.Fatal(err)
	}
	c := s.Clone()
	c.ReviewThreads[0].Status = contracts.ReviewStatusResolved
	c.ReviewThreads[0].Replies[0].Body = "changed"
	c.ReviewThreads[0].Replies = append(c.ReviewThreads[0].Replies, ReviewReply{Body: "extra"})

	if got := s.ReviewThreads[0]; got.Status != contracts.ReviewStatusOpen || len(got.Replies) != 1 || got.Replies[0].Body != "fixed" {
		t.Fatalf("clone mutation leaked into the original: %+v", got)
	}
}

func TestReviewThreadCapsRefuseNewWork(t *testing.T) {
	s := reviewThreadState(t)
	for i := 0; i < maxReviewThreads; i++ {
		if err := apply(s, openThread(string(rune(0x4e00+i)), "rev")); err != nil {
			t.Fatalf("thread %d: %v", i, err)
		}
	}
	if err := apply(s, openThread("over", "rev")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("thread past the cap: want ErrInvalidCommand, got %v", err)
	}

	first := s.ReviewThreads[0].ID
	for i := 0; i < maxReviewReplies; i++ {
		if err := apply(s, replyThread(first, "exe", "reply")); err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
	}
	if err := apply(s, replyThread(first, "exe", "one more")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("reply past the cap: want ErrInvalidCommand, got %v", err)
	}
	if err := apply(s, statusThread(first, "rev", contracts.ReviewStatusResolved, "note")); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("status note past the reply cap: want ErrInvalidCommand, got %v", err)
	}
	if err := apply(s, statusThread(first, "rev", contracts.ReviewStatusResolved, "")); err != nil {
		t.Fatalf("bare status change at the reply cap: %v", err)
	}
}
