package domain

import (
	"errors"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestDecideThreadCreate(t *testing.T) {
	s := NewThreadState("t1")
	evs, err := Decide(contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1"}, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if _, ok := evs[0].(contracts.ThreadCreated); !ok {
		t.Fatalf("want ThreadCreated, got %T", evs[0])
	}
}

func TestDecideThreadCreateDefaultsTitle(t *testing.T) {
	evs, _ := Decide(contracts.Command{Type: contracts.CmdThreadCreate}, NewThreadState("t1"))
	created := evs[0].(contracts.ThreadCreated)
	if created.Title != contracts.DefaultThreadTitle {
		t.Fatalf("want default title, got %q", created.Title)
	}
}

func TestDecideDuplicateThread(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	_, err := Decide(contracts.Command{Type: contracts.CmdThreadCreate}, s)
	if !errors.Is(err, ErrThreadExists) {
		t.Fatalf("want ErrThreadExists, got %v", err)
	}
}

func TestDecideTurnRequiresExistingThread(t *testing.T) {
	_, err := Decide(contracts.Command{Type: contracts.CmdTurnStart, Text: "hi"}, NewThreadState("t1"))
	if !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("want ErrThreadNotFound, got %v", err)
	}
}

func TestDecideTurnRejectsEmptyText(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	for _, text := range []string{"", "   ", "\n\t"} {
		_, err := Decide(contracts.Command{Type: contracts.CmdTurnStart, Text: text}, s)
		if !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("text %q: want ErrInvalidCommand, got %v", text, err)
		}
	}
}

func TestDecideUnknownCommand(t *testing.T) {
	_, err := Decide(contracts.Command{Type: "nope"}, NewThreadState("t1"))
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("want ErrInvalidCommand, got %v", err)
	}
}

func TestDecideDispatchDefaultsRoleToExecutor(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	evs, err := Decide(contracts.Command{
		Type:     contracts.CmdTaskDispatch,
		ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{AgentID: "a1", Task: "do it"},
	}, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dispatched := evs[0].(contracts.TaskDispatched)
	if dispatched.Role != contracts.RoleExecutor {
		t.Fatalf("want executor default, got %q", dispatched.Role)
	}
}

func TestDecideDispatchRequiresAgentID(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	_, err := Decide(contracts.Command{
		Type:     contracts.CmdTaskDispatch,
		ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{Task: "do it"},
	}, s)
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("want ErrInvalidCommand, got %v", err)
	}
}
