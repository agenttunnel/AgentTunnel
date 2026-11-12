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
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d", len(evs))
	}
	if _, ok := evs[0].(contracts.ThreadCreated); !ok {
		t.Fatalf("want ThreadCreated, got %T", evs[0])
	}
	// Creation also records the peer mailbox on, so new threads carry agent
	// traffic (and graph arrows) without a separate opt-in.
	if on, ok := evs[1].(contracts.PeerMailboxSet); !ok || !on.Enabled {
		t.Fatalf("want PeerMailboxSet{true}, got %#v", evs[1])
	}
	// New threads report workflow status on a default period until changed.
	if p, ok := evs[2].(contracts.StatusPeriodSet); !ok || p.PeriodSeconds != contracts.DefaultStatusPeriodSeconds {
		t.Fatalf("want StatusPeriodSet{%d}, got %#v", contracts.DefaultStatusPeriodSeconds, evs[2])
	}
}

func TestDecideStatusPeriod(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true

	if _, err := Decide(contracts.Command{Type: contracts.CmdStatusPeriodSet}, s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("missing value: want ErrInvalidCommand, got %v", err)
	}
	neg := -1
	if _, err := Decide(contracts.Command{Type: contracts.CmdStatusPeriodSet, StatusPeriod: &neg}, s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("negative period: want ErrInvalidCommand, got %v", err)
	}
	tooBig := contracts.MaxStatusPeriodSeconds + 1
	if _, err := Decide(contracts.Command{Type: contracts.CmdStatusPeriodSet, StatusPeriod: &tooBig}, s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("oversized period: want ErrInvalidCommand, got %v", err)
	}

	zero := 0
	evs, err := Decide(contracts.Command{Type: contracts.CmdStatusPeriodSet, StatusPeriod: &zero}, s)
	if err != nil || len(evs) != 1 {
		t.Fatalf("zero period: err=%v events=%d", err, len(evs))
	}
	if p := evs[0].(contracts.StatusPeriodSet); p.PeriodSeconds != 0 {
		t.Fatalf("zero period projected %d", p.PeriodSeconds)
	}

	if _, err := Decide(contracts.Command{Type: contracts.CmdStatusPeriodSet, StatusPeriod: &zero}, NewThreadState("t1")); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("unknown thread: want ErrThreadNotFound, got %v", err)
	}
}

func TestProjectStatusPeriod(t *testing.T) {
	s := NewThreadState("t1")
	if err := Project(s, contracts.Event{Payload: contracts.StatusPeriodSet{PeriodSeconds: 30}}); err != nil {
		t.Fatal(err)
	}
	if s.StatusPeriodSeconds != 30 {
		t.Fatalf("status period = %d, want 30", s.StatusPeriodSeconds)
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

func TestDecideDispatchRejectsUnknownHarness(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	_, err := Decide(contracts.Command{
		Type:     contracts.CmdTaskDispatch,
		ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{AgentID: "a1", Task: "do it", Harness: "nope"},
	}, s)
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("want ErrInvalidCommand, got %v", err)
	}
}

func TestDecideDispatchAcceptsKnownHarness(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	evs, err := Decide(contracts.Command{
		Type:     contracts.CmdTaskDispatch,
		ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{
			AgentID: "a1", Task: "do it", Harness: contracts.HarnessClaude,
		},
	}, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evs[0].(contracts.TaskDispatched).Harness != contracts.HarnessClaude {
		t.Fatalf("harness not carried: %+v", evs[0])
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
