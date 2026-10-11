package domain

import (
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestPublishStreamsWithoutPersisting(t *testing.T) {
	e, ctx := newTestEngine(t)
	ch, unsub := e.Subscribe("t1")
	defer unsub()

	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	select {
	case <-ch: // drain the committed thread.created event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the create event")
	}

	e.Publish("t1", contracts.AgentOutput{AgentID: "a1", Kind: contracts.LogText, Text: "hello"})

	select {
	case ev := <-ch:
		if ev.Type() != contracts.EvtAgentOutput {
			t.Fatalf("got event %s, want agent.output", ev.Type())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the published event")
	}

	// The transient event must never reach the event log.
	events, err := e.store.Load(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type() == contracts.EvtAgentOutput {
			t.Fatal("transient agent.output event was persisted")
		}
	}
}
