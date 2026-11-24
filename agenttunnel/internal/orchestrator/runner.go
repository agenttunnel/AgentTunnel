package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/actionlog"
	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/gitguard"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/steering"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
	"github.com/agenttunnel/agenttunnel-go/internal/worktree"
)

// Config controls how a runner schedules and executes work.
type Config struct {
	MaxPasses      int
	MaxConcurrency int
}

// LaunchThreadTurnFunc launches a turn on a thread from inside another
// thread's turn — the relaunch path. It claims the target's turn slot
// synchronously, so a busy target is refused before anything is recorded;
// runs record while the claim is held, so the control claim and the turn's
// opener land in the caller's own drain and the agent that asked learns the
// outcome in the same pass; then runs the turn in the background on the
// claimed context and hands the ended turn, whatever its outcome, to finish.
// The transport layer installs the real one — turn slots, first-worktree
// cuts, and per-turn status reports live in the dispatcher. A Runner with
// none (an orchestrator-level harness) records directly and runs the turn
// itself, detached from the caller's turn context.
type LaunchThreadTurnFunc func(ctx context.Context, target contracts.ThreadID, text string, record func() error, finish func(turnCtx context.Context)) error

// Deps are the collaborators a runner needs.
type Deps struct {
	Engine    *domain.Engine
	Registry  *providers.Registry
	Roles     *contracts.RoleRegistry
	Logs      *logstore.Store
	Ledger    *usage.Ledger
	Mailbox   *contextstore.Mailbox
	Inherited *contextstore.InheritedContext
	Settings  *settings.Store
	Models    *models.Service
	Tools     *toolbox.Box
	Steering  *steering.Box
	Audit     actionlog.Func
	ToolPath  string
	Token     string
	Endpoint  func() string
	Clock     domain.Clock
	Cfg       Config
	// Notebooks persists each thread's shared notebook. Nil keeps notebooks
	// in memory only.
	Notebooks *contextstore.NotebookStore
	// LaunchThreadTurn runs turns the Runner starts on other threads. Nil
	// falls back to the Runner's own orchestrator.
	LaunchThreadTurn LaunchThreadTurnFunc
}

// Runner executes dispatched tasks with dependency-aware, bounded concurrency,
// and records status, usage, logs, and findings as it goes.
type Runner struct {
	engine    *domain.Engine
	registry  *providers.Registry
	roles     *contracts.RoleRegistry
	logs      *logstore.Store
	ledger    *usage.Ledger
	mailbox   *contextstore.Mailbox
	inherited *contextstore.InheritedContext
	settings  *settings.Store
	models    *models.Service
	tools     *toolbox.Box
	steering  *steering.Box
	audit     actionlog.Func
	toolPath  string
	token     string
	endpoint  func() string
	clock     domain.Clock
	cfg       Config
	seq       atomic.Int64

	// cancels tracks the live cancel func of every running agent so a single
	// agent can be stopped from the UI without touching its siblings.
	cancelMu sync.Mutex
	cancels  map[contracts.AgentID]*trackedCancel

	spawns *spawnStore

	// steers holds the team lead's steers to subagents, read when each
	// subagent's next run prompt is built.
	steers *steerQueue

	// notebooks holds each thread's shared notebook. A thread's context is its
	// own: a subthread running beside its parent must not read or write the
	// parent's notes.
	notebookMu    sync.Mutex
	notebooks     map[contracts.ThreadID]*notebookSlot
	notebookStore *contextstore.NotebookStore

	// btwSessions holds each thread's /btw side-conversation memory. The
	// runner is shared daemon-wide, so a session is keyed by thread ID, never
	// by agent: agent ids are thread-agnostic and would collide across
	// threads.
	btwMu       sync.Mutex
	btwSessions map[contracts.ThreadID]*btwSession

	// curator single-flights the auto context manager per thread.
	curator contextCurator

	// launchTurn runs turns this Runner starts on other threads (see
	// LaunchThreadTurnFunc); nil uses background below.
	launchTurn LaunchThreadTurnFunc

	// background is the orchestrator that runs turns the Runner itself
	// starts — a relaunch with no transport launcher. One instance, kept for
	// the Runner's lifetime, keeps its dispatch sequence growing like any
	// other orchestrator.
	background *Orchestrator
}

// NewRunner wires a runner from its dependencies.
func NewRunner(d Deps) *Runner {
	if d.Clock == nil {
		d.Clock = time.Now
	}
	r := &Runner{
		engine:        d.Engine,
		registry:      d.Registry,
		roles:         d.Roles,
		logs:          d.Logs,
		ledger:        d.Ledger,
		mailbox:       d.Mailbox,
		inherited:     d.Inherited,
		settings:      d.Settings,
		models:        d.Models,
		tools:         d.Tools,
		steering:      d.Steering,
		audit:         d.Audit,
		toolPath:      d.ToolPath,
		token:         d.Token,
		endpoint:      d.Endpoint,
		clock:         d.Clock,
		cfg:           d.Cfg,
		spawns:        newSpawnStore(),
		steers:        newSteerQueue(),
		notebooks:     map[contracts.ThreadID]*notebookSlot{},
		notebookStore: d.Notebooks,
		btwSessions:   map[contracts.ThreadID]*btwSession{},
		launchTurn:    d.LaunchThreadTurn,
	}
	r.background = New(r)
	return r
}

// Ledger exposes the environment usage ledger.
func (r *Runner) Ledger() *usage.Ledger { return r.ledger }

// Inherited exposes the environment's inherited context store.
func (r *Runner) Inherited() *contextstore.InheritedContext { return r.inherited }

// Notebook exposes a thread's shared notebook.
func (r *Runner) Notebook(threadID contracts.ThreadID) *contextstore.Notebook {
	return r.notebookFor(threadID)
}

// notebookSlot is one thread's notebook. Its mutex serialises that thread's
// open and drop; notebookMu only guards the map, so disk IO never blocks
// another thread.
type notebookSlot struct {
	mu      sync.Mutex
	nb      *contextstore.Notebook
	dropped bool
}

// notebookFor returns the thread's notebook, loading it on first use. Concurrent
// first callers for one thread share one slot and therefore one object.
func (r *Runner) notebookFor(threadID contracts.ThreadID) *contextstore.Notebook {
	for {
		slot := r.slotFor(threadID)
		slot.mu.Lock()
		if slot.dropped {
			// Dropped after the lookup. DropNotebook has already removed the slot
			// from the map, so the next lookup creates a fresh one.
			slot.mu.Unlock()
			continue
		}
		if slot.nb == nil {
			slot.nb = r.openNotebook(threadID)
		}
		nb := slot.nb
		slot.mu.Unlock()
		return nb
	}
}

// slotFor returns the thread's slot, inserting an empty one if needed. It holds
// notebookMu only for the map access.
func (r *Runner) slotFor(threadID contracts.ThreadID) *notebookSlot {
	r.notebookMu.Lock()
	defer r.notebookMu.Unlock()
	slot, ok := r.notebooks[threadID]
	if !ok {
		slot = &notebookSlot{}
		r.notebooks[threadID] = slot
	}
	return slot
}

// openNotebook loads a thread's persisted notebook, or makes a memory-only one
// when the Runner has no store. The first write failure of each loaded notebook
// is also posted to the thread's transcript, so the user sees it. The turn
// keeps running either way.
func (r *Runner) openNotebook(threadID contracts.ThreadID) *contextstore.Notebook {
	if r.notebookStore == nil {
		return contextstore.NewNotebook()
	}
	var notice sync.Once
	return r.notebookStore.Open(string(threadID), func(err error) {
		r.auditLog("notebook.error", map[string]any{"thread": string(threadID), "error": err.Error()})
		notice.Do(func() { r.postNotebookNotice(threadID, err) })
	})
}

// postNotebookNotice records a transcript status line for a notes failure. It is
// authored by AuthorStatus, never "user", so it does not open a turn. It is best
// effort: a failed dispatch is ignored.
func (r *Runner) postNotebookNotice(threadID contracts.ThreadID, err error) {
	_ = r.engine.Dispatch(context.Background(), contracts.Command{
		ID:       r.nextID(),
		Type:     contracts.CmdMessageAppend,
		ThreadID: threadID,
		Message: &contracts.MessageSent{
			MessageID: r.nextID(),
			Author:    contracts.AuthorStatus,
			Text:      "Shared notes for this thread could not be saved to disk, so they will not survive a restart: " + err.Error(),
		},
	})
}

// DropNotebook forgets a deleted thread's notebook, both the cached object and
// its file. A later thread with the same id starts with an empty notebook.
//
// The file is removed before the slot leaves the map, so a lookup that races
// this drop cannot reload the old notes. No other thread waits on the disk IO.
func (r *Runner) DropNotebook(threadID contracts.ThreadID) {
	slot := r.slotFor(threadID)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	slot.dropped = true
	if slot.nb != nil {
		slot.nb.Discard()
	}
	if r.notebookStore != nil {
		if err := r.notebookStore.Remove(string(threadID)); err != nil {
			r.auditLog("notebook.error", map[string]any{"thread": string(threadID), "error": err.Error()})
		}
	}
	r.notebookMu.Lock()
	if r.notebooks[threadID] == slot {
		delete(r.notebooks, threadID)
	}
	r.notebookMu.Unlock()
}

// Mailbox exposes the runtime peer mailbox.
func (r *Runner) Mailbox() *contextstore.Mailbox { return r.mailbox }

// Logs exposes the per-agent log store.
func (r *Runner) Logs() *logstore.Store { return r.logs }

// Dispatch registers each task as an agent in the engine (creating the agent
// records) before execution.
func (r *Runner) Dispatch(ctx context.Context, threadID contracts.ThreadID, tasks []contracts.DispatchTask) error {
	for _, t := range tasks {
		task := t
		if task.Harness == "" {
			task.Harness = r.dispatchHarness(ctx, threadID, task.Role)
		}
		if err := r.engine.Dispatch(ctx, contracts.Command{
			ID:       r.nextID(),
			Type:     contracts.CmdTaskDispatch,
			ThreadID: threadID,
			Dispatch: &task,
		}); err != nil {
			return err
		}
	}
	return nil
}

// dispatchHarness is the harness a task with none requested will run on, so the
// agent record names it instead of reading as unknown in harness stats. It is the
// same role runtime run uses, so the recorded harness matches what executes.
func (r *Runner) dispatchHarness(ctx context.Context, threadID contracts.ThreadID, role contracts.RoleID) contracts.Harness {
	if r.roles == nil {
		return ""
	}
	if role == "" {
		role = contracts.RoleExecutor
	}
	rl, ok := r.roles.Get(role)
	if !ok {
		return ""
	}
	h, _, _ := r.roleRuntimeFor(ctx, threadID, rl)
	return h
}

// Run executes the tasks with wave-based dependency scheduling: each wave starts
// every task whose dependencies have succeeded, runs them with bounded
// concurrency, and then proceeds. Blocked tasks (dependency cycles) are
// cancelled.
// Run executes dispatched tasks and reports error only. Collect results with
// RunCollect when the caller needs what each agent produced.
func (r *Runner) Run(ctx context.Context, threadID contracts.ThreadID, tasks []contracts.DispatchTask) error {
	_, err := r.RunCollect(ctx, threadID, tasks)
	return err
}

// RunCollect executes waved tasks with bounded concurrency and returns each
// task's final text, keyed by task name. Tasks whose agent produced no text
// are absent from the map.
func (r *Runner) RunCollect(ctx context.Context, threadID contracts.ThreadID, tasks []contracts.DispatchTask) (map[string]string, error) {
	results := map[string]string{}
	if len(tasks) == 0 {
		return results, nil
	}
	status := r.threadStatuses(ctx, threadID, tasks)
	for _, t := range tasks {
		status[t.Name] = contracts.AgentPending
	}

	var mu sync.Mutex
	for {
		// A stopped turn must not start new waves; mark the rest cancelled.
		if ctx.Err() != nil {
			for _, t := range tasks {
				if status[t.Name] == contracts.AgentPending {
					status[t.Name] = contracts.AgentCancelled
					r.setStatus(ctx, threadID, t.AgentID, contracts.AgentCancelled, "turn stopped")
				}
			}
			return results, ctx.Err()
		}
		ready := make([]contracts.DispatchTask, 0, len(tasks))
		pending := 0
		for _, t := range tasks {
			if status[t.Name] != contracts.AgentPending {
				continue
			}
			if d, ok := unknownDependency(t.DependsOn, status); ok {
				status[t.Name] = contracts.AgentCancelled
				r.setStatus(ctx, threadID, t.AgentID, contracts.AgentCancelled, "unknown dependency "+d)
				continue
			}
			pending++
			if depsSucceeded(t.DependsOn, status) {
				ready = append(ready, t)
			}
		}
		if len(ready) == 0 {
			if pending > 0 {
				for _, t := range tasks {
					if status[t.Name] == contracts.AgentPending {
						r.setStatus(ctx, threadID, t.AgentID, contracts.AgentCancelled, "unsatisfied dependencies")
					}
				}
			}
			break
		}
		for _, t := range ready {
			status[t.Name] = contracts.AgentRunning
		}

		var wg sync.WaitGroup
		sem := make(chan struct{}, r.concurrency())
		for _, t := range ready {
			wg.Add(1)
			sem <- struct{}{}
			go func(t contracts.DispatchTask) {
				defer wg.Done()
				defer func() { <-sem }()
				findings, err := r.runTask(ctx, threadID, t, false)
				mu.Lock()
				if errors.Is(err, context.Canceled) {
					status[t.Name] = contracts.AgentCancelled
					r.setStatus(ctx, threadID, t.AgentID, contracts.AgentCancelled, "stopped")
				} else if err != nil {
					status[t.Name] = contracts.AgentFailed
					r.setStatus(ctx, threadID, t.AgentID, contracts.AgentFailed, err.Error())
				} else {
					status[t.Name] = contracts.AgentSucceeded
					r.setStatus(ctx, threadID, t.AgentID, contracts.AgentSucceeded, "")
					if findings != "" {
						results[t.Name] = findings
					}
				}
				mu.Unlock()
				if err == nil && t.Role != contracts.RoleContextManager {
					r.requestCuration(ctx, threadID)
				}
			}(t)
		}
		wg.Wait()
	}
	return results, nil
}

// trackedCancel is heap-allocated per registration so identity survives the
// func-comparison restriction: an old unregister must never drop a newer one.
type trackedCancel struct{ cancel context.CancelFunc }

// trackCancel remembers an agent's cancel func so StopAgent can target it.
func (r *Runner) trackCancel(id contracts.AgentID, cancel context.CancelFunc) *trackedCancel {
	tc := &trackedCancel{cancel: cancel}
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	if r.cancels == nil {
		r.cancels = map[contracts.AgentID]*trackedCancel{}
	}
	r.cancels[id] = tc
	return tc
}

// untrackCancel forgets a registration if it is still the live one.
func (r *Runner) untrackCancel(id contracts.AgentID, tc *trackedCancel) {
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	if r.cancels[id] == tc {
		delete(r.cancels, id)
	}
}

// StopAgent cancels one running agent and reports whether it was found. The
// agent records a cancelled status; the rest of the turn continues.
func (r *Runner) StopAgent(id contracts.AgentID) bool {
	r.cancelMu.Lock()
	tc, ok := r.cancels[id]
	r.cancelMu.Unlock()
	if ok && tc != nil {
		tc.cancel()
	}
	return ok
}

// runTask executes one dispatched agent and returns its final text output.
// runTask runs one agent to completion. curate marks the auto context-manager
// run: its input is the clipped notebook and its summary replaces the context
// key instead of writing result_*.
func (r *Runner) runTask(ctx context.Context, threadID contracts.ThreadID, t contracts.DispatchTask, curate bool) (string, error) {
	// A cancellation here targets only this agent: either the turn ended or
	// the user stopped the agent specifically. Both map to a cancelled status.
	ctx, cancel := context.WithCancel(ctx)
	tc := r.trackCancel(t.AgentID, cancel)
	defer r.untrackCancel(t.AgentID, tc)

	role, ok := r.roles.Get(t.Role)
	if !ok {
		return "", fmt.Errorf("unknown role %q", t.Role)
	}
	if !r.roleUsable(t.Role) {
		return "", fmt.Errorf("role %q is disabled", t.Role)
	}
	// A wildcard agent is the lead's definition: its brief is the system
	// prompt. role is a copy of the registered value, so the registry keeps the
	// fallback prompt.
	if t.Role == contracts.RoleWildcard {
		if strings.TrimSpace(t.Brief) == "" {
			return "", fmt.Errorf("role %q needs a brief", t.Role)
		}
		role.SystemPrompt = strings.TrimSpace(t.Brief)
	}
	auto := r.autoModeFor(ctx, threadID)
	// An explicit user choice (council membership) is honored regardless of
	// auto mode; an orchestrator pick still needs auto mode to override.
	overrideRuntime := auto || t.Override
	roleHarness, _, roleVariant := r.roleRuntimeFor(ctx, threadID, role)
	// In auto mode the orchestrator picks the runtime per task: a requested
	// harness wins when it is known and enabled, mirroring how resolveModel
	// treats requested models. Otherwise the role's configured harness stands.
	// A variant belongs to the role's harness, so it never follows a switch.
	harness, variant := roleHarness, roleVariant
	if overrideRuntime && t.Harness.Valid() && t.Harness != roleHarness && r.harnessAllowed(t.Harness) {
		harness, variant = t.Harness, ""
	}
	if !r.harnessAllowed(harness) {
		return "", fmt.Errorf("harness %q is disabled", harness)
	}
	model := r.resolveModelFor(ctx, threadID, role, harness, t.Model, auto, t.Override)
	r.setStatus(ctx, threadID, t.AgentID, contracts.AgentRunning, "")

	inherited, _ := r.inherited.Render()
	peerMailbox := r.peerMailboxFor(ctx, threadID)
	reviews := r.openReviewsFor(ctx, threadID, t.Role)
	shared := r.notebookFor(threadID).Render()
	if curate {
		shared = curatorInput(r.notebookFor(threadID))
	}
	steering := r.steers.Drain(threadID, t.AgentID)
	prompt := BuildSubagentPrompt(role, t.Task, r.userRequestFor(ctx, threadID), inherited, shared, reviews, peerMailbox, r.teammateMessages(threadID, t.AgentID, peerMailbox), steering)

	env := r.toolEnv(threadID, t.AgentID)
	env = append(env, "AT_ROLE="+string(t.Role), "AT_TASK="+t.Task)
	env = append(env, subagentGitEnv(role)...)
	spec := providers.SessionSpec{
		AgentID:  t.AgentID,
		ThreadID: threadID,
		Role:     t.Role,
		Harness:  harness,
		Model:    model,
		Variant:  variant,
		Prompt:   prompt,
		Workdir:  r.workdirFor(ctx, threadID),
		Env:      env,
	}
	adapter, err := r.registry.NewAdapter(spec)
	if err != nil {
		return "", err
	}
	r.auditLog("agent.spawn", map[string]any{
		"agentId": string(t.AgentID), "role": string(t.Role), "harness": string(harness),
		"model": model, "workdir": spec.Workdir, "task": t.Task, "brief": clip(t.Brief, 500),
	})
	if err := adapter.Start(ctx, spec); err != nil {
		r.auditLog("agent.error", map[string]any{"agentId": string(t.AgentID), "error": err.Error()})
		return "", err
	}
	events, err := adapter.Stream(ctx)
	if err != nil {
		return "", err
	}

	var text replyText
	emitted := false
	for ev := range events {
		emitted = true
		r.recordEvent(ctx, threadID, t.AgentID, ev)
		if ev.Usage != nil {
			r.recordUsage(ctx, threadID, t.AgentID, t.Role, false, *ev.Usage)
		}
		if ev.Kind == contracts.LogText && strings.TrimSpace(ev.Text) != "" {
			// A protocol action frame is a tool call, not a finding: routes
			// through the inbox like a printed command line.
			if frame, ok := ParseToolLine(ev.Text); ok {
				if call, ok := frameCall(frame); ok {
					r.postAgentCall(threadID, t.AgentID, call)
					text.toolCall()
					continue
				}
			}
		}
		text.add(ev)
	}
	werr := adapter.Wait(ctx)
	r.auditLog("agent.exit", map[string]any{
		"agentId": string(t.AgentID), "status": statusWord(werr), "error": errString(werr),
	})
	// A steer that arrived during this run has no run left to read it. Say so
	// in the agent's log rather than let it vanish.
	if late := r.steers.Drain(threadID, t.AgentID); len(late) > 0 {
		msg := fmt.Sprintf("%d steer(s) from the team lead arrived during this run and were not delivered: the agent has no further run", len(late))
		_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: t.AgentID, Kind: contracts.LogError, Text: msg, At: r.clock()})
		r.publishOutput(threadID, t.AgentID, contracts.LogError, msg)
	}

	// A harness that exits cleanly but emitted nothing is a silent failure —
	// a dead API key, a swallowed quota error — and must not read as success.
	// Surface it as an error instead of an empty, green agent.
	if werr == nil && !emitted {
		msg := "harness exited without any output (check its auth or quota)"
		_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: t.AgentID, Kind: contracts.LogError, Text: msg, At: r.clock()})
		r.publishOutput(threadID, t.AgentID, contracts.LogError, msg)
		return "", errors.New(msg)
	}

	// Some harnesses print the `$AT_TOOL tool …` command instead of executing
	// it with their shell. Route the action kinds through the tool inbox,
	// attributed to this agent, so the next pass handles them like a real
	// call; everything else stays the agent's findings. Text deltas are
	// arbitrary chunks, so a printed command can straddle two events — join
	// without separators, the real newlines are already in the deltas.
	raw := text.String()
	var findings []string
	for _, line := range strings.Split(raw, "\n") {
		if call, ok := toolbox.ParseCommandLine(line); ok && routedPrintedKind(call.Kind) {
			if call.Kind.IsReview() {
				r.applyPrintedReview(ctx, threadID, t.AgentID, call)
			} else {
				r.postAgentCall(threadID, t.AgentID, call)
			}
			continue
		}
		findings = append(findings, line)
	}

	// A summary of only whitespace is no result at all; the trailing split of
	// a stream ending in a newline must not become one either.
	summary := strings.TrimSpace(strings.Join(findings, "\n"))
	if curate {
		if summary != "" {
			r.notebookFor(threadID).Set(ContextKey, summary, string(t.AgentID))
			r.setSharedContext(ctx, threadID, ContextKey, summary, t.AgentID)
		}
	} else if summary != "" || werr != nil {
		// A crashed agent's partial output must not read as a clean result.
		stored := clipResult(summary)
		if werr != nil {
			stored = "[failed: " + werr.Error() + "] " + stored
		}
		key := "result_" + t.Name
		r.notebookFor(threadID).Append(key, stored, string(t.AgentID))
		r.setSharedContext(ctx, threadID, key, stored, t.AgentID)
	}
	return summary, werr
}

// postAgentCall queues one tool call pulled out of an agent's stream — a
// printed command line or a protocol frame — attributed to that agent, so the
// next toolTasks drain handles it like a real call: a message forwards through
// the mailbox, a subagent's dispatch or spawn becomes a gated spawn request,
// and the lead's spawn or decision runs directly.
func (r *Runner) postAgentCall(threadID contracts.ThreadID, from contracts.AgentID, call toolbox.Call) {
	call.AgentID = from
	if r.tools == nil {
		return
	}
	r.tools.Post(threadID, call)
	line := "tool " + string(call.Kind)
	_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: from, Kind: contracts.LogTool, Text: line, At: r.clock()})
	r.publishOutput(threadID, from, contracts.LogTool, line)
}

// routedPrintedKind reports whether a printed tool command from a subagent is
// an action to route rather than findings. Dispatch and spawn become gated
// spawn requests in the drain, and approve/reject from a non-lead are ignored
// with an audit; message forwards through the mailbox. Review calls apply at
// once rather than queueing. Every other kind a subagent prints stays its
// findings.
func routedPrintedKind(k toolbox.Kind) bool {
	switch k {
	case toolbox.KindMessage, toolbox.KindDispatch, toolbox.KindSpawn, toolbox.KindApprove, toolbox.KindReject,
		toolbox.KindReviewComment, toolbox.KindReviewReply, toolbox.KindReviewResolve:
		return true
	default:
		return false
	}
}

// leadRuntime resolves lead i's harness, model and variant: the orchestrator
// role's runtime, with the thread's per-lead override on top. Overriding the
// harness drops the role's model, which belongs to the role's harness.
func (r *Runner) leadRuntime(ctx context.Context, threadID contracts.ThreadID, lead int, role contracts.Role) (contracts.Harness, string, string) {
	harness, model, variant := r.roleRuntimeFor(ctx, threadID, role)
	if r.engine == nil {
		return harness, model, variant
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil {
		return harness, model, variant
	}
	ov := st.LeadRuntimeFor(lead)
	if ov.Harness.Valid() && ov.Harness != harness {
		harness, model, variant = ov.Harness, "", ""
	}
	if ov.Model != "" {
		model = ov.Model
	}
	if ov.Variant != "" {
		variant = ov.Variant
	}
	return harness, model, variant
}

// runOrchestratorPass runs one orchestrator process for lead `lead` and collects
// its text output and tool calls. Usage from the orchestrator is attributed as
// orchestrator.
func (r *Runner) runOrchestratorPass(ctx context.Context, threadID contracts.ThreadID, lead int, agentID contracts.AgentID, role contracts.Role, prompt string, pass int, images []providers.ImageInput) (string, []ToolCall, error) {
	ctx, cancel := context.WithCancel(ctx)
	tc := r.trackCancel(agentID, cancel)
	defer r.untrackCancel(agentID, tc)

	harness, model, variant := r.leadRuntime(ctx, threadID, lead, role)
	if !r.harnessAllowed(harness) {
		return "", nil, fmt.Errorf("harness %q is disabled", harness)
	}
	env := r.toolEnv(threadID, agentID)
	env = append(env, "AT_ROLE=orchestrator", "AT_PASS="+strconv.Itoa(pass))
	spec := providers.SessionSpec{
		AgentID:  agentID,
		ThreadID: threadID,
		Role:     contracts.RoleOrchestrator,
		Harness:  harness,
		Model:    model,
		Variant:  variant,
		Prompt:   prompt,
		Images:   images,
		Workdir:  r.workdirFor(ctx, threadID),
		Env:      env,
	}
	adapter, err := r.registry.NewAdapter(spec)
	if err != nil {
		return "", nil, err
	}
	r.auditLog("agent.spawn", map[string]any{
		"agentId": string(agentID), "role": "orchestrator", "pass": pass,
		"harness": string(harness), "model": spec.Model, "workdir": spec.Workdir,
	})
	if err := adapter.Start(ctx, spec); err != nil {
		r.auditLog("agent.error", map[string]any{"agentId": string(agentID), "error": err.Error()})
		return "", nil, err
	}
	events, err := adapter.Stream(ctx)
	if err != nil {
		return "", nil, err
	}

	var text replyText
	var calls []ToolCall
	for ev := range events {
		if call, ok := ParseToolLine(ev.Text); ok {
			text.toolCall()
			// A protocol action frame is delivered through the same inbox
			// drain as a printed command line; only dispatch frames are
			// minted directly into the pass's task set here.
			if routed, ok := frameCall(call); ok && call.Name != "dispatch_task" {
				r.postAgentCall(threadID, agentID, routed)
				continue
			}
			calls = append(calls, *call)
			_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: agentID, Kind: contracts.LogTool, Text: ev.Text, At: r.clock()})
			r.publishOutput(threadID, agentID, contracts.LogTool, ev.Text)
			continue
		}
		r.recordEvent(ctx, threadID, agentID, ev)
		if ev.Usage != nil {
			r.recordUsage(ctx, threadID, agentID, contracts.RoleOrchestrator, true, *ev.Usage)
		}
		text.add(ev)
	}
	werr := adapter.Wait(ctx)
	r.auditLog("agent.exit", map[string]any{
		"agentId": string(agentID), "role": "orchestrator", "pass": pass,
		"status": statusWord(werr), "error": errString(werr),
	})

	// Some harnesses print the `$AT_TOOL tool …` command instead of executing it
	// with their shell tool. Treat those lines as real calls so dispatch still
	// works, and keep them out of the user-facing summary. The inbox dedupes a
	// call that was both executed and printed.
	// Text deltas are arbitrary chunks, so a command can straddle two events.
	// Join without adding separators; real newlines are already in the deltas.
	raw := text.String()
	var kept []string
	for _, line := range strings.Split(raw, "\n") {
		if call, ok := toolbox.ParseCommandLine(line); ok {
			if call.Kind.IsReview() {
				r.applyPrintedReview(ctx, threadID, agentID, call)
				continue
			}
			call.AgentID = agentID
			if r.tools != nil {
				r.tools.Post(threadID, call)
			}
			_ = r.logs.Append(threadID, contracts.LogEntry{
				AgentID: agentID, Kind: contracts.LogTool,
				Text: "tool " + string(call.Kind), At: r.clock(),
			})
			r.publishOutput(threadID, agentID, contracts.LogTool, "tool "+string(call.Kind))
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), calls, werr
}

// runSoloPass runs one one-shot session of a solo thread's single agent and
// returns its text. There is no orchestrator protocol: every text line is
// conversation, no tool lines are parsed or routed, and the spec carries no
// AT_* environment at all — the solo agent has lead git parity and the
// process adapter scrubs ambient AT_* regardless.
func (r *Runner) runSoloPass(ctx context.Context, threadID contracts.ThreadID, agentID contracts.AgentID, rt contracts.LeadRuntime, prompt string, images []providers.ImageInput, pass int) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	tc := r.trackCancel(agentID, cancel)
	defer r.untrackCancel(agentID, tc)

	if !rt.Harness.Valid() {
		return "", fmt.Errorf("solo harness %q is not set", rt.Harness)
	}
	if !r.harnessAllowed(rt.Harness) {
		return "", fmt.Errorf("harness %q is disabled", rt.Harness)
	}
	spec := providers.SessionSpec{
		AgentID:  agentID,
		ThreadID: threadID,
		Role:     contracts.RoleOrchestrator,
		Harness:  rt.Harness,
		Model:    rt.Model,
		Variant:  rt.Variant,
		Prompt:   prompt,
		Images:   images,
		Workdir:  r.workdirFor(ctx, threadID),
		Env:      r.toolEnv(threadID, agentID),
	}
	// Steering between tool calls needs the hook's call-back environment, so a
	// pass gets that environment only when the daemon can answer the hook.
	if r.toolPath != "" && r.endpoint != nil && r.steering != nil {
		spec.Env = r.toolEnv(threadID, agentID)
		spec.ToolSteering = true
	}
	adapter, err := r.registry.NewAdapter(spec)
	if err != nil {
		return "", err
	}
	r.auditLog("agent.spawn", map[string]any{
		"agentId": string(agentID), "role": soloName, "pass": pass,
		"harness": string(rt.Harness), "model": spec.Model, "workdir": spec.Workdir,
	})
	if err := adapter.Start(ctx, spec); err != nil {
		r.auditLog("agent.error", map[string]any{"agentId": string(agentID), "error": err.Error()})
		return "", err
	}
	events, err := adapter.Stream(ctx)
	if err != nil {
		return "", err
	}

	var text replyText
	emitted := false
	for ev := range events {
		emitted = true
		r.recordEvent(ctx, threadID, agentID, ev)
		if ev.Usage != nil {
			r.recordUsage(ctx, threadID, agentID, contracts.RoleOrchestrator, true, *ev.Usage)
		}
		text.add(ev)
	}
	werr := adapter.Wait(ctx)
	r.auditLog("agent.exit", map[string]any{
		"agentId": string(agentID), "role": soloName, "pass": pass,
		"status": statusWord(werr), "error": errString(werr),
	})

	// A harness that exits cleanly but emitted nothing is a silent failure —
	// the same guard runTask applies — and must not read as a green, empty
	// result.
	if werr == nil && !emitted {
		msg := "harness exited without any output (check its auth or quota)"
		_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: agentID, Kind: contracts.LogError, Text: msg, At: r.clock()})
		r.publishOutput(threadID, agentID, contracts.LogError, msg)
		return "", errors.New(msg)
	}

	return strings.TrimSpace(text.String()), werr
}

// replyText collects an agent's prose. Text deltas are arbitrary chunks of one
// block, so back-to-back chunks join without separators (real newlines are
// already in them). A tool call ends the block: the text after it is a new
// block (Claude sends each one whole) and starts a new paragraph instead of
// running into the last sentence.
type replyText struct {
	parts []string
	gap   bool
}

func (t *replyText) add(ev providers.Event) {
	switch {
	case ev.Kind == contracts.LogTool:
		t.toolCall()
	case ev.Kind == contracts.LogText && strings.TrimSpace(ev.Text) != "":
		if t.gap {
			t.parts = append(t.parts, "\n\n")
		}
		t.gap = false
		t.parts = append(t.parts, ev.Text)
	}
}

// toolCall ends the current text block.
func (t *replyText) toolCall() { t.gap = len(t.parts) > 0 }

func (t *replyText) String() string { return strings.Join(t.parts, "") }

// auditLog records one action for post-hoc debugging.
func (r *Runner) auditLog(kind string, fields map[string]any) {
	if r.audit != nil {
		r.audit(kind, fields)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func statusWord(err error) string {
	if err == nil {
		return "ok"
	}
	return "failed"
}

// Roster renders every subagent in the thread with its status, so the team lead
// can see what it has already dispatched.
func (r *Runner) Roster(state *domain.ThreadState) string {
	var b strings.Builder
	for _, id := range state.AgentOrder {
		a := state.Agents[id]
		if a.IsOrchestrator() {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): %s [%s]", a.Name, a.Role, clip(a.Task, rosterTaskBytes), a.Status)
		if (a.Status == contracts.AgentFailed || a.Status == contracts.AgentCancelled) && strings.TrimSpace(a.Reason) != "" {
			b.WriteString(" — " + clip(a.Reason, rosterReasonBytes))
		}
		b.WriteString("\n")
	}
	return b.String()
}

const (
	rosterTaskBytes   = 300
	rosterReasonBytes = 200
	// resultNotebookBytes caps one stored result_<name> entry; the full output
	// stays in the agent's log.
	resultNotebookBytes = 4096
)

// clipResult keeps the head of a long result and says how to read the rest.
func clipResult(s string) string {
	if len(s) <= resultNotebookBytes {
		return s
	}
	return clipHead(s, resultNotebookBytes) + fmt.Sprintf("[clipped, %d bytes total; read the full log with tool agent-logs]", len(s))
}

// workdir is the fallback directory handed to agents.
func (r *Runner) workdir() string {
	if r.settings == nil {
		return ""
	}
	return r.settings.Workdir()
}

// workdirFor resolves a thread's working directory: its worktree when it has one
// on disk, otherwise its project's path, falling back to the environment default
// when the thread has no project.
func (r *Runner) workdirFor(ctx context.Context, threadID contracts.ThreadID) string {
	if r.settings == nil {
		return ""
	}
	base := r.settings.Workdir()
	st, err := r.engine.State(ctx, threadID)
	if err != nil {
		return base
	}
	if st.ProjectID != "" {
		if project, ok := r.settings.Project(st.ProjectID); ok && project.Path != "" {
			base = project.Path
		}
	}
	return worktree.Resolve(st.Worktree, base)
}

// roleRuntimeFor resolves a role's runtime for one thread: the thread's
// thread-scoped override wins over the daemon-wide settings, over the role's
// built-in default. It reads the engine snapshot, never a shared mutable map, so
// concurrent turns running different jobs do not see each other's overrides.
// Empty override fields inherit the lower layer.
func (r *Runner) roleRuntimeFor(ctx context.Context, threadID contracts.ThreadID, role contracts.Role) (contracts.Harness, string, string) {
	harness, model, variant := r.roleRuntime(role)
	if r.engine == nil || threadID == "" {
		return harness, model, variant
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil {
		return harness, model, variant
	}
	ov, ok := st.RoleRuntimeFor(role.ID)
	if !ok {
		return harness, model, variant
	}
	// Overriding the harness drops the inherited model and variant, which
	// belong to the harness they were configured for. Mirror leadRuntime: a
	// harness-only override must not send the old harness's model to the new
	// one.
	if ov.Harness.Valid() && ov.Harness != harness {
		harness, model, variant = ov.Harness, "", ""
	}
	if ov.Model != "" {
		model = ov.Model
	}
	if ov.Variant != "" {
		variant = ov.Variant
	}
	return harness, model, variant
}

// roleRuntime resolves a role's harness/model/variant from settings and the
// role's built-in default, ignoring any thread-scoped override.
func (r *Runner) roleRuntime(role contracts.Role) (contracts.Harness, string, string) {
	harness, model, variant := role.DefaultHarness, role.DefaultModel, role.DefaultVariant
	if r.settings == nil {
		return harness, model, variant
	}
	cfg, ok := r.settings.RoleConfig(role.ID)
	if !ok {
		return harness, model, variant
	}
	if cfg.Harness.Valid() {
		harness = cfg.Harness
	}
	if cfg.Model != "" {
		model = cfg.Model
	}
	if cfg.Variant != "" {
		variant = cfg.Variant
	}
	return harness, model, variant
}

// harnessAllowed reports whether a harness is enabled in settings.
func (r *Runner) harnessAllowed(h contracts.Harness) bool {
	return r.settings == nil || r.settings.HarnessEnabled(h)
}

// roleUsable reports whether a role may run. Required roles always run, even if
// a hand-edited settings file disables them. An unknown role is never usable.
func (r *Runner) roleUsable(id contracts.RoleID) bool {
	role, ok := r.roles.Get(id)
	if !ok {
		return false
	}
	if role.Required {
		return true
	}
	if r.settings == nil {
		return !role.Experimental
	}
	return r.settings.RoleEnabled(id) && (!role.Experimental || r.settings.ExperimentalRoles())
}

// experimentalRoles reports whether the user opted into experimental roles.
func (r *Runner) experimentalRoles() bool {
	return r.settings != nil && r.settings.ExperimentalRoles()
}

// roleChoices splits the registry into the roles the lead may dispatch and the
// non-required roles that are off, rendered for the lead's prompt. The lead's
// own role and the facilitator are left out: the facilitator is dispatched only
// by the council machinery, never by the lead.
func (r *Runner) roleChoices() (usable, disabled string) {
	var on, off []string
	for _, role := range r.roles.List() {
		if role.ID == contracts.RoleOrchestrator || role.ID == contracts.RoleFacilitator {
			continue
		}
		if r.roleUsable(role.ID) {
			on = append(on, string(role.ID))
		} else {
			off = append(off, string(role.ID))
		}
	}
	return strings.Join(on, "|"), strings.Join(off, ", ")
}

// runtimeChoices renders the harness/model options the orchestrator may pick
// from in auto mode: enabled harnesses with every model each reports, so any
// listed model can be requested.
func (r *Runner) runtimeChoices() string {
	if r.registry == nil {
		return ""
	}
	var b strings.Builder
	for _, harness := range r.harnessList() {
		if r.settings != nil && !r.settings.HarnessEnabled(harness) {
			continue
		}
		var models []string
		if r.models != nil {
			models = r.models.List(harness)
		}
		fmt.Fprintf(&b, "- %s", harness)
		if len(models) > 0 {
			b.WriteString(": " + strings.Join(models, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// harnessList returns the harnesses worth offering: known to the registry.
func (r *Runner) harnessList() []contracts.Harness {
	return []contracts.Harness{
		contracts.HarnessOpenCode,
		contracts.HarnessClaude,
		contracts.HarnessCodex,
		contracts.HarnessPi,
		contracts.HarnessCommandCode,
	}
}

// autoModeFor reports whether the orchestrator picks per-task runtimes for a
// thread: the environment default, or the thread's own toggle.
func (r *Runner) autoModeFor(ctx context.Context, threadID contracts.ThreadID) bool {
	if r.settings != nil && r.settings.AutoMode() {
		return true
	}
	if r.engine == nil {
		return false
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil || st == nil {
		return false
	}
	return st.AutoMode
}

// userRequestFor is the thread's latest user-authored message, which the
// subagent prompt carries so a task is judged against what was asked. "" when
// there is none.
func (r *Runner) userRequestFor(ctx context.Context, threadID contracts.ThreadID) string {
	if r.engine == nil {
		return ""
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil || st == nil {
		return ""
	}
	for i := len(st.Messages) - 1; i >= 0; i-- {
		if st.Messages[i].Author == "user" {
			return st.Messages[i].Text
		}
	}
	return ""
}

// peerMailboxFor reports whether peer messaging is on for a thread, reading the
// durable thread state the same way auto mode is read.
func (r *Runner) peerMailboxFor(ctx context.Context, threadID contracts.ThreadID) bool {
	if r.engine == nil {
		return false
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil || st == nil {
		return false
	}
	return st.PeerMailbox
}

// teammateMessages drains the agent's runtime inbox at the moment its prompt is
// built. An agent waiting behind a dependency receives the peer traffic that
// arrived while it waited directly in its first prompt; draining keeps the
// lead's next-pass relay from repeating what was already delivered here.
func (r *Runner) teammateMessages(threadID contracts.ThreadID, id contracts.AgentID, peerMailbox bool) []contracts.MailboxPosted {
	if !peerMailbox || r.mailbox == nil {
		return nil
	}
	return r.mailbox.Drain(threadID, id)
}

// resolveModel picks the model a role runs on for the harness it will run on.
// Priority:
//
//  1. No request: the role's configured model, if the harness is the role's own.
//  2. A harness other than the role's never takes the role's model. Only an
//     explicit override (council membership) or an auto-mode request the
//     chosen harness allows is used; otherwise the model is empty and the
//     harness runs its own default.
//  3. A role with no configured model trusts the request (the settings UI
//     feeds per-role models through here).
//  4. An explicit user override (council membership) runs exactly what was
//     asked for.
//  5. Auto mode may pick another model, bounded by the thread's own approved
//     list, else the harness's environment list, when one is configured. With
//     neither, any model the harness actually reports is allowed — enabling
//     auto mode is the opt-in.
//
// Anything else falls back to the role's model.
func (r *Runner) resolveModel(role contracts.Role, harness contracts.Harness, requested string, auto, override bool) string {
	roleHarness, model, _ := r.roleRuntime(role)
	resolved, _ := r.resolveModelAgainst(context.Background(), "", roleHarness, model, harness, requested, auto, override)
	return resolved
}

// resolveModelFor is resolveModel with the thread's role override applied. When
// the requested model is dropped for the role or harness default, it leaves one
// model.fallback audit line so the choice can be read back later.
func (r *Runner) resolveModelFor(ctx context.Context, threadID contracts.ThreadID, role contracts.Role, harness contracts.Harness, requested string, auto, override bool) string {
	roleHarness, model, _ := r.roleRuntimeFor(ctx, threadID, role)
	resolved, reason := r.resolveModelAgainst(ctx, threadID, roleHarness, model, harness, requested, auto, override)
	if reason != "" && requested != "" && resolved != requested {
		shown := resolved
		if shown == "" {
			shown = "harness default"
		}
		r.auditLog("model.fallback", map[string]any{
			"threadId": string(threadID), "role": string(role.ID), "harness": string(harness),
			"requested": requested, "resolved": shown, "reason": reason,
		})
	}
	return resolved
}

// resolveModelAgainst returns the model to run and, when it is not the one
// requested, the reason it was dropped. An empty result means the harness default.
func (r *Runner) resolveModelAgainst(ctx context.Context, threadID contracts.ThreadID, roleHarness contracts.Harness, model string, harness contracts.Harness, requested string, auto, override bool) (string, string) {
	sameHarness := harness == roleHarness
	if !sameHarness {
		model = ""
	}
	if requested == "" {
		return model, ""
	}
	if override {
		return requested, ""
	}
	if !sameHarness {
		if !auto {
			return "", "auto mode off"
		}
		if r.autoAllowed(ctx, threadID, harness, requested) {
			return requested, ""
		}
		return "", "model not allowed in auto mode"
	}
	if model == "" {
		return requested, ""
	}
	if !auto {
		return model, "auto mode off"
	}
	if !r.autoAllowed(ctx, threadID, harness, requested) {
		return model, "model not allowed in auto mode"
	}
	return requested, ""
}

// autoAllowed reports whether a model may run in auto mode on a harness: a
// thread's own allowlist wins when it has one; otherwise inside that harness's
// approved list when it has one, otherwise any model the harness actually reports.
func (r *Runner) autoAllowed(ctx context.Context, threadID contracts.ThreadID, harness contracts.Harness, requested string) bool {
	if threadID != "" && r.engine != nil {
		if st, err := r.engine.State(ctx, threadID); err == nil && st != nil && len(st.AutoApprovedModels) > 0 {
			for _, m := range st.AutoApprovedModels[string(harness)] {
				if m == requested {
					return true
				}
			}
			return false
		}
	}
	if r.settings == nil {
		return true
	}
	if approved := r.settings.ApprovedModelsFor(harness); len(approved) > 0 {
		_, ok := approved[requested]
		return ok
	}
	if r.models == nil {
		return true
	}
	for _, m := range r.models.List(harness) {
		if m == requested {
			return true
		}
	}
	return false
}

func (r *Runner) recordUsage(ctx context.Context, threadID contracts.ThreadID, agent contracts.AgentID, role contracts.RoleID, orchestrator bool, u contracts.TokenUsage) {
	r.ledger.Record(agent, role, orchestrator, u)

	// Stamp the spend with the turn it belongs to, so usage can be read per turn.
	turn := 0
	if st, err := r.engine.State(ctx, threadID); err == nil {
		turn = st.Turn
	}

	_ = r.engine.Dispatch(ctx, contracts.Command{
		ID:       r.nextID(),
		Type:     contracts.CmdUsageRecord,
		ThreadID: threadID,
		Usage:    &contracts.UsageRecorded{AgentID: agent, Role: role, Usage: u, Turn: turn},
	})
}

// recordModel stores the model a harness reported for an agent. The decider
// drops repeats, so a model repeated on every frame costs no extra events.
func (r *Runner) recordModel(ctx context.Context, threadID contracts.ThreadID, agent contracts.AgentID, model string) {
	_ = r.engine.Dispatch(ctx, contracts.Command{
		ID:            r.nextID(),
		Type:          contracts.CmdAgentModelObserve,
		ThreadID:      threadID,
		ModelObserved: &contracts.AgentModelObserved{AgentID: agent, Model: model},
	})
}

func (r *Runner) setStatus(ctx context.Context, threadID contracts.ThreadID, agent contracts.AgentID, status contracts.AgentStatus, reason string) {
	// Status writes must survive the turn ending: a cancelled turn still owes
	// the thread its terminal status, so detach from the caller's context.
	_ = r.engine.Dispatch(context.WithoutCancel(ctx), contracts.Command{
		ID:       r.nextID(),
		Type:     contracts.CmdAgentStatusSet,
		ThreadID: threadID,
		Status:   &contracts.AgentStatusChanged{AgentID: agent, Status: status, Reason: reason},
	})
}

func (r *Runner) setSharedContext(ctx context.Context, threadID contracts.ThreadID, key, value string, author contracts.AgentID) {
	_ = r.engine.Dispatch(ctx, contracts.Command{
		ID:            r.nextID(),
		Type:          contracts.CmdSharedContextSet,
		ThreadID:      threadID,
		SharedContext: &contracts.SharedContextSet{Key: key, Value: value, AuthorID: author},
	})
}

func (r *Runner) nextID() string { return fmt.Sprintf("cmd-%d", r.seq.Add(1)) }

// publishOutput streams one agent output line to the thread's subscribers. Only
// human-meaningful kinds are streamed; usage and raw frames stay in the log.
func (r *Runner) publishOutput(threadID contracts.ThreadID, agent contracts.AgentID, kind contracts.LogKind, text string) {
	r.publishAgentOutput(threadID, contracts.AgentOutput{AgentID: agent, Kind: kind, Text: text})
}

func (r *Runner) publishAgentOutput(threadID contracts.ThreadID, out contracts.AgentOutput) {
	if out.Text == "" {
		return
	}
	switch out.Kind {
	case contracts.LogText, contracts.LogReasoning, contracts.LogTool, contracts.LogRaw, contracts.LogError:
	default:
		return
	}
	r.engine.Publish(threadID, out)
}

// recordEvent logs one adapter event and streams it, keeping its structured
// tool call on both.
func (r *Runner) recordEvent(ctx context.Context, threadID contracts.ThreadID, agent contracts.AgentID, ev providers.Event) {
	if ev.Model != "" {
		r.recordModel(ctx, threadID, agent, ev.Model)
	}
	if ev.Kind == "" {
		return
	}
	_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: agent, Kind: ev.Kind, Text: ev.Text, Tool: ev.Tool, At: r.clock()})
	r.publishAgentOutput(threadID, contracts.AgentOutput{AgentID: agent, Kind: ev.Kind, Text: ev.Text, Tool: ev.Tool})
}

// toolEnv is the environment that lets an agent call back through `atd tool`.
func (r *Runner) toolEnv(threadID contracts.ThreadID, agentID contracts.AgentID) []string {
	env := []string{
		"AT_THREAD_ID=" + string(threadID),
		"AT_AGENT_ID=" + string(agentID),
	}
	if r.toolPath != "" {
		env = append(env, "AT_TOOL="+r.toolPath)
	}
	if r.endpoint != nil {
		if url := r.endpoint(); url != "" {
			env = append(env, "AT_DAEMON_URL="+url)
		}
	}
	if r.token != "" {
		env = append(env, "AT_TOKEN="+r.token)
	}
	return env
}

// subagentGitEnv confines a subagent to read-only git. The lead keeps the full
// git so it can commit, branch, and merge the work its agents produced.
func subagentGitEnv(role contracts.Role) []string {
	if role.ID == contracts.RoleOrchestrator {
		return nil
	}
	return gitguard.Env()
}

// DrainTools returns and clears the tool calls posted for a thread.
func (r *Runner) DrainTools(threadID contracts.ThreadID) []toolbox.Call {
	if r.tools == nil {
		return nil
	}
	return r.tools.Drain(threadID)
}

// DrainSteering returns and clears the user's pending mid-run steering messages.
func (r *Runner) DrainSteering(threadID contracts.ThreadID) []steering.Entry {
	if r.steering == nil {
		return nil
	}
	return r.steering.Drain(threadID)
}

// recordSteeringTaken records that the agent took these steering messages: the
// injected count for the pending style, and the consumed ids so the transcript
// marks them. Both the solo pass loop and the tool-call hook call it.
func (r *Runner) recordSteeringTaken(ctx context.Context, threadID contracts.ThreadID, entries []steering.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	consumed := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.MessageID != "" {
			consumed = append(consumed, entry.MessageID)
		}
	}
	if err := r.engine.Dispatch(ctx, contracts.Command{
		ID:       r.nextID(),
		Type:     contracts.CmdSteeringInjected,
		ThreadID: threadID,
		Injected: &contracts.SteeringInjected{Count: len(entries)},
	}); err != nil {
		return err
	}
	if len(consumed) == 0 {
		return nil
	}
	return r.engine.Dispatch(ctx, contracts.Command{
		ID:              r.nextID(),
		Type:            contracts.CmdSteeringConsume,
		ThreadID:        threadID,
		SteeringConsume: &contracts.SteeringConsumed{MessageIDs: consumed},
	})
}

// HasSteering reports whether steering arrived for a thread.
func (r *Runner) HasSteering(threadID contracts.ThreadID) bool {
	return r.steering != nil && r.steering.Has(threadID)
}

// titleRuntimeFor is generateTitle's runtime resolution: the thread's solo
// runtime when set — a solo thread names itself on its own harness instead of
// quietly running the orchestrator role's — falling back to the role's.
func (r *Runner) titleRuntimeFor(ctx context.Context, threadID contracts.ThreadID, role contracts.Role) (contracts.Harness, string, string) {
	if r.engine != nil {
		if st, err := r.engine.State(ctx, threadID); err == nil && st != nil && st.SoloRuntime != nil {
			rt := *st.SoloRuntime
			return rt.Harness, rt.Model, rt.Variant
		}
	}
	return r.roleRuntimeFor(ctx, threadID, role)
}

// generateTitle asks the orchestrator's model to name the session. It is
// invoked at the start of the first turn (prompt only) and again when that turn
// completes (prompt plus the cycle's summary). It runs off the turn so it
// cannot delay the reply.
func (r *Runner) generateTitle(ctx context.Context, threadID contracts.ThreadID, role contracts.Role, userMessage, summary string) {
	harness, model, variant := r.titleRuntimeFor(ctx, threadID, role)
	if !r.harnessAllowed(harness) {
		return
	}

	const orchAgent = contracts.AgentID("agent-orchestrator")
	spec := providers.SessionSpec{
		AgentID:  orchAgent,
		ThreadID: threadID,
		Role:     contracts.RoleOrchestrator,
		Harness:  harness,
		Model:    model,
		Variant:  variant,
		Prompt:   BuildTitlePrompt(userMessage, summary),
		Workdir:  r.workdirFor(ctx, threadID),
		Env:      r.toolEnv(threadID, orchAgent),
	}

	adapter, err := r.registry.NewAdapter(spec)
	if err != nil {
		return
	}
	if err := adapter.Start(ctx, spec); err != nil {
		return
	}
	events, err := adapter.Stream(ctx)
	if err != nil {
		return
	}

	var text []string
	for ev := range events {
		if ev.Usage != nil {
			r.recordUsage(ctx, threadID, orchAgent, contracts.RoleOrchestrator, true, *ev.Usage)
		}
		if ev.Kind == contracts.LogText && strings.TrimSpace(ev.Text) != "" {
			text = append(text, ev.Text)
		}
	}
	_ = adapter.Wait(ctx)

	title := cleanTitle(strings.Join(text, "\n"))
	if title == "" || title == contracts.DefaultThreadTitle {
		return
	}
	_ = r.engine.Dispatch(ctx, contracts.Command{
		ID:       r.nextID(),
		Type:     contracts.CmdThreadRename,
		ThreadID: threadID,
		Title:    title,
	})
}

// cleanTitle reduces a model reply to a single short title.
func cleanTitle(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.Trim(line, "`\"'*#")
		line = strings.TrimSpace(line)
		if lower := strings.ToLower(line); strings.HasPrefix(lower, "title:") {
			line = strings.TrimSpace(line[len("title:"):])
		}
		line = strings.Join(strings.Fields(line), " ")
		if len(line) > 60 {
			line = strings.TrimSpace(line[:60])
		}
		if line != "" {
			return line
		}
	}
	return ""
}

// isDefaultTitle reports whether a thread still carries the placeholder name.
func isDefaultTitle(title string) bool {
	t := strings.TrimSpace(title)
	return t == "" || t == contracts.DefaultThreadTitle
}

// concurrency is the maximum number of tasks run at once.
func (r *Runner) concurrency() int {
	if r.cfg.MaxConcurrency > 0 {
		return r.cfg.MaxConcurrency
	}
	return 4
}

func (r *Runner) maxPasses() int {
	if r.cfg.MaxPasses > 0 {
		return r.cfg.MaxPasses
	}
	return 3
}

// threadStatuses seeds the scheduler with the thread's agents from earlier
// passes (latest per name wins), skipping the batch itself. An earlier agent
// still pending or running never changes during this batch, so a dependent on
// it is cancelled like any other unsatisfied dependency.
func (r *Runner) threadStatuses(ctx context.Context, threadID contracts.ThreadID, tasks []contracts.DispatchTask) map[string]contracts.AgentStatus {
	status := make(map[string]contracts.AgentStatus, len(tasks))
	if r.engine == nil {
		return status
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil || st == nil {
		return status
	}
	batch := make(map[contracts.AgentID]bool, len(tasks))
	for _, t := range tasks {
		batch[t.AgentID] = true
	}
	for _, id := range st.AgentOrder {
		a := st.Agents[id]
		if batch[id] || a.IsOrchestrator() {
			continue
		}
		status[a.Name] = a.Status
	}
	return status
}

// unknownDependency names the first dependency that is in neither the batch
// nor the thread, so a typo cannot read as satisfied.
func unknownDependency(deps []string, status map[string]contracts.AgentStatus) (string, bool) {
	for _, d := range deps {
		if _, ok := status[d]; !ok {
			return d, true
		}
	}
	return "", false
}

func depsSucceeded(deps []string, status map[string]contracts.AgentStatus) bool {
	for _, d := range deps {
		if status[d] != contracts.AgentSucceeded {
			return false
		}
	}
	return true
}
