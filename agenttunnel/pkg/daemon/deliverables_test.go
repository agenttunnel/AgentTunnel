package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// A solo turn's final answer lands in the state dir as a markdown deliverable
// whose contents are exactly the answer the thread recorded.
func TestSoloTurnWritesFinalAnswerAsMarkdownDeliverable(t *testing.T) {
	stateDir := t.TempDir()
	const script = `printf '{"type":"text","text":"the final answer"}\n'`
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: stateDir,
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		Config:   orchestrator.Config{MaxPasses: 1, MaxConcurrency: 1},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	t.Cleanup(d.Close)
	c, _ := dialWS(t, d)
	watch := watchEngine(d)
	t.Cleanup(watch.close)

	id := createThread(t, c, "deliverable")
	call(t, c, "1", "thread.setSolo", map[string]any{"threadId": id, "harness": "opencode", "model": "m"})
	call(t, c, "2", "thread.turn.start", map[string]any{"threadId": id, "text": "answer me"})
	watch.wait(t, "solo answer recorded", func(ev contracts.Event) bool {
		p, ok := ev.Payload.(contracts.MessageSent)
		return ok && ev.ThreadID == id && p.Author == "solo"
	})

	dir := filepath.Join(stateDir, "deliverables", string(id))
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("deliverables in %s: %v (err %v), want exactly one file", dir, entries, err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if string(got) != "the final answer\n" {
		t.Fatalf("deliverable = %q, want only the final answer", got)
	}
}
