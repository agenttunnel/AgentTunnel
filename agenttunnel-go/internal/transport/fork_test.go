package transport

import (
	"context"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

func TestThreadForkCopiesHistoryUpToMessage(t *testing.T) {
	f := newDraftFixture(t)
	src := f.mustCall("thread.create", map[string]any{"title": "Login", "projectId": "p1"}).(map[string]any)["threadId"].(contracts.ThreadID)
	for _, m := range []contracts.MessageSent{
		{MessageID: "m1", Author: "user", Text: "first ask"},
		{MessageID: "m2", Author: "orchestrator", Text: "first answer"},
		{MessageID: "m3", Author: "user", Text: "second ask"},
	} {
		msg := m
		if err := f.eng.Dispatch(f.ctx, contracts.Command{ID: "append-" + msg.MessageID, Type: contracts.CmdMessageAppend, ThreadID: src, Message: &msg}); err != nil {
			t.Fatal(err)
		}
	}

	out := f.mustCall("thread.fork", map[string]any{"threadId": src, "messageId": "m2"})
	forkID := out.(map[string]any)["threadId"].(contracts.ThreadID)
	if forkID == src || forkID == "" {
		t.Fatalf("fork id = %q, want a new thread", forkID)
	}

	fork := f.threadState(forkID)
	if !fork.Exists || fork.Title != "Login (branch)" || fork.ProjectID != "p1" || fork.ParentThreadID != "" {
		t.Fatalf("fork = %+v, want a top-level thread titled from the source", fork)
	}
	if len(fork.Messages) != 2 || fork.Messages[0].ID != "m1" || fork.Messages[1].ID != "m2" {
		t.Fatalf("fork messages = %+v, want m1..m2 only", fork.Messages)
	}
	if got := f.threadState(src); len(got.Messages) != 3 {
		t.Fatalf("source messages = %d, want 3 untouched", len(got.Messages))
	}
}

func (f *draftFixture) threadState(id contracts.ThreadID) *domain.ThreadState {
	f.t.Helper()
	st, err := f.eng.State(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func TestThreadForkRejectsUnknownThreadAndMessage(t *testing.T) {
	f := newDraftFixture(t)
	src := f.mustCall("thread.create", map[string]any{"title": "Login", "projectId": "p1"}).(map[string]any)["threadId"].(contracts.ThreadID)

	cases := map[string]map[string]any{
		"unknown thread":  {"threadId": "th_missing", "messageId": "m1"},
		"unknown message": {"threadId": src, "messageId": "m9"},
		"empty message":   {"threadId": src, "messageId": ""},
	}
	for name, params := range cases {
		if _, err := f.call("thread.fork", params); err == nil {
			t.Fatalf("%s: fork accepted", name)
		}
	}
	if threads, _ := f.eng.Threads(context.Background()); len(threads) != 1 {
		t.Fatalf("rejected forks left threads behind: %v", threads)
	}
}
