package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// Orchestrator drives a user turn: it runs the team lead, executes whatever it
// dispatches, feeds the results back into shared context, and repeats until the
// lead stops dispatching or the pass budget runs out.
type Orchestrator struct {
	runner *Runner
	seq    atomic.Int64
}

// New returns an orchestrator backed by a runner.
func New(r *Runner) *Orchestrator { return &Orchestrator{runner: r} }

// safeName keeps a task name usable as part of an agent id and a log file name:
// the name comes from the model, so it may contain anything.
func safeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "task"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// toolTasks turns the tool calls posted for a thread into work items: notes
// fold into the shared notebook, a lead's dispatch or spawn becomes a task, and
// a subagent's proposal pends until the lead decides it.
func (o *Orchestrator) toolTasks(threadID contracts.ThreadID, orchID contracts.AgentID) []contracts.DispatchTask {
	var tasks []contracts.DispatchTask
	seen := map[string]bool{}
	names := map[string]bool{}
	for i, call := range o.runner.DrainTools(threadID) {
		// A command can arrive twice when an agent executes it and also prints
		// the command text. The parser posts the printed form; ignore the echo.
		key := toolCallKey(call)
		if seen[key] {
			continue
		}
		seen[key] = true
		// An empty agent id arrives from printed lines only after the lead's
		// id was stamped on, and from curls outside an agent session, which
		// have always been lead-equivalent; both count as the lead here.
		fromLead := call.AgentID == "" || call.AgentID == orchID
		switch call.Kind {
		case toolbox.KindNote, toolbox.KindProgress, toolbox.KindDone:
			if strings.TrimSpace(call.Text) != "" {
				o.runner.notebook.Append("notes", call.Text, string(call.AgentID))
			}

		case toolbox.KindLearn:
			if text := strings.TrimSpace(call.Text); text != "" && o.runner.inherited != nil {
				_ = o.runner.inherited.Record(text)
				o.runner.auditLog("context.learn", map[string]any{"agentId": string(call.AgentID), "text": text})
			}

		case toolbox.KindDispatch, toolbox.KindSpawn:
			if fromLead {
				// The lead is the approver, so its own spawn is simply a
				// dispatch.
				if task, ok := o.dispatchTask(orchID, call, i, names); ok {
					tasks = append(tasks, task)
				}
				continue
			}
			o.recordSpawnRequest(threadID, call, i)

		case toolbox.KindApprove, toolbox.KindReject:
			if !fromLead {
				o.runner.auditLog("spawn.decision-ignored", map[string]any{
					"threadId": string(threadID), "agentId": string(call.AgentID), "kind": string(call.Kind),
				})
				continue
			}
			o.decideSpawnRequest(threadID, call, &tasks, names)
		}
	}
	return tasks
}

// dispatchTask converts one lead dispatch or spawn call into a task, skipping
// empty ones. names records the task names built in this drain so an approved
// request cannot silently collide with one of them.
func (o *Orchestrator) dispatchTask(parentID contracts.AgentID, call toolbox.Call, i int, names map[string]bool) (contracts.DispatchTask, bool) {
	if strings.TrimSpace(call.Task) == "" {
		return contracts.DispatchTask{}, false
	}
	name := strings.TrimSpace(call.Name)
	if name == "" {
		name = fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), i)
	}
	name = safeName(name)
	names[name] = true
	role := call.Role
	if role == "" {
		role = contracts.RoleExecutor
	}
	// Agent ids must be unique per dispatch: the same task name can be
	// dispatched again on a later pass.
	id := fmt.Sprintf("agent-%s-%d", name, o.seq.Add(1))
	task := contracts.DispatchTask{
		AgentID:       contracts.AgentID(id),
		ParentAgentID: parentID,
		Name:          name,
		Role:          role,
		Task:          call.Task,
		Harness:       contracts.Harness(call.Harness),
		Model:         call.Model,
		Pipeline:      contracts.PipelineID(call.Lane),
		DependsOn:     call.DependsOn,
	}
	return task, true
}

// recordSpawnRequest parks a subagent's dispatch or spawn call as a pending
// request the thread's lead must decide. Requests for further orchestrators,
// for unknown roles, or without a task are refused outright rather than left
// for the lead to reject.
func (o *Orchestrator) recordSpawnRequest(threadID contracts.ThreadID, call toolbox.Call, i int) {
	role := call.Role
	if role == "" {
		role = contracts.RoleExecutor
	}
	kindWord := "spawn request"
	if call.Kind == toolbox.KindDispatch {
		kindWord = "dispatch request"
	}
	refuse := func(reason string) {
		o.runner.notebook.Append("notes", fmt.Sprintf("Refused %s from %s: %s", kindWord, call.AgentID, reason), "orchestrator")
		o.runner.auditLog("spawn.refused", map[string]any{
			"threadId": string(threadID), "requester": string(call.AgentID),
			"role": string(role), "reason": reason,
		})
	}
	switch {
	case strings.TrimSpace(call.Task) == "":
		refuse("it has no task")
		return
	case role == contracts.RoleOrchestrator:
		refuse("only the team lead may orchestrate")
		return
	}
	if _, known := o.runner.roles.Get(role); !known {
		refuse(fmt.Sprintf("unknown role %q", role))
		return
	}
	name := strings.TrimSpace(call.Name)
	if name == "" {
		name = fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), i)
	}
	name = safeName(name)
	if o.runner.spawns.add(threadID, spawnRequest{
		RequesterID: call.AgentID,
		Name:        name,
		Role:        role,
		Task:        call.Task,
		Harness:     call.Harness,
		Model:       call.Model,
		Lane:        call.Lane,
		DependsOn:   call.DependsOn,
	}) {
		o.runner.auditLog("spawn.requested", map[string]any{
			"threadId": string(threadID), "requester": string(call.AgentID),
			"name": name, "role": string(role), "task": call.Task,
		})
	}
}

// decideSpawnRequest applies the lead's approve or reject call to the matching
// pending request. An approval converts the request into a task parented on the
// requester, preserving everything the proposal asked for.
func (o *Orchestrator) decideSpawnRequest(threadID contracts.ThreadID, call toolbox.Call, tasks *[]contracts.DispatchTask, names map[string]bool) {
	var req spawnRequest
	var ok bool
	// Approve selects by the same safe names the pending section shows; the
	// empty case must not fall through to safeName's "task" default.
	if trimmed := strings.TrimSpace(call.Name); trimmed != "" {
		req, ok = o.runner.spawns.take(threadID, safeName(trimmed))
	}
	if !ok {
		o.runner.notebook.Append("notes", fmt.Sprintf("No pending spawn request named %q.", strings.TrimSpace(call.Name)), "orchestrator")
		o.runner.auditLog("spawn.unknown", map[string]any{"threadId": string(threadID), "name": call.Name})
		return
	}
	if call.Kind == toolbox.KindReject {
		o.runner.notebook.Append("notes", fmt.Sprintf("Rejected spawn request %q from %s.", req.Name, req.RequesterID), "orchestrator")
		o.runner.auditLog("spawn.rejected", map[string]any{
			"threadId": string(threadID), "name": req.Name, "requester": string(req.RequesterID),
		})
		return
	}
	// Two requests approved in one drain would collide in the name-keyed wave
	// bookkeeping, so suffix rather than duplicate.
	name := req.Name
	for i := 2; names[name]; i++ {
		name = fmt.Sprintf("%s-%d", req.Name, i)
	}
	names[name] = true
	id := fmt.Sprintf("agent-%s-%d", name, o.seq.Add(1))
	*tasks = append(*tasks, contracts.DispatchTask{
		AgentID:       contracts.AgentID(id),
		ParentAgentID: req.RequesterID,
		Name:          name,
		Role:          req.Role,
		Task:          req.Task,
		Harness:       contracts.Harness(req.Harness),
		Model:         req.Model,
		Pipeline:      contracts.PipelineID(req.Lane),
		DependsOn:     req.DependsOn,
	})
	o.runner.auditLog("spawn.approved", map[string]any{
		"threadId": string(threadID), "name": name,
		"requester": string(req.RequesterID), "agentId": id,
	})
}

// toolCallKey identifies a call for deduplication within one pass. Agent id is
// intentionally excluded: a printed echo and the real call may carry different
// attribution but represent the same work.
func toolCallKey(call toolbox.Call) string {
	return strings.Join([]string{
		string(call.Kind), call.Name, call.Task, call.Text, call.Harness,
		call.Model, call.Lane, strings.Join(call.DependsOn, ","),
	}, "\x00")
}

// renderPendingSpawns formats a thread's pending spawn requests for the lead's
// pass prompt, keyed by the name approve/reject select on.
func renderPendingSpawns(state *domain.ThreadState, reqs []spawnRequest) string {
	if len(reqs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Pending spawn requests\n")
	b.WriteString("A subagent proposed spawning another agent. Nothing runs until you decide:\n")
	for _, req := range reqs {
		requester := string(req.RequesterID)
		if a, ok := state.Agents[req.RequesterID]; ok && a.Name != "" {
			requester = a.Name
		}
		fmt.Fprintf(&b, "- %s — from %s (%s): %s — %q\n", req.Name, requester, req.RequesterID, req.Role, req.Task)
	}
	b.WriteString("Run it with \"$AT_TOOL\" tool approve --name <name>, decline with \"$AT_TOOL\" tool reject --name <name>.\n")
	b.WriteString("Requests left undecided when this turn ends are dropped.")
	return b.String()
}

// RunTurn executes one user turn end to end.
func (o *Orchestrator) RunTurn(ctx context.Context, threadID contracts.ThreadID, userMessage string) (err error) {
	role, ok := o.runner.roles.Get(contracts.RoleOrchestrator)
	if !ok {
		return errors.New("orchestrator role is not registered")
	}

	const orchName = "orchestrator"
	orchID := contracts.AgentID("agent-orchestrator")
	if err := o.runner.Dispatch(ctx, threadID, []contracts.DispatchTask{{
		AgentID: orchID,
		Name:    orchName,
		Role:    contracts.RoleOrchestrator,
		Task:    userMessage,
	}}); err != nil {
		return err
	}
	o.runner.setStatus(ctx, threadID, orchID, contracts.AgentRunning, "")

	// Whatever goes wrong mid-turn — failure, engine errors, or a stop landing
	// between passes — the orchestrator owes the thread a terminal status.
	// setStatus detaches from the turn context so a cancelled turn can still
	// record it.
	defer func() {
		if err == nil {
			return
		}
		finishCtx := context.WithoutCancel(ctx)
		if ctx.Err() != nil {
			o.runner.setStatus(finishCtx, threadID, orchID, contracts.AgentCancelled, "turn stopped")
		} else {
			o.runner.setStatus(finishCtx, threadID, orchID, contracts.AgentFailed, err.Error())
		}
	}()

	// Pending spawn requests never outlive their turn, whatever the exit path.
	defer o.runner.spawns.clear(threadID)

	var finalText string
	var dispatched []contracts.DispatchTask
	for pass := 0; pass < o.runner.maxPasses(); pass++ {
		// Calls that arrived while the previous wave ran — a subagent proposing
		// a spawn, a late note — fold in before the prompt is built so the
		// lead sees them this pass rather than the next.
		carried := o.toolTasks(threadID, orchID)
		inherited, _ := o.runner.inherited.Render()
		state, err := o.runner.engine.State(ctx, threadID)
		if err != nil {
			return err
		}
		// Hand any pending review comments to the team as shared context.
		if len(state.ReviewComments) > 0 {
			o.runner.notebook.Set("review_comments", renderReviewComments(state.ReviewComments), "user")
		}
		// Steering the user sent while this turn was running is weighed here.
		steering := o.runner.DrainSteering(threadID)
		// Auto mode gets the menu it may pick from; otherwise the prompt stays
		// as before.
		runtimeChoices := ""
		if o.runner.autoModeFor(ctx, threadID) {
			runtimeChoices = o.runner.runtimeChoices()
		}
		prompt := BuildOrchestratorPrompt(role, userMessage, inherited, o.runner.notebook.Render(), o.runner.Roster(state), renderPendingSpawns(state, o.runner.spawns.list(threadID)), pass, steering, runtimeChoices)

		text, calls, err := o.runner.runOrchestratorPass(ctx, threadID, orchID, role, prompt, pass)
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) != "" {
			finalText = text
		}

		// Work can arrive two ways: the tool command the agent runs, or the
		// legacy text protocol.
		tasks := ToDispatchTasks(orchID, Dispatches(calls))
		tasks = append(tasks, o.toolTasks(threadID, orchID)...)
		tasks = append(tasks, carried...)
		// A pass that dispatched nothing still earns another pass if the user
		// steered while it was running or a spawn request is still undecided.
		if len(tasks) == 0 && !o.runner.HasSteering(threadID) && len(o.runner.spawns.list(threadID)) == 0 {
			break
		}
		if len(tasks) == 0 {
			continue
		}
		dispatched = append(dispatched, tasks...)
		if err := o.runner.Dispatch(ctx, threadID, tasks); err != nil {
			return err
		}
		if err := o.runner.Run(ctx, threadID, tasks); err != nil {
			return err
		}
	}

	// A harness that only printed its dispatch commands leaves no prose. Give the
	// user a concrete receipt instead of an empty final message.
	if strings.TrimSpace(finalText) == "" && len(dispatched) > 0 {
		names := make([]string, 0, len(dispatched))
		for _, t := range dispatched {
			if t.Name != "" {
				names = append(names, t.Name)
			}
		}
		if len(names) == 0 {
			finalText = fmt.Sprintf("Dispatched %d task(s).", len(dispatched))
		} else {
			finalText = fmt.Sprintf("Dispatched %d task(s): %s.", len(dispatched), strings.Join(names, ", "))
		}
	}

	o.runner.setStatus(ctx, threadID, orchID, contracts.AgentSucceeded, "")
	if strings.TrimSpace(finalText) != "" {
		if err := o.runner.engine.Dispatch(ctx, contracts.Command{
			ID:       o.runner.nextID(),
			Type:     contracts.CmdMessageAppend,
			ThreadID: threadID,
			Message:  &contracts.MessageSent{MessageID: o.runner.nextID(), Author: "orchestrator", Text: finalText},
		}); err != nil {
			return err
		}
	}

	// Name the thread once, from the first exchange, using the orchestrator's
	// model. It runs off the turn so it cannot delay the reply.
	if state, err := o.runner.engine.State(ctx, threadID); err == nil && isDefaultTitle(state.Title) {
		go o.runner.generateTitle(context.Background(), threadID, role, userMessage, finalText)
	}
	return nil
}
