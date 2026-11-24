package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/steering"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// newPersistRunner is a prompt-harness runner whose notebooks persist under
// notebookDir. Two runners built over one directory model a daemon restart.
func newPersistRunner(t *testing.T, notebookDir, script string) (*Runner, *domain.Engine, context.Context) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.SetClock(fixedClock)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, "sh", spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	inherited := contextstore.NewInheritedContext(t.TempDir())
	if err := inherited.Ensure(); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Deps{
		Engine:    eng,
		Registry:  providers.NewRegistry(driver),
		Roles:     contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs:      logstore.New(t.TempDir()),
		Ledger:    usage.NewLedger(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: inherited,
		Settings:  settings.New(t.TempDir()),
		Tools:     toolbox.New(),
		Steering:  steering.New(),
		Notebooks: contextstore.NewNotebookStore(notebookDir),
		Clock:     fixedClock,
		Cfg:       Config{MaxPasses: 2, MaxConcurrency: 2},
	})
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	return r, eng, ctx
}

// A subagent's result written before a restart reaches the lead's prompt after
// it: the lead's Shared section is rendered from the reloaded notebook.
func TestSubagentResultSurvivesRestartIntoLeadPrompt(t *testing.T) {
	dir := t.TempDir()

	before, _, ctx := newPersistRunner(t, dir, `printf '{"type":"text","text":"finding-xyz"}\n'`)
	tasks := []contracts.DispatchTask{{
		AgentID: "agent-worker", ParentAgentID: "agent-orchestrator",
		Name: "worker", Role: contracts.RoleExecutor, Task: "find it",
	}}
	if err := before.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	if err := before.Run(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}

	// The restarted daemon's lead pass must see result_worker in Shared context.
	after, _, ctx := newPersistRunner(t, dir, `case "$1" in
  *"## Shared context"*"### result_worker"*"finding-xyz"*) ;;
  *) printf '{"type":"error","text":"lead prompt lacked the persisted result"}\n'; exit 1 ;;
esac
printf '{"type":"text","text":"lead saw the worker result"}\n'`)
	if err := New(after).RunTurn(ctx, "t1", "what did the worker find?"); err != nil {
		for _, e := range after.Logs().Entries("t1") {
			t.Logf("orchestrator %s: %s", e.Kind, e.Text)
		}
		t.Fatalf("run turn after restart: %v", err)
	}
	state, _ := after.engine.State(ctx, "t1")
	found := false
	for _, m := range state.Messages {
		if m.Author == "orchestrator" && m.Text == "lead saw the worker result" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lead did not run against the persisted result: %+v", state.Messages)
	}
}

// Runner.Notebook and notebookFor hand out the same persisted object.
func TestRunnerNotebookIsTheCachedPersistedObject(t *testing.T) {
	r, _, _ := newPersistRunner(t, t.TempDir(), `true`)
	if r.Notebook("t1") != r.notebookFor("t1") {
		t.Fatal("Notebook and notebookFor returned different objects")
	}
}

// Deleting a thread drops its notebook: a recreated id starts empty.
func TestDropNotebookForgetsTheThreadsNotes(t *testing.T) {
	dir := t.TempDir()
	r, _, _ := newPersistRunner(t, dir, `true`)
	r.notebookFor("t1").Append("notes", "old note", "orchestrator")

	r.DropNotebook("t1")

	if got := r.notebookFor("t1").Get("notes"); len(got) != 0 {
		t.Fatalf("notes after drop = %+v", got)
	}
	again, _, _ := newPersistRunner(t, dir, `true`)
	if got := again.notebookFor("t1").Get("notes"); len(got) != 0 {
		t.Fatalf("restarted runner resurrected dropped notes: %+v", got)
	}
}

// Concurrent first callers for one thread share one slot, so they all get the
// same object even while the notebook is loading.
func TestConcurrentFirstCallersShareOneNotebook(t *testing.T) {
	r, _, _ := newPersistRunner(t, t.TempDir(), `true`)
	const callers = 64
	got := make([]*contextstore.Notebook, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got[i] = r.notebookFor("t9")
		}()
	}
	close(start)
	wg.Wait()
	for i, nb := range got {
		if nb != got[0] {
			t.Fatalf("caller %d got a different notebook object", i)
		}
	}
}

// A stale holder of a dropped notebook (a turn that grabbed the pointer before
// the delete) must not recreate the thread's file after DropNotebook.
func TestStaleNotebookHolderCannotRecreateDroppedFile(t *testing.T) {
	dir := t.TempDir()
	r, _, _ := newPersistRunner(t, dir, `true`)
	stale := r.notebookFor("t1")
	stale.Append("notes", "before", "orchestrator")

	r.DropNotebook("t1")
	stale.Append("notes", "after drop", "orchestrator")

	if _, err := os.Stat(filepath.Join(dir, "t1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("stale holder recreated the dropped notebook's file: %v", err)
	}
}

// A failed notes write reaches the transcript once per notebook, however many
// writes fail, and the writes still land in memory.
func TestNotebookWriteFailureSurfacesOneNoticeAndTurnKeepsRunning(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notebooks")
	r, eng, ctx := newPersistRunner(t, dir, `true`)
	nb := r.notebookFor("t1")

	// The store directory becomes a regular file, so every write fails.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		nb.Append("result_w", fmt.Sprintf("note %d", i), "agent-w")
	}

	if got := nb.Get("result_w"); len(got) != 3 {
		t.Fatalf("memory lost notes after failed writes: %+v", got)
	}
	state, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, m := range state.Messages {
		if m.Author == contracts.AuthorStatus {
			notices++
			if !strings.Contains(m.Text, "could not be saved to disk") {
				t.Fatalf("notice text = %q", m.Text)
			}
		}
	}
	if notices != 1 {
		t.Fatalf("status notices = %d, want exactly 1 for three failed writes", notices)
	}
	if state.Turn != 0 {
		t.Fatalf("notice opened a turn: Turn = %d", state.Turn)
	}
}
