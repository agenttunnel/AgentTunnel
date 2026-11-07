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
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/steering"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// Config controls how a runner schedules and executes work.
type Config struct {
	MaxPasses      int
	MaxConcurrency int
}

// Deps are the collaborators a runner needs.
type Deps struct {
	Engine    *domain.Engine
	Registry  *providers.Registry
	Roles     *contracts.RoleRegistry
	Logs      *logstore.Store
	Ledger    *usage.Ledger
	Notebook  *contextstore.Notebook
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
}

// Runner executes dispatched tasks with dependency-aware, bounded concurrency,
// and records status, usage, logs, and findings as it goes.
type Runner struct {
	engine    *domain.Engine
	registry  *providers.Registry
	roles     *contracts.RoleRegistry
	logs      *logstore.Store
	ledger    *usage.Ledger
	notebook  *contextstore.Notebook
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
}

// NewRunner wires a runner from its dependencies.
func NewRunner(d Deps) *Runner {
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &Runner{
		engine:    d.Engine,
		registry:  d.Registry,
		roles:     d.Roles,
		logs:      d.Logs,
		ledger:    d.Ledger,
		notebook:  d.Notebook,
		mailbox:   d.Mailbox,
		inherited: d.Inherited,
		settings:  d.Settings,
		models:    d.Models,
		tools:     d.Tools,
		steering:  d.Steering,
		audit:     d.Audit,
		toolPath:  d.ToolPath,
		token:     d.Token,
		endpoint:  d.Endpoint,
		clock:     d.Clock,
		cfg:       d.Cfg,
	}
}

// Ledger exposes the environment usage ledger.
func (r *Runner) Ledger() *usage.Ledger { return r.ledger }

// Inherited exposes the environment's inherited context store.
func (r *Runner) Inherited() *contextstore.InheritedContext { return r.inherited }

// Notebook exposes the shared notebook.
func (r *Runner) Notebook() *contextstore.Notebook { return r.notebook }

// Mailbox exposes the runtime peer mailbox.
func (r *Runner) Mailbox() *contextstore.Mailbox { return r.mailbox }

// Logs exposes the per-agent log store.
func (r *Runner) Logs() *logstore.Store { return r.logs }

// Dispatch registers each task as an agent in the engine (creating the agent
// records) before execution.
func (r *Runner) Dispatch(ctx context.Context, threadID contracts.ThreadID, tasks []contracts.DispatchTask) error {
	for _, t := range tasks {
		task := t
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
	status := make(map[string]contracts.AgentStatus, len(tasks))
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
				findings, err := r.runTask(ctx, threadID, t)
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
func (r *Runner) runTask(ctx context.Context, threadID contracts.ThreadID, t contracts.DispatchTask) (string, error) {
	// A cancellation here targets only this agent: either the turn ended or
	// the user stopped the agent specifically. Both map to a cancelled status.
	ctx, cancel := context.WithCancel(ctx)
	tc := r.trackCancel(t.AgentID, cancel)
	defer r.untrackCancel(t.AgentID, tc)

	role, ok := r.roles.Get(t.Role)
	if !ok {
		return "", fmt.Errorf("unknown role %q", t.Role)
	}
	auto := r.autoModeFor(ctx, threadID)
	// An explicit user choice (council membership) is honored regardless of
	// auto mode; an orchestrator pick still needs auto mode to override.
	overrideRuntime := auto || t.Override
	harness, _, variant := r.roleRuntime(role)
	// In auto mode the orchestrator picks the runtime per task: a requested
	// harness wins when it is known and enabled, mirroring how resolveModel
	// treats requested models. Otherwise the role's configured harness stands.
	if overrideRuntime && t.Harness.Valid() && r.harnessAllowed(t.Harness) {
		harness = t.Harness
	}
	if !r.harnessAllowed(harness) {
		return "", fmt.Errorf("harness %q is disabled", harness)
	}
	model := r.resolveModel(role, harness, t.Model, auto, t.Override)
	r.setStatus(ctx, threadID, t.AgentID, contracts.AgentRunning, "")

	inherited, _ := r.inherited.Render()
	peerMailbox := r.peerMailboxFor(ctx, threadID)
	prompt := BuildSubagentPrompt(role, t.Task, inherited, r.notebook.Render(), peerMailbox, r.teammateMessages(t.AgentID, peerMailbox))

	env := r.toolEnv(threadID, t.AgentID)
	env = append(env, "AT_ROLE="+string(t.Role), "AT_TASK="+t.Task)
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
		"model": model, "workdir": spec.Workdir, "task": t.Task,
	})
	if err := adapter.Start(ctx, spec); err != nil {
		r.auditLog("agent.error", map[string]any{"agentId": string(t.AgentID), "error": err.Error()})
		return "", err
	}
	events, err := adapter.Stream(ctx)
	if err != nil {
		return "", err
	}

	var text []string
	emitted := false
	for ev := range events {
		emitted = true
		_ = r.logs.Append(contracts.LogEntry{AgentID: t.AgentID, Kind: ev.Kind, Text: ev.Text, At: r.clock()})
		r.publishOutput(threadID, t.AgentID, ev.Kind, ev.Text)
		if ev.Usage != nil {
			r.recordUsage(ctx, threadID, t.AgentID, t.Role, false, *ev.Usage)
		}
		if ev.Kind == contracts.LogText && strings.TrimSpace(ev.Text) != "" {
			// A protocol message frame is a tool action, not a finding.
			if call, ok := ParseToolLine(ev.Text); ok && call.Name == "message" &&
				strings.TrimSpace(call.To) != "" && strings.TrimSpace(call.Body) != "" {
				r.postMessageCall(threadID, t.AgentID, call.To, call.Body)
				continue
			}
			text = append(text, ev.Text)
		}
	}
	werr := adapter.Wait(ctx)
	r.auditLog("agent.exit", map[string]any{
		"agentId": string(t.AgentID), "status": statusWord(werr), "error": errString(werr),
	})

	// A harness that exits cleanly but emitted nothing is a silent failure —
	// a dead API key, a swallowed quota error — and must not read as success.
	// Surface it as an error instead of an empty, green agent.
	if werr == nil && !emitted {
		msg := "harness exited without any output (check its auth or quota)"
		_ = r.logs.Append(contracts.LogEntry{AgentID: t.AgentID, Kind: contracts.LogError, Text: msg, At: r.clock()})
		r.publishOutput(threadID, t.AgentID, contracts.LogError, msg)
		return "", errors.New(msg)
	}

	// Some harnesses print the `$AT_TOOL tool message …` command instead of
	// executing it. Route those lines through the tool inbox, attributed to
	// this agent, so the next pass delivers them like a real call; everything
	// else stays the agent's findings. Text deltas are arbitrary chunks, so a
	// printed command can straddle two events — join without separators, the
	// real newlines are already in the deltas.
	raw := strings.Join(text, "")
	var findings []string
	for _, line := range strings.Split(raw, "\n") {
		if call, ok := toolbox.ParseCommandLine(line); ok && call.Kind == toolbox.KindMessage {
			r.postMessageCall(threadID, t.AgentID, call.To, call.Text)
			continue
		}
		findings = append(findings, line)
	}

	// A summary of only whitespace is no result at all; the trailing split of
	// a stream ending in a newline must not become one either.
	summary := strings.TrimSpace(strings.Join(findings, "\n"))
	if summary != "" {
		key := "result_" + t.Name
		r.notebook.Append(key, summary, string(t.AgentID))
		r.setSharedContext(ctx, threadID, key, summary, t.AgentID)
	}
	return summary, werr
}

// postMessageCall queues one peer message pulled out of an agent's stream — a
// printed command line or a protocol frame — as a tool call attributed to that
// agent, so the next toolTasks drain delivers it through PostMessage.
func (r *Runner) postMessageCall(threadID contracts.ThreadID, from contracts.AgentID, to, body string) {
	if r.tools == nil {
		return
	}
	r.tools.Post(threadID, toolbox.Call{Kind: toolbox.KindMessage, AgentID: from, To: to, Text: body})
	_ = r.logs.Append(contracts.LogEntry{AgentID: from, Kind: contracts.LogTool, Text: "tool message", At: r.clock()})
	r.publishOutput(threadID, from, contracts.LogTool, "tool message")
}

// runOrchestratorPass runs one orchestrator process and collects its text output
// and tool calls. Usage from the orchestrator is attributed as orchestrator.
func (r *Runner) runOrchestratorPass(ctx context.Context, threadID contracts.ThreadID, agentID contracts.AgentID, role contracts.Role, prompt string, pass int) (string, []ToolCall, error) {
	ctx, cancel := context.WithCancel(ctx)
	tc := r.trackCancel(agentID, cancel)
	defer r.untrackCancel(agentID, tc)

	harness, _, variant := r.roleRuntime(role)
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
		Model:    r.resolveModel(role, harness, "", r.autoModeFor(ctx, threadID), false),
		Variant:  variant,
		Prompt:   prompt,
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

	var text []string
	var calls []ToolCall
	for ev := range events {
		if call, ok := ParseToolLine(ev.Text); ok {
			// A protocol message frame is delivered through the same inbox
			// drain as a printed command line; only dispatch actions can be
			// acted on directly here.
			if call.Name == "message" && strings.TrimSpace(call.To) != "" && strings.TrimSpace(call.Body) != "" {
				r.postMessageCall(threadID, agentID, call.To, call.Body)
				continue
			}
			calls = append(calls, *call)
			_ = r.logs.Append(contracts.LogEntry{AgentID: agentID, Kind: contracts.LogTool, Text: ev.Text, At: r.clock()})
			r.publishOutput(threadID, agentID, contracts.LogTool, ev.Text)
			continue
		}
		_ = r.logs.Append(contracts.LogEntry{AgentID: agentID, Kind: ev.Kind, Text: ev.Text, At: r.clock()})
		r.publishOutput(threadID, agentID, ev.Kind, ev.Text)
		if ev.Usage != nil {
			r.recordUsage(ctx, threadID, agentID, contracts.RoleOrchestrator, true, *ev.Usage)
		}
		if ev.Kind == contracts.LogText && strings.TrimSpace(ev.Text) != "" {
			text = append(text, ev.Text)
		}
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
	raw := strings.Join(text, "")
	var kept []string
	for _, line := range strings.Split(raw, "\n") {
		if call, ok := toolbox.ParseCommandLine(line); ok {
			call.AgentID = agentID
			if r.tools != nil {
				r.tools.Post(threadID, call)
			}
			_ = r.logs.Append(contracts.LogEntry{
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
		fmt.Fprintf(&b, "- %s (%s): %s [%s]\n", a.Name, a.Role, a.Task, a.Status)
	}
	return b.String()
}

// workdir is the fallback directory handed to agents.
func (r *Runner) workdir() string {
	if r.settings == nil {
		return ""
	}
	return r.settings.Workdir()
}

// workdirFor resolves a thread's working directory: its project's path, falling
// back to the environment default when the thread has no project.
func (r *Runner) workdirFor(ctx context.Context, threadID contracts.ThreadID) string {
	if r.settings == nil {
		return ""
	}
	if st, err := r.engine.State(ctx, threadID); err == nil && st.ProjectID != "" {
		if project, ok := r.settings.Project(st.ProjectID); ok && project.Path != "" {
			return project.Path
		}
	}
	return r.settings.Workdir()
}

// roleRuntime resolves a role's harness/model/variant, preferring a user override
// from settings over the role's built-in default.
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

// runtimeChoices renders the harness/model options the orchestrator may pick
// from in auto mode: enabled harnesses that are actually installed, with the
// models each reports. Capped per harness so the prompt stays small; the role
// defaults are always listed.
func (r *Runner) runtimeChoices() string {
	if r.registry == nil {
		return ""
	}
	var b strings.Builder
	for _, harness := range r.harnessList() {
		if r.settings != nil && !r.settings.HarnessEnabled(harness) {
			continue
		}
		models := []string{}
		if r.models != nil {
			models = r.models.List(harness)
		}
		if len(models) > 8 {
			models = models[:8]
			models = append(models, "…")
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
		contracts.HarnessCursor,
		contracts.HarnessGrok,
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
func (r *Runner) teammateMessages(id contracts.AgentID, peerMailbox bool) []contracts.MailboxPosted {
	if !peerMailbox || r.mailbox == nil {
		return nil
	}
	return r.mailbox.Drain(id)
}

// resolveModel applies auto mode: the orchestrator's requested model wins only if
// it is approved; otherwise the role's configured model is used.
// resolveModel picks the model a role runs on. Priority:
//
//  1. No request: the role's configured model.
//  2. A role with no configured model trusts the request (the settings UI
//     feeds per-role models through here).
//  3. An explicit user override (council membership) runs exactly what was
//     asked for.
//  4. Auto mode may pick another model, bounded by the approved list when one
//     is configured. With no list, any model the harness actually reports is
//     allowed — enabling auto mode is the opt-in.
//
// Anything else falls back to the role's model.
func (r *Runner) resolveModel(role contracts.Role, harness contracts.Harness, requested string, auto, override bool) string {
	_, model, _ := r.roleRuntime(role)
	if requested == "" {
		return model
	}
	if model == "" || override {
		return requested
	}
	if !auto || !r.autoAllowed(harness, requested) {
		return model
	}
	return requested
}

// autoAllowed reports whether a model may run in auto mode: inside the
// approved list when one exists, otherwise any model the chosen harness
// actually reports.
func (r *Runner) autoAllowed(harness contracts.Harness, requested string) bool {
	if r.settings == nil {
		return true
	}
	if approved := r.settings.ApprovedModels(); len(approved) > 0 {
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
	if text == "" {
		return
	}
	switch kind {
	case contracts.LogText, contracts.LogReasoning, contracts.LogTool, contracts.LogRaw, contracts.LogError:
	default:
		return
	}
	r.engine.Publish(threadID, contracts.AgentOutput{AgentID: agent, Kind: kind, Text: text})
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

// DrainTools returns and clears the tool calls posted for a thread.
func (r *Runner) DrainTools(threadID contracts.ThreadID) []toolbox.Call {
	if r.tools == nil {
		return nil
	}
	return r.tools.Drain(threadID)
}

// DrainSteering returns and clears the user's pending mid-run steering messages.
func (r *Runner) DrainSteering(threadID contracts.ThreadID) []string {
	if r.steering == nil {
		return nil
	}
	return r.steering.Drain(threadID)
}

// HasSteering reports whether steering arrived for a thread.
func (r *Runner) HasSteering(threadID contracts.ThreadID) bool {
	return r.steering != nil && r.steering.Has(threadID)
}

// generateTitle asks the orchestrator's model to name the session, once, after
// the first exchange. It runs off the turn so it cannot delay the reply.
func (r *Runner) generateTitle(ctx context.Context, threadID contracts.ThreadID, role contracts.Role, userMessage, summary string) {
	harness, model, variant := r.roleRuntime(role)
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

func depsSucceeded(deps []string, status map[string]contracts.AgentStatus) bool {
	for _, d := range deps {
		st, ok := status[d]
		if !ok {
			continue
		}
		if st != contracts.AgentSucceeded {
			return false
		}
	}
	return true
}
