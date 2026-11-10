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
// fold into the shared notebook, peer messages forward through the mailbox, a
// lead's dispatch or spawn becomes a task, and a subagent's proposal pends
// until the lead decides it.
//
// `minted` lists dispatches already built for this pass (protocol dispatches;
// tasks carried in from the pre-pass drain) that join the returned set and act
// as resolution candidates a peer message can name; `claimed` adds further
// names the surrounding batch has already taken. Wave bookkeeping — status,
// results, dependency resolution — is name-keyed, so names stay unique across
// the whole pass batch.
func (o *Orchestrator) toolTasks(ctx context.Context, threadID contracts.ThreadID, orchID contracts.AgentID, minted []contracts.DispatchTask, claimed map[string]bool) []contracts.DispatchTask {
	tasks := append([]contracts.DispatchTask(nil), minted...)
	names := map[string]bool{}
	for name := range claimed {
		names[name] = true
	}
	for _, t := range minted {
		names[t.Name] = true
	}
	used := o.threadNameSet(ctx, threadID)
	taken := func(name string) bool { return used[name] || names[name] }
	seen := map[string]bool{}
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
		// This is an honesty gate, not a security boundary: the tool endpoint
		// authenticates a shared daemon token, so any process holding it —
		// including a subagent's harness — could self-attest the lead's id.
		// It confines well-meaning agents, like the rest of the endpoint's
		// surface already does.
		fromLead := call.AgentID == "" || isLeadID(call.AgentID)
		switch call.Kind {
		case toolbox.KindNote, toolbox.KindProgress, toolbox.KindDone:
			if strings.TrimSpace(call.Text) != "" {
				o.runner.notebookFor(threadID).Append("notes", call.Text, string(call.AgentID))
			}

		case toolbox.KindLearn:
			if text := strings.TrimSpace(call.Text); text != "" && o.runner.inherited != nil {
				_ = o.runner.inherited.Record(text)
				o.runner.auditLog("context.learn", map[string]any{"agentId": string(call.AgentID), "text": text})
			}

		case toolbox.KindMessage:
			// A rejection (peer mailbox off, unknown recipient) is already
			// audited inside PostMessage; there is no task to make of it. The
			// tasks minted so far — the seeds plus anything built earlier in
			// this drain — act as resolution candidates, so a lead can
			// dispatch an agent and then message it while the agent still
			// waits on dependencies.
			if err := o.runner.postMessage(ctx, threadID, call.AgentID, call.To, call.Text, tasks); err != nil {
				// The turn survives a rejected message, but the sender — and
				// the model behind it — must see why, or it will assume the
				// message arrived.
				line := "tool message failed: " + err.Error()
				_ = o.runner.logs.Append(threadID, contracts.LogEntry{AgentID: call.AgentID, Kind: contracts.LogError, Text: line, At: o.runner.clock()})
				o.runner.publishOutput(threadID, call.AgentID, contracts.LogError, line)
			}

		case toolbox.KindDispatch, toolbox.KindSpawn:
			if fromLead {
				// The lead is the approver, so its own spawn is simply a
				// dispatch.
				if task, ok := o.dispatchTask(orchID, call, taken); ok {
					names[task.Name] = true
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
// empty ones. An unnamed dispatch gets a docker-style name that `taken` reports
// as unused; a named one keeps the lead's name, which dependencies refer to.
func (o *Orchestrator) dispatchTask(parentID contracts.AgentID, call toolbox.Call, taken func(string) bool) (contracts.DispatchTask, bool) {
	if strings.TrimSpace(call.Task) == "" {
		return contracts.DispatchTask{}, false
	}
	// Agent ids must be unique per dispatch: the same task name can be
	// dispatched again on a later pass.
	seq := o.seq.Add(1)
	name := strings.TrimSpace(call.Name)
	if name == "" {
		name = generateAgentName(fmt.Sprintf("agent-%d", seq), taken)
	} else {
		name = safeName(name)
	}
	role := call.Role
	if role == "" {
		role = contracts.RoleExecutor
	}
	id := fmt.Sprintf("agent-%s-%d", name, seq)
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
		o.runner.notebookFor(threadID).Append("notes", fmt.Sprintf("Refused %s from %s: %s", kindWord, call.AgentID, reason), "orchestrator")
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
	} else {
		// An identical request is already pending — usually a command that was
		// both executed and printed. Dropping it is right, but the other
		// swallowed-request paths leave a trail, so this one does too.
		o.runner.auditLog("spawn.duplicate", map[string]any{
			"threadId": string(threadID), "requester": string(call.AgentID), "name": name,
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
		o.runner.notebookFor(threadID).Append("notes", fmt.Sprintf("No pending spawn request named %q.", strings.TrimSpace(call.Name)), "orchestrator")
		o.runner.auditLog("spawn.unknown", map[string]any{"threadId": string(threadID), "name": call.Name})
		return
	}
	if call.Kind == toolbox.KindReject {
		o.runner.notebookFor(threadID).Append("notes", fmt.Sprintf("Rejected spawn request %q from %s.", req.Name, req.RequesterID), "orchestrator")
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
		call.Model, call.Lane, call.To, strings.Join(call.DependsOn, ","),
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

// drainPeerMessages empties the runtime inboxes one lead's pass should weigh
// into prompt lines. A lead's own messages arrive as direct lines; messages to
// subagents become relay lines so the lead can pass them on. Other leads'
// inboxes are left for their own passes. Draining keeps a prompt from repeating
// messages a previous pass already saw.
//
// An agent that has not started yet does not come through here: its inbox is
// drained straight into its prompt when it starts (runTask), so waiting agents
// hear from peers directly. A message to an agent that is *running* has no
// channel into a one-shot harness process — it waits here for the next pass and
// is relayed by the lead, by design.
func (o *Orchestrator) drainPeerMessages(state *domain.ThreadState, lead contracts.AgentID) string {
	if o.runner.Mailbox() == nil {
		return ""
	}
	var b strings.Builder
	for _, id := range state.AgentOrder {
		agent := state.Agents[id]
		// A pending agent has not started: its inbox is drained straight
		// into its first prompt when its work starts (teammateMessages in
		// runTask), so draining it here would steal that delivery. This
		// makes the skip local rather than relying on Dispatch and Run being
		// adjacent: Run ends every dispatched agent terminal before the next
		// pass, so no turn ends with a pending agent holding messages.
		if agent.Status == contracts.AgentPending {
			continue
		}
		if isLeadID(id) && id != lead {
			continue
		}
		for _, msg := range o.runner.Mailbox().Drain(state.ID, id) {
			// Parentage marks the team lead, and the roster can hold agents
			// dispatched without one, so leads are recognized by id.
			if isLeadID(id) {
				fmt.Fprintf(&b, "- from %s: %s\n", msg.From, msg.Body)
			} else {
				fmt.Fprintf(&b, "- for %s from %s: %s\n", agent.Name, msg.From, msg.Body)
			}
		}
	}
	return b.String()
}

// turn is one user turn as its lead team sees it.
type turn struct {
	threadID contracts.ThreadID
	leads    []contracts.AgentID
	role     contracts.Role
	message  string
}

// RunTurn executes one user turn end to end. The thread's team of leads takes a
// pass each round, in order: a lead's dispatches run before the next lead
// speaks, so its peer message reaches the next lead in the same round. The
// round loop repeats while work is dispatched, steering or a spawn decision is
// pending, or a lead has mail; it ends after the pass budget regardless. With
// one lead this is the classic single-orchestrator loop.
func (o *Orchestrator) RunTurn(ctx context.Context, threadID contracts.ThreadID, userMessage string) (err error) {
	role, ok := o.runner.roles.Get(contracts.RoleOrchestrator)
	if !ok {
		return errors.New("orchestrator role is not registered")
	}
	state, err := o.runner.engine.State(ctx, threadID)
	if err != nil {
		return err
	}

	t := turn{threadID: threadID, role: role, message: userMessage, leads: make([]contracts.AgentID, state.LeadCount())}
	seed := make([]contracts.DispatchTask, 0, len(t.leads))
	for i := range t.leads {
		t.leads[i] = leadAgentID(i)
		seed = append(seed, contracts.DispatchTask{
			AgentID: t.leads[i],
			Name:    leadName(i),
			Role:    contracts.RoleOrchestrator,
			Task:    userMessage,
		})
	}
	if err := o.runner.Dispatch(ctx, threadID, seed); err != nil {
		return err
	}
	for _, id := range t.leads {
		o.runner.setStatus(ctx, threadID, id, contracts.AgentRunning, "")
	}

	// Whatever goes wrong mid-turn — failure, engine errors, or a stop landing
	// between passes — every lead owes the thread a terminal status.
	// setStatus detaches from the turn context so a cancelled turn can still
	// record it.
	defer func() {
		if err == nil {
			return
		}
		finishCtx := context.WithoutCancel(ctx)
		for _, id := range t.leads {
			if ctx.Err() != nil {
				o.runner.setStatus(finishCtx, threadID, id, contracts.AgentCancelled, "turn stopped")
			} else {
				o.runner.setStatus(finishCtx, threadID, id, contracts.AgentFailed, err.Error())
			}
		}
	}()

	// Pending spawn requests never outlive their turn, whatever the exit path.
	defer o.runner.spawns.clear(threadID)

	var finalText string
	var dispatched []contracts.DispatchTask
	for pass := 0; pass < o.runner.maxPasses(); pass++ {
		// Steering is drained once per round so every lead weighs it, not just
		// the first one to speak.
		steering := o.runner.DrainSteering(threadID)
		dispatchedThisRound := false
		for i := range t.leads {
			text, tasks, err := o.runLeadPass(ctx, t, i, pass, steering)
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) != "" {
				finalText = text
			}
			if len(tasks) > 0 {
				dispatchedThisRound = true
				dispatched = append(dispatched, tasks...)
			}
		}
		// A round that dispatched nothing still earns another round if the user
		// steered while it was running, a spawn request is still undecided, or
		// a peer is waiting on a lead's reply.
		if !dispatchedThisRound && !o.runner.HasSteering(threadID) &&
			len(o.runner.spawns.list(threadID)) == 0 && !o.leadsHaveMail(t) {
			break
		}
	}

	// Late peer messages have no next pass to land in. Drain once more so they
	// surface in the final summary instead of being lost.
	if state, err := o.runner.engine.State(ctx, threadID); err == nil {
		var late strings.Builder
		for _, id := range t.leads {
			late.WriteString(o.drainPeerMessages(state, id))
		}
		if lateText := late.String(); lateText != "" {
			if strings.TrimSpace(finalText) != "" {
				finalText += "\n\n"
			}
			finalText += "Peer messages received after the final pass:\n" + lateText
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

	for _, id := range t.leads {
		o.runner.setStatus(ctx, threadID, id, contracts.AgentSucceeded, "")
	}
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

// runLeadPass runs one pass of lead i and executes whatever that pass
// dispatched. It returns the lead's text and the tasks it dispatched.
func (o *Orchestrator) runLeadPass(ctx context.Context, t turn, i, pass int, steering []string) (string, []contracts.DispatchTask, error) {
	orchID := t.leads[i]
	// Calls that arrived while the previous wave ran — a subagent proposing a
	// spawn, a late note, a printed peer message — fold in before the prompt is
	// built so the lead sees them this pass rather than the next.
	carried := o.toolTasks(ctx, t.threadID, orchID, nil, nil)
	inherited, _ := o.runner.inherited.Render()
	state, err := o.runner.engine.State(ctx, t.threadID)
	if err != nil {
		return "", nil, err
	}
	// Peer messages that arrived while work was running are weighed here too:
	// the lead's own arrive directly, the rest as relay lines.
	peerMessages := o.drainPeerMessages(state, orchID)
	// Auto mode gets the menu it may pick from; otherwise the prompt stays
	// as before.
	runtimeChoices := ""
	if o.runner.autoModeFor(ctx, t.threadID) {
		runtimeChoices = o.runner.runtimeChoices()
	}
	peerMailbox := o.runner.peerMailboxFor(ctx, t.threadID)
	prompt := BuildOrchestratorPrompt(OrchestratorPrompt{
		Role:           t.role,
		UserMessage:    t.message,
		Inherited:      inherited,
		Shared:         o.runner.notebookFor(t.threadID).Render(),
		Roster:         o.runner.Roster(state),
		PendingSpawns:  renderPendingSpawns(state, o.runner.spawns.list(t.threadID)),
		PeerMessages:   peerMessages,
		PeerMailbox:    peerMailbox,
		Subthreads:     subthreadsOffered(state, peerMailbox),
		Team:           teamBrief(t.leads, i, peerMailbox),
		Pass:           pass,
		Steering:       steering,
		RuntimeChoices: runtimeChoices,
	})

	text, calls, err := o.runner.runOrchestratorPass(ctx, t.threadID, i, orchID, t.role, prompt, pass)
	if err != nil {
		return "", nil, err
	}

	// Work can arrive two ways: the tool command the agent runs, or the legacy
	// text protocol. toolTasks seeds its result with the protocol dispatches so
	// a same-pass message can address them; the returned list is therefore the
	// whole pass's task set, and the carried tasks join it under names the drain
	// was told are already taken.
	claimed := make(map[string]bool, len(carried))
	for _, c := range carried {
		claimed[c.Name] = true
	}
	used := o.threadNameSet(ctx, t.threadID)
	protocol := ToDispatchTasks(orchID, Dispatches(calls), func(name string) bool { return used[name] || claimed[name] })
	tasks := o.toolTasks(ctx, t.threadID, orchID, protocol, claimed)
	tasks = append(tasks, carried...)
	if len(tasks) == 0 {
		return text, nil, nil
	}
	if err := o.runner.Dispatch(ctx, t.threadID, tasks); err != nil {
		return "", nil, err
	}
	if err := o.runner.Run(ctx, t.threadID, tasks); err != nil {
		return "", nil, err
	}
	return text, tasks, nil
}

// leadsHaveMail reports whether a peer wrote to a lead since that lead's last
// prompt, so the team owes another round. A single lead never waits on mail:
// its late messages surface in the final summary, as they always have.
func (o *Orchestrator) leadsHaveMail(t turn) bool {
	if len(t.leads) < 2 || o.runner.Mailbox() == nil {
		return false
	}
	for _, id := range t.leads {
		if len(o.runner.Mailbox().Inbox(t.threadID, id)) > 0 {
			return true
		}
	}
	return false
}

// subthreadsOffered reports whether a lead may be offered the subthread tool: a
// top-level thread with peer messaging on and a slot left. The daemon refuses a
// call past the limit, so offering the tool there would only earn an error.
func subthreadsOffered(state *domain.ThreadState, peerMailbox bool) bool {
	return peerMailbox && state.ParentThreadID == "" && len(state.Subthreads) < contracts.MaxSubthreads
}

// teamBrief tells lead i how its team is organized and what it must agree on
// before dispatching. A single lead gets no brief: its prompt is unchanged.
func teamBrief(leads []contracts.AgentID, i int, peerMailbox bool) string {
	if len(leads) < 2 {
		return ""
	}
	peers := make([]string, 0, len(leads)-1)
	for j := range leads {
		if j != i {
			peers = append(peers, leadName(j))
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, lead %d of %d on this thread's orchestrator team. Your peers: %s.\n",
		leadName(i), i+1, len(leads), strings.Join(peers, ", "))
	b.WriteString("The team decides together. Before anyone dispatches, agree with your peers on the plan, the task list, and how many agents to spawn. ")
	if peerMailbox {
		b.WriteString("Discuss it with them by direct message (see below); their replies reach you in a later pass. ")
	} else {
		b.WriteString("Peer messaging is off on this thread, so the team can only coordinate through shared notes. ")
	}
	b.WriteString("Once the team agrees, record the plan with \"$AT_TOOL\" tool note --text \"PLAN: <plan, task list, agent count>\" and dispatch. Do not dispatch work the team has not agreed on.")
	return b.String()
}
