package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// contextScript answers the curator with summary and every other role with a
// finding. Each curator run appends a line to runs, so tests count the runs.
// When gate is set, a curator run spins until that file exists.
func contextScript(runs, gate, summary string) string {
	wait := ""
	if gate != "" {
		wait = fmt.Sprintf("while [ ! -f %q ]; do :; done\n", gate)
	}
	return fmt.Sprintf(`case "$AT_ROLE" in
context-manager) echo "$AT_THREAD_ID" >> %q
%s printf '{"type":"text","text":"%%s"}\n' "%s" ;;
*) printf '{"type":"text","text":"finding from $AT_TASK"}\n' ;;
esac`, runs, wait, summary)
}

func countRuns(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

func runExecutor(t *testing.T, r *Runner, ctx context.Context, threadID contracts.ThreadID, agent contracts.AgentID, name string) {
	t.Helper()
	tasks := []contracts.DispatchTask{{AgentID: agent, Name: name, Role: contracts.RoleExecutor, Task: name}}
	if err := r.Dispatch(ctx, threadID, tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, threadID, tasks); err != nil {
		t.Fatal(err)
	}
}

func TestContextManagerCuratesIntoContextKey(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "runs")
	r, eng, ctx := newHarness(t, contextScript(runs, "", "curated summary"))
	r.settings.SetExperimentalRoles(true)

	runExecutor(t, r, ctx, "t1", "agent-a", "a")
	r.curator.wait("t1")

	nb := r.notebookFor("t1")
	ctxEntries := nb.Get(ContextKey)
	if len(ctxEntries) != 1 || ctxEntries[0].Value != "curated summary" {
		t.Fatalf("context entries = %+v", ctxEntries)
	}
	for _, k := range nb.Keys() {
		if strings.HasPrefix(k, "result_context") {
			t.Fatalf("curator wrote a result key: %q", k)
		}
	}
	state, _ := eng.State(ctx, "t1")
	if state.Shared[ContextKey] != "curated summary" {
		t.Fatalf("shared context = %q", state.Shared[ContextKey])
	}
	if n := countRuns(t, runs); n != 1 {
		t.Fatalf("curator runs = %d, want 1", n)
	}
}

func TestContextManagerTopicDispatchWritesResultKey(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "runs")
	r, _, ctx := newHarness(t, contextScript(runs, "", "curated summary"))
	r.settings.SetExperimentalRoles(true)

	tasks := []contracts.DispatchTask{{AgentID: "agent-topic", Name: "topic", Role: contracts.RoleContextManager, Task: "what changed?"}}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	r.curator.wait("t1")

	nb := r.notebookFor("t1")
	if got := nb.Get("result_topic"); len(got) != 1 || got[0].Value != "curated summary" {
		t.Fatalf("result_topic = %+v", got)
	}
	if got := nb.Get(ContextKey); len(got) != 0 {
		t.Fatalf("a topic dispatch must not write the context key: %+v", got)
	}
	if n := countRuns(t, runs); n != 1 {
		t.Fatalf("curator runs = %d, want only the dispatched run", n)
	}
}

func TestContextManagerRunsCoalesceWhileRunning(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	gate := filepath.Join(dir, "gate")
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o600) })
	r, _, ctx := newHarness(t, contextScript(runs, gate, "curated summary"))
	r.settings.SetExperimentalRoles(true)

	// The first curator run cannot finish until the gate opens, so every
	// success below lands while it is in flight and must coalesce into one rerun.
	runExecutor(t, r, ctx, "t1", "agent-a", "a")
	runExecutor(t, r, ctx, "t1", "agent-b", "b")
	runExecutor(t, r, ctx, "t1", "agent-c", "c")
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.curator.wait("t1")

	if n := countRuns(t, runs); n != 2 {
		t.Fatalf("curator runs = %d, want 2 (one run plus one coalesced rerun)", n)
	}
}

func TestContextManagerStateIsPerThread(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	gate := filepath.Join(dir, "gate")
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o600) })
	r, eng, ctx := newHarness(t, contextScript(runs, gate, "summary for $AT_THREAD_ID"))
	r.settings.SetExperimentalRoles(true)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}

	// Both threads are curating at once. A thread must not coalesce into
	// another thread's run, or one of them would never get a summary.
	runExecutor(t, r, ctx, "t1", "agent-a1", "a")
	runExecutor(t, r, ctx, "t2", "agent-a2", "a")
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.curator.wait("t1")
	r.curator.wait("t2")

	for _, tid := range []contracts.ThreadID{"t1", "t2"} {
		got := r.notebookFor(tid).Get(ContextKey)
		want := "summary for " + string(tid)
		if len(got) != 1 || got[0].Value != want {
			t.Fatalf("%s context = %+v, want %q", tid, got, want)
		}
	}
	if n := countRuns(t, runs); n != 2 {
		t.Fatalf("curator runs = %d, want one per thread", n)
	}
}

func TestContextManagerNeverTriggersOnItself(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "runs")
	r, _, ctx := newHarness(t, contextScript(runs, "", "curated summary"))
	r.settings.SetExperimentalRoles(true)

	runExecutor(t, r, ctx, "t1", "agent-a", "a")
	r.curator.wait("t1")

	// The curator's own success must not queue another curation.
	if n := countRuns(t, runs); n != 1 {
		t.Fatalf("curator runs = %d, want 1", n)
	}
}

func TestContextManagerOffWhenExperimentalDisabled(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "runs")
	r, _, ctx := newHarness(t, contextScript(runs, "", "curated summary"))
	if r.settings.ExperimentalRoles() {
		t.Fatal("experimental roles must default off")
	}

	runExecutor(t, r, ctx, "t1", "agent-a", "a")
	r.curator.wait("t1")

	if n := countRuns(t, runs); n != 0 {
		t.Fatalf("curator ran %d times with experimental roles off", n)
	}
	if got := r.notebookFor("t1").Get(ContextKey); len(got) != 0 {
		t.Fatalf("context written with experimental roles off: %+v", got)
	}
}

func TestCuratorInputClipsToRecentEntries(t *testing.T) {
	r, _, _ := newHarness(t, "true")
	nb := r.notebookFor("t1")
	nb.Set(ContextKey, "previous", "old")
	for i := 0; i < contextCuratorInputEntries+5; i++ {
		nb.Append(fmt.Sprintf("result_%d", i), fmt.Sprintf("finding-%d", i), "agent")
	}

	input := curatorInput(nb)
	if !strings.Contains(input, "previous") {
		t.Fatalf("previous summary missing: %s", input)
	}
	if strings.Contains(input, "finding-4\n") || strings.Contains(input, "finding-4:") {
		t.Fatalf("entry beyond the clip survived: %s", input)
	}
	if !strings.Contains(input, fmt.Sprintf("finding-%d", contextCuratorInputEntries+4)) {
		t.Fatalf("newest entry missing: %s", input)
	}
	if n := strings.Count(input, "- [agent]"); n != contextCuratorInputEntries {
		t.Fatalf("clipped entries = %d, want %d", n, contextCuratorInputEntries)
	}
}
