package providers

import (
	"context"
	"os/exec"
	"strings"
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

// TestLiveClaudeAdapterReportsUsage proves the stream-json invocation yields
// both assistant text and a usable usage event (tokens plus cost).
func TestLiveClaudeAdapterReportsUsage(t *testing.T) {
	requireLive(t)
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	driver, ok := BuiltinRegistry().Get(contracts.HarnessClaude)
	if !ok {
		t.Fatal("no claude driver")
	}

	a := NewProcessAdapter(driver)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	spec := SessionSpec{
		AgentID: "a1",
		Harness: contracts.HarnessClaude,
		Prompt:  "Reply with exactly: ok",
	}
	if err := a.Start(ctx, spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	events, err := a.Stream(ctx)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	var usage *contracts.TokenUsage
	sawText := false
	for ev := range events {
		if ev.Kind == contracts.LogUsage && ev.Usage != nil {
			usage = ev.Usage
		}
		if ev.Kind == contracts.LogText && strings.Contains(ev.Text, "ok") {
			sawText = true
		}
	}
	if werr := a.Wait(ctx); werr != nil {
		t.Errorf("adapter exited with error: %v", werr)
	}
	if !sawText {
		t.Error("no assistant text event")
	}
	if usage == nil {
		t.Fatal("no usage event")
	}
	if usage.InputTokens == 0 || usage.OutputTokens == 0 || usage.CostUSD == 0 {
		t.Fatalf("usage not populated: %+v", usage)
	}
	t.Logf("usage: %+v", usage)
}
