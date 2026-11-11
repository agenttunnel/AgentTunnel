package domain

import (
	"errors"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func archiveCmd(v bool) contracts.Command {
	return contracts.Command{Type: contracts.CmdArchiveSet, ThreadID: "t1", Archived: &v}
}

func TestDecideArchiveSetsFlag(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	evs, err := Decide(archiveCmd(true), s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, ok := evs[0].(contracts.ArchiveSet); !ok || !got.Archived {
		t.Fatalf("want ArchiveSet{Archived: true}, got %#v", evs[0])
	}
}

func TestDecideUnarchiveAllowedWhileRunning(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	s.Agents["sub1"] = contracts.Agent{Status: contracts.AgentRunning}
	if _, err := Decide(archiveCmd(false), s); err != nil {
		t.Fatalf("restoring a thread should not need its agents stopped: %v", err)
	}
}

func TestDecideArchiveRejectsRunningAgents(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	s.Agents["sub1"] = contracts.Agent{Status: contracts.AgentRunning}
	if _, err := Decide(archiveCmd(true), s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("want ErrInvalidCommand, got %v", err)
	}
}

func TestDecideArchiveRequiresExistingThread(t *testing.T) {
	if _, err := Decide(archiveCmd(true), NewThreadState("t1")); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("want ErrThreadNotFound, got %v", err)
	}
}

func TestDecideArchiveRequiresValue(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	_, err := Decide(contracts.Command{Type: contracts.CmdArchiveSet, ThreadID: "t1"}, s)
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("want ErrInvalidCommand, got %v", err)
	}
}
