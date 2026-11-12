package domain

import (
	"errors"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Injection is recorded as its own durable event, and the read model passes
// through it unchanged: the queued message is what the transcript keeps.
func TestSteeringInjectedDecidesOneEventAndLeavesStateAlone(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	before := len(s.Messages)

	events, err := Decide(contracts.Command{
		Type:     contracts.CmdSteeringInjected,
		ThreadID: "t1",
		Injected: &contracts.SteeringInjected{Count: 2},
	}, s)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if len(events) != 1 || events[0] != (contracts.SteeringInjected{Count: 2}) {
		t.Fatalf("events = %+v, want one SteeringInjected{Count: 2}", events)
	}
	if err := Project(s, contracts.Event{ThreadID: "t1", Payload: events[0]}); err != nil {
		t.Fatalf("project: %v", err)
	}
	if len(s.Messages) != before {
		t.Fatalf("messages = %d, want unchanged %d", len(s.Messages), before)
	}
}

func TestSteeringInjectedRejectsEmptyAndUnknownThreads(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true

	if _, err := Decide(contracts.Command{
		Type: contracts.CmdSteeringInjected, ThreadID: "t1", Injected: &contracts.SteeringInjected{Count: 0},
	}, s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("count 0: err = %v, want ErrInvalidCommand", err)
	}

	missing := NewThreadState("t2")
	if _, err := Decide(contracts.Command{
		Type: contracts.CmdSteeringInjected, ThreadID: "t2", Injected: &contracts.SteeringInjected{Count: 1},
	}, missing); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("missing thread: err = %v, want ErrThreadNotFound", err)
	}
}
