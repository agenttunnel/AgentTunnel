package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestDecideThreadDelete(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	evs, err := Decide(contracts.Command{Type: contracts.CmdThreadDelete, ThreadID: "t1"}, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := evs[0].(contracts.ThreadDeleted); !ok {
		t.Fatalf("want ThreadDeleted, got %T", evs[0])
	}
}

func TestDecideThreadDeleteRequiresExistingThread(t *testing.T) {
	_, err := Decide(contracts.Command{Type: contracts.CmdThreadDelete, ThreadID: "t1"}, NewThreadState("t1"))
	if !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("want ErrThreadNotFound, got %v", err)
	}
}

func TestDecideThreadDeleteRejectsRunningAgents(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	s.Agents["orch"] = contracts.Agent{Status: contracts.AgentSucceeded}
	s.Agents["sub1"] = contracts.Agent{Status: contracts.AgentRunning}
	_, err := Decide(contracts.Command{Type: contracts.CmdThreadDelete, ThreadID: "t1"}, s)
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("want ErrInvalidCommand, got %v", err)
	}
}

func TestDecideThreadDeleteAllowsSettledAgents(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	for i, st := range []contracts.AgentStatus{
		contracts.AgentSucceeded, contracts.AgentFailed, contracts.AgentCancelled,
	} {
		s.Agents[contracts.AgentID(fmt.Sprintf("a%d", i))] = contracts.Agent{Status: st}
	}
	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadDelete, ThreadID: "t1"}, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProjectThreadDeletedTombstones(t *testing.T) {
	s := NewThreadState("t1")
	evs, _ := Decide(contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1"}, s)
	for _, ev := range evs {
		if err := Project(s, contracts.Event{ThreadID: "t1", Payload: ev}); err != nil {
			t.Fatalf("project create: %v", err)
		}
	}
	if !s.Exists || s.Deleted {
		t.Fatalf("after create: exists=%v deleted=%v", s.Exists, s.Deleted)
	}
	del, _ := Decide(contracts.Command{Type: contracts.CmdThreadDelete, ThreadID: "t1"}, s)
	for _, ev := range del {
		if err := Project(s, contracts.Event{ThreadID: "t1", Payload: ev}); err != nil {
			t.Fatalf("project delete: %v", err)
		}
	}
	if s.Exists || !s.Deleted {
		t.Fatalf("after delete: exists=%v deleted=%v", s.Exists, s.Deleted)
	}
	// A tombstoned thread refuses further commands.
	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadRename, ThreadID: "t1", Title: "x"}, s); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("rename on deleted: want ErrThreadNotFound, got %v", err)
	}
}
