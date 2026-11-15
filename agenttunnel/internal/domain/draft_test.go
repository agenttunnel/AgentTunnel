package domain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestDecideDraftCreateCarriesDraftFields(t *testing.T) {
	issue := &contracts.IssueRef{Provider: "gitlab", IID: 3, Title: "Fix login", URL: "https://gitlab.example/g/p/-/issues/3"}
	evs, err := Decide(contracts.Command{
		Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "Login",
		Draft: true, Description: "the form", Issue: issue,
	}, NewThreadState("t1"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	created := evs[0].(contracts.ThreadCreated)
	if !created.Draft || created.Description != "the form" || created.Issue != issue {
		t.Fatalf("created = %+v, want the draft fields", created)
	}
}

func TestDecideDescribeOnlyTakesDrafts(t *testing.T) {
	live := NewThreadState("t1")
	live.Exists = true

	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadDescribe, ThreadID: "t1", Description: "x"}, live); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("describe on a running thread: err = %v, want ErrInvalidCommand", err)
	}
	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadDescribe, ThreadID: "t9", Description: "x"}, NewThreadState("t9")); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("describe on a missing thread: err = %v, want ErrThreadNotFound", err)
	}

	draft := NewThreadState("t1")
	draft.Exists, draft.Draft = true, true
	evs, err := Decide(contracts.Command{Type: contracts.CmdThreadDescribe, ThreadID: "t1", Description: "the plan"}, draft)
	if err != nil {
		t.Fatalf("describe draft: %v", err)
	}
	if got := evs[0].(contracts.ThreadDescribed).Description; got != "the plan" {
		t.Fatalf("description = %q, want %q", got, "the plan")
	}

	tooLong := strings.Repeat("é", maxDescriptionRunes+1)
	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadDescribe, ThreadID: "t1", Description: tooLong}, draft); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("over-long description: err = %v, want ErrInvalidCommand", err)
	}
}

// The first turn on a draft also starts it, after the user's message, so the
// projection flips the flag in the same durable write as the prompt.
func TestDecideTurnStartOnDraftStartsIt(t *testing.T) {
	draft := NewThreadState("t1")
	draft.Exists, draft.Draft = true, true
	evs, err := Decide(contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "go"}, draft)
	if err != nil {
		t.Fatalf("turn on draft: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("payloads = %#v, want message then draft-started", evs)
	}
	if _, ok := evs[0].(contracts.MessageSent); !ok {
		t.Fatalf("first payload = %T, want MessageSent", evs[0])
	}
	if _, ok := evs[1].(contracts.DraftStarted); !ok {
		t.Fatalf("second payload = %T, want DraftStarted", evs[1])
	}

	live := NewThreadState("t1")
	live.Exists = true
	evs, err = Decide(contracts.Command{ID: "m2", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "go"}, live)
	if err != nil {
		t.Fatalf("turn on thread: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("payloads on a running thread = %#v, want only the message", evs)
	}
}

func TestProjectDraftLifecycle(t *testing.T) {
	s := NewThreadState("t1")
	apply := func(p contracts.EventPayload) {
		t.Helper()
		if err := Project(s, contracts.Event{Seq: 1, ThreadID: "t1", Payload: p}); err != nil {
			t.Fatalf("project %T: %v", p, err)
		}
	}
	apply(contracts.ThreadCreated{Title: "Login", Draft: true, Description: "first"})
	if !s.Draft || s.Description != "first" {
		t.Fatalf("after create: draft=%v description=%q", s.Draft, s.Description)
	}
	apply(contracts.ThreadDescribed{Description: "second"})
	if s.Description != "second" || !s.Draft {
		t.Fatalf("after describe: draft=%v description=%q", s.Draft, s.Description)
	}
	apply(contracts.DraftStarted{})
	if s.Draft {
		t.Fatal("draft-started left the thread a draft")
	}
	if s.Description != "second" {
		t.Fatalf("starting dropped the description: %q", s.Description)
	}
}

// A draft is the user's work and must survive a restart, which is why it is
// written at once instead of living in the provisional in-memory set.
func TestDraftIsDurableAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	issue := &contracts.IssueRef{Provider: "gitlab", IID: 9, Title: "Crash on save", URL: "https://gitlab.example/g/p/-/issues/9"}

	store1, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e1 := NewEngine(store1, nil)
	e1.Start(ctx)
	if err := e1.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "Crash",
		Draft: true, Description: "repro steps", Issue: issue,
	}); err != nil {
		t.Fatalf("create draft: %v", err)
	}
	evs, err := store1.Load(ctx, "t1")
	if err != nil || len(evs) == 0 {
		t.Fatalf("draft wrote no durable events: %v (%d)", err, len(evs))
	}
	e1.Close()

	store2, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(store2, nil)
	e2.Start(ctx)
	t.Cleanup(e2.Close)
	st, err := e2.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || !st.Draft || st.Description != "repro steps" || st.Issue == nil || st.Issue.URL != issue.URL {
		t.Fatalf("after restart: %+v, want the draft with its description and issue", st)
	}
}
