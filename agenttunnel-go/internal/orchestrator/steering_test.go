package orchestrator

import (
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A steer that arrives mid-turn is drained by the next pass. That pass records
// the consumption in the transcript, so the client can retire the pending style
// exactly when the agent has actually received it.
func TestRunTurnMarksDrainedSteeringConsumed(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)

	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "s1", Type: contracts.CmdMessageAppend, ThreadID: "t1",
		Message: &contracts.MessageSent{MessageID: "steer-1", Author: "steering", Text: "focus on the parser"},
	}); err != nil {
		t.Fatal(err)
	}
	r.steering.Post("t1", "steer-1", "focus on the parser")

	events, unsub := eng.Subscribe("t1")
	defer unsub()

	if err := New(r).RunTurn(ctx, "t1", "do the thing"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	// The signal is the typed event, delivered on the same stream live clients
	// subscribe to.
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type() != contracts.EvtSteeringConsumed {
				continue
			}
			p, ok := ev.Payload.(contracts.SteeringConsumed)
			if !ok || len(p.MessageIDs) != 1 || p.MessageIDs[0] != "steer-1" {
				t.Fatalf("consumed event = %+v", ev.Payload)
			}
			state, err := eng.State(ctx, "t1")
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range state.Messages {
				if m.ID == "steer-1" {
					if !m.Consumed {
						t.Fatalf("steering message was not projected as consumed: %+v", m)
					}
					return
				}
			}
			t.Fatalf("steering message missing from the read model: %+v", state.Messages)
		case <-deadline:
			t.Fatal("timed out waiting for the steering-consumed event")
		}
	}
}
