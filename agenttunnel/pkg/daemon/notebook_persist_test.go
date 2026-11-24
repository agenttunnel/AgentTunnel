package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// bootDaemonAt starts a daemon on a caller-owned state dir, so a test can close
// it and boot a second one over the same files. The caller closes it.
func bootDaemonAt(t *testing.T, stateDir string) *Daemon {
	t.Helper()
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", e2eScript}, PromptVia: "stdin"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: stateDir,
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		Config:   orchestrator.Config{MaxPasses: 2, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	return d
}

func TestNotebookSurvivesDaemonRestart(t *testing.T) {
	stateDir := t.TempDir()

	first := bootDaemonAt(t, stateDir)
	nb := first.Runner.Notebook("t-restart")
	nb.Append("result_worker", "found the leak", "agent-worker")
	nb.Set(orchestrator.ContextKey, "curated summary", "curator")
	first.Close()

	second := bootDaemonAt(t, stateDir)
	t.Cleanup(second.Close)
	got := second.Runner.Notebook("t-restart")
	if r := got.Get("result_worker"); len(r) != 1 || r[0].Value != "found the leak" || r[0].Author != "agent-worker" {
		t.Fatalf("result_worker after restart = %+v", r)
	}
	if c := got.Get(orchestrator.ContextKey); len(c) != 1 || c[0].Value != "curated summary" {
		t.Fatalf("curated context after restart = %+v", c)
	}
}

func TestThreadDeleteRemovesNotebookFileAndRecreatedIDStartsEmpty(t *testing.T) {
	stateDir := t.TempDir()
	d := bootDaemonAt(t, stateDir)
	c, _ := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "1", "thread.create", map[string]any{"title": "doomed"}), &created)
	id := string(created.ThreadID)

	d.Runner.Notebook(created.ThreadID).Append("notes", "old finding", "orchestrator")
	path := filepath.Join(stateDir, "notebooks", id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("notebook file not written before delete: %v", err)
	}

	call(t, c, "2", "thread.delete", map[string]any{"threadId": created.ThreadID})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("notebook file survived thread delete: %v", err)
	}
	if got := d.Runner.Notebook(created.ThreadID).Get("notes"); len(got) != 0 {
		t.Fatalf("notes survived thread delete in memory: %+v", got)
	}

	// A new thread under the same id, after a restart, starts with no notes.
	d.Close()
	reborn := bootDaemonAt(t, stateDir)
	t.Cleanup(reborn.Close)
	reborn.Runner.Notebook(created.ThreadID).Append("notes", "fresh", "orchestrator")
	got := reborn.Runner.Notebook(created.ThreadID).Get("notes")
	if len(got) != 1 || got[0].Value != "fresh" {
		t.Fatalf("recreated id resurrected old notes: %+v", got)
	}
}
