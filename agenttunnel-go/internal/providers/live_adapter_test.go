package providers

import (
	"context"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestLiveOpenCodeAdapterStreams(t *testing.T) {
	requireLive(t)
	driver, ok := BuiltinRegistry().Get(contracts.HarnessOpenCode)
	if !ok {
		t.Fatal("no opencode driver")
	}

	a := NewProcessAdapter(driver)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	spec := SessionSpec{
		AgentID: "a1",
		Harness: contracts.HarnessOpenCode,
		Prompt:  "Reply with only the single word: pong",
	}
	if err := a.Start(ctx, spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	events, err := a.Stream(ctx)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	count := 0
	for ev := range events {
		count++
		t.Logf("event %s: %q", ev.Kind, ev.Text)
	}
	if werr := a.Wait(ctx); werr != nil {
		t.Errorf("adapter exited with error: %v", werr)
	}
	if count == 0 {
		t.Fatal("adapter produced no events")
	}
	t.Logf("adapter produced %d events", count)
}
