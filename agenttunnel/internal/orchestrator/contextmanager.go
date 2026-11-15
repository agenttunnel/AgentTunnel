package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ContextKey is the notebook key the context manager curates into. Only the
// auto-run writes it; a context-manager dispatched by a lead writes result_*.
const ContextKey = "context"

// contextCuratorInputEntries bounds what the curator reads: the last N notebook
// entries (excluding the previous summary, which is always included).
const contextCuratorInputEntries = 20

const contextCuratorTask = "Curate the shared context above into one concise, current summary of what is " +
	"known, decided, and still open. Reply with only the summary."

// contextCurator is per-thread single-flight state for the auto context manager.
// Keyed by ThreadID only: agent ids are not thread-scoped (the lead is always
// agent-orchestrator). The zero value is ready to use.
type contextCurator struct {
	mu      sync.Mutex
	threads map[contracts.ThreadID]*curation
}

// curation is one thread's run state. dirty means a subagent succeeded while a
// run was in flight, so exactly one rerun follows it. idle closes when the
// thread's run loop exits.
type curation struct {
	dirty bool
	idle  chan struct{}
}

// markDirty records that the thread's notebook changed. It reports whether the
// caller must start the run loop; false means a loop is already in flight and
// will pick the change up.
func (c *contextCurator) markDirty(threadID contracts.ThreadID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.threads == nil {
		c.threads = map[contracts.ThreadID]*curation{}
	}
	if st, ok := c.threads[threadID]; ok {
		st.dirty = true
		return false
	}
	c.threads[threadID] = &curation{idle: make(chan struct{})}
	return true
}

// nextRun reports whether the loop reruns, consuming the dirty flag. When it
// does not, the thread's state is dropped and waiters are released.
func (c *contextCurator) nextRun(threadID contracts.ThreadID, rerun bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.threads[threadID]
	if rerun && st.dirty {
		st.dirty = false
		return true
	}
	delete(c.threads, threadID)
	close(st.idle)
	return false
}

// wait blocks until the thread has no curation in flight.
func (c *contextCurator) wait(threadID contracts.ThreadID) {
	c.mu.Lock()
	st, ok := c.threads[threadID]
	c.mu.Unlock()
	if ok {
		<-st.idle
	}
}

// contextCurationOn reports whether the auto context manager may run: the
// experimental flag is on and the role is usable.
func (r *Runner) contextCurationOn() bool {
	return r.settings != nil && r.settings.ExperimentalRoles() && r.roleUsable(contracts.RoleContextManager)
}

// requestCuration is called after a subagent succeeds. It starts the thread's
// curation loop, or leaves the in-flight loop to rerun once.
func (r *Runner) requestCuration(ctx context.Context, threadID contracts.ThreadID) {
	if !r.contextCurationOn() {
		return
	}
	if r.curator.markDirty(threadID) {
		go r.runCurationLoop(ctx, threadID)
	}
}

// runCurationLoop runs the curator for a thread until no change is pending. A
// cancelled context ends the loop without a rerun.
func (r *Runner) runCurationLoop(ctx context.Context, threadID contracts.ThreadID) {
	for {
		r.curateOnce(ctx, threadID)
		if !r.curator.nextRun(threadID, ctx.Err() == nil) {
			return
		}
	}
}

// curateOnce runs the context manager once as a fresh agent and writes its
// summary to the context key.
func (r *Runner) curateOnce(ctx context.Context, threadID contracts.ThreadID) {
	task := contracts.DispatchTask{
		AgentID: contracts.AgentID(fmt.Sprintf("agent-context-%d", r.seq.Add(1))),
		Name:    ContextKey,
		Role:    contracts.RoleContextManager,
		Task:    contextCuratorTask,
	}
	if err := r.Dispatch(ctx, threadID, []contracts.DispatchTask{task}); err != nil {
		return
	}
	_, err := r.runTask(ctx, threadID, task, true)
	switch {
	case errors.Is(err, context.Canceled):
		r.setStatus(ctx, threadID, task.AgentID, contracts.AgentCancelled, "stopped")
	case err != nil:
		r.setStatus(ctx, threadID, task.AgentID, contracts.AgentFailed, err.Error())
	default:
		r.setStatus(ctx, threadID, task.AgentID, contracts.AgentSucceeded, "")
	}
}

// curatorInput renders the notebook for the curator: the previous summary, then
// the last contextCuratorInputEntries entries from every other key in notebook
// order.
func curatorInput(nb *contextstore.Notebook) string {
	type item struct {
		key   string
		entry contextstore.Entry
	}
	var items []item
	for _, k := range nb.Keys() {
		if k == ContextKey {
			continue
		}
		for _, e := range nb.Get(k) {
			items = append(items, item{k, e})
		}
	}
	if len(items) > contextCuratorInputEntries {
		items = items[len(items)-contextCuratorInputEntries:]
	}

	var b strings.Builder
	b.WriteString("## Shared context\n")
	if prev := nb.Get(ContextKey); len(prev) > 0 {
		b.WriteString("\n### previous summary\n")
		for _, e := range prev {
			fmt.Fprintf(&b, "- [%s] %s\n", e.Author, e.Value)
		}
	}
	b.WriteString("\n### recent entries\n")
	for _, it := range items {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", it.entry.Author, it.key, it.entry.Value)
	}
	return b.String()
}
