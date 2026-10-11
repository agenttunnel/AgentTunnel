package providers

import (
	"context"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// scriptDriver runs a shell snippet as if it were a harness CLI, so the process
// plumbing can be exercised without a real provider installed.
func scriptDriver(script string) Driver {
	return NewGenericDriver(contracts.HarnessOpenCode, "sh", func(SessionSpec) (CommandSpec, error) {
		return CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
	}, JSONLineTranslator{})
}

func collect(t *testing.T, a Adapter) []Event {
	t.Helper()
	ch, err := a.Stream(context.Background())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestProcessAdapterTranslatesFrames(t *testing.T) {
	script := `printf '%s\n' '{"type":"text","text":"hello"}' '{"type":"reasoning","text":"hmm"}' '{"usage":{"input_tokens":10,"output_tokens":5,"cost_usd":0.02}}'`
	a := NewProcessAdapter(scriptDriver(script))
	if err := a.Start(context.Background(), SessionSpec{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	events := collect(t, a)

	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(events), events)
	}
	if events[0].Kind != contracts.LogText || events[0].Text != "hello" {
		t.Fatalf("event 0 = %+v", events[0])
	}
	if events[1].Kind != contracts.LogReasoning {
		t.Fatalf("event 1 = %+v", events[1])
	}
	if events[2].Kind != contracts.LogUsage || events[2].Usage == nil {
		t.Fatalf("event 2 = %+v", events[2])
	}
	if events[2].Usage.InputTokens != 10 || events[2].Usage.CostUSD != 0.02 {
		t.Fatalf("usage = %+v", events[2].Usage)
	}
}

func TestProcessAdapterNonJSONFallsBackToText(t *testing.T) {
	a := NewProcessAdapter(scriptDriver(`printf 'plain output\n'`))
	if err := a.Start(context.Background(), SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	events := collect(t, a)
	if len(events) != 1 || events[0].Text != "plain output" || events[0].Kind != contracts.LogText {
		t.Fatalf("events = %+v", events)
	}
}

func TestProcessAdapterSendToStdin(t *testing.T) {
	script := `IFS= read -r line; printf '{"type":"text","text":"got:%s"}\n' "$line"`
	a := NewProcessAdapter(scriptDriver(script))
	ctx := context.Background()
	if err := a.Start(ctx, SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(ctx, "ping"); err != nil {
		t.Fatalf("send: %v", err)
	}
	events := collect(t, a)
	if len(events) != 1 || events[0].Text != "got:ping" {
		t.Fatalf("events = %+v", events)
	}
}

func TestProcessAdapterStopTerminatesProcess(t *testing.T) {
	a := NewProcessAdapter(scriptDriver(`sleep 30`))
	ctx := context.Background()
	if err := a.Start(ctx, SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	done := make(chan struct{})
	go func() {
		ch, _ := a.Stream(ctx)
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not terminate after stop")
	}
}

func TestProcessAdapterStreamBeforeStartFails(t *testing.T) {
	a := NewProcessAdapter(scriptDriver(""))
	if _, err := a.Stream(context.Background()); err == nil {
		t.Fatal("want error streaming before start")
	}
}

func TestRegistryUnknownHarness(t *testing.T) {
	r := NewRegistry()
	if _, err := r.NewAdapter(SessionSpec{Harness: "nope"}); err == nil {
		t.Fatal("want error for unknown harness")
	}
}

func TestRegistryRegisterAndList(t *testing.T) {
	r := NewRegistry(scriptDriver(""))
	got := r.Harnesses()
	if len(got) != 1 || got[0] != contracts.HarnessOpenCode {
		t.Fatalf("harnesses = %v", got)
	}
	if _, ok := r.Get(contracts.HarnessOpenCode); !ok {
		t.Fatal("driver not found")
	}
}

func TestBuiltinDriversCoverEveryHarness(t *testing.T) {
	r := BuiltinRegistry()
	for _, h := range contracts.AllHarnesses() {
		if _, ok := r.Get(h); !ok {
			t.Errorf("no builtin driver for harness %q", h)
		}
	}
}

func TestUsageTranslatorAccumulatesAcrossFieldNames(t *testing.T) {
	tr := JSONLineTranslator{}
	evs := tr.Translate(`{"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	if len(evs) != 1 || evs[0].Usage == nil || evs[0].Usage.InputTokens != 7 || evs[0].Usage.OutputTokens != 3 {
		t.Fatalf("events = %+v", evs)
	}
}
