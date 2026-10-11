package domain

import (
	"errors"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestDecideThreadRename(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true

	evs, err := Decide(contracts.Command{
		Type:     contracts.CmdThreadRename,
		ThreadID: "t1",
		Title:    "  Parse the config  ",
	}, s)
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := evs[0].(contracts.ThreadRenamed).Title; got != "Parse the config" {
		t.Fatalf("title = %q", got)
	}

	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadRename, ThreadID: "t1", Title: "  "}, s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("blank title should be rejected, got %v", err)
	}
	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadRename, Title: "x"}, NewThreadState("t9")); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("rename on a missing thread should fail, got %v", err)
	}
}

func TestProjectThreadRename(t *testing.T) {
	s := NewThreadState("t1")
	if err := Project(s, contracts.Event{Seq: 1, ThreadID: "t1", Payload: contracts.ThreadCreated{Title: contracts.DefaultThreadTitle}}); err != nil {
		t.Fatal(err)
	}
	if err := Project(s, contracts.Event{Seq: 2, ThreadID: "t1", Payload: contracts.ThreadRenamed{Title: "Named by the model"}}); err != nil {
		t.Fatal(err)
	}
	if s.Title != "Named by the model" {
		t.Fatalf("title = %q", s.Title)
	}
}
