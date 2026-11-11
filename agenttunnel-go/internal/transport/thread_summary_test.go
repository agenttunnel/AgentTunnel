package transport

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

func TestThreadListShipsFirstUserPromptTruncated(t *testing.T) {
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	dispatch := func(cmd contracts.Command) {
		t.Helper()
		if err := eng.Dispatch(ctx, cmd); err != nil {
			t.Fatalf("dispatch %s: %v", cmd.Type, err)
		}
	}

	// Multi-byte runes prove the truncation counts characters, not bytes.
	long := strings.Repeat("é", maxSummaryPromptRunes+50)
	dispatch(contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "First"})
	dispatch(contracts.Command{ID: "m1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: long})
	dispatch(contracts.Command{ID: "m2", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "second prompt"})
	dispatch(contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2", Title: "Empty"})

	disp := NewDispatcher(DispatcherDeps{Engine: eng})
	res, err := disp.Handle(ctx, Request{ID: "1", Method: "thread.list"})
	if err != nil {
		t.Fatalf("thread.list: %v", err)
	}
	list, ok := res.([]ThreadSummary)
	if !ok {
		t.Fatalf("thread.list result = %T, want []ThreadSummary", res)
	}
	byID := make(map[contracts.ThreadID]ThreadSummary, len(list))
	for _, s := range list {
		byID[s.ID] = s
	}

	got := byID["t1"].FirstPrompt
	if want := strings.Repeat("é", maxSummaryPromptRunes); got != want {
		t.Fatalf("firstPrompt = %d runes, want the first %d", utf8.RuneCountInString(got), maxSummaryPromptRunes)
	}
	if got == long {
		t.Fatal("firstPrompt must be truncated, not shipped whole")
	}
	if !utf8.ValidString(got) {
		t.Fatal("firstPrompt must not split a rune")
	}
	if byID["t2"].FirstPrompt != "" {
		t.Fatalf("a thread with no user message has no firstPrompt, got %q", byID["t2"].FirstPrompt)
	}
}
