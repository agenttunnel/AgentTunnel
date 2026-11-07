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

// toolTasks turns the tool calls an agent posted during a pass into work items,
// folding notes into the shared notebook and forwarding peer messages.
// `pending` lists dispatches already minted for this pass (e.g. protocol
// dispatches) that a message may address before the state knows them.
func (o *Orchestrator) toolTasks(ctx context.Context, threadID contracts.ThreadID, orchID contracts.AgentID, pending []contracts.DispatchTask) []contracts.DispatchTask {
	var tasks []contracts.DispatchTask
	pending = append append placeholder
	seen := map[string]bool{}
	for i, call := range o.runner.DrainTools(threadID) {
		// A command can arrive twice when an agent executes it and also prints
		// the command text. The parser posts the printed form; ignore the echo.
		key := toolCallKey(call)
		if seen[key] {
			continue
		}
		seen[key] = true
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

		case toolbox.KindMessage:
			// A rejection (peer mailbox off, unknown recipient) is already
			// audited inside PostMessage; there is no task to make of it. The
			// dispatches minted earlier in this same drain act as extra
			// resolution candidates, so a lead can dispatch an agent and then
			// message it while it still waits on dependencies.
			if err := o.runner.postMessage(ctx, threadID, call.AgentID, call.To, call.Text, tasks); err != nil {
				// The turn survives a rejected message, but the sender — and
				// the model behind it — must see why, or it will assume the
				// message arrived.
				line := "tool message failed: " + err.Error()
				_ = o.runner.logs.Append(contracts.LogEntry{AgentID: call.AgentID, Kind: contracts.LogError, Text: line, At: o.runner.clock()})
				o.runner.publishOutput(threadID, call.AgentID, contracts.LogError, line)
			}

		case toolbox.KindDispatch:
			if strings.TrimSpace(call.Task) == "" {
				continue
			}
			name := strings.TrimSpace(call.Name)
			if name == "" {
				name = fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), i)
			}
			name = safeName(name)
			role := call.Role
			if role == "" {
				role = contracts.RoleExecutor
			}
			// Agent ids must be unique per dispatch: the same task name can be
			// dispatched again on a later pass.
			id := fmt.Sprintf("agent-%s-%d", name, o.seq.Add(1))
			tasks = append(tasks, contracts.DispatchTask{
				AgentID:       contracts.AgentID(id),
				ParentAgentID: orchID,
				Name:          name,
				Role:          role,
				Task:          call.Task,
				Harness:       contracts.Harness(call.Harness),
				Model:         call.Model,
				Pipeline:      contracts.PipelineID(call.Lane),
				DependsOn:     call.DependsOn,
			})
		}
	}
	return tasks
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

// drainPeerMessages empties every agent's runtime inbox into prompt lines:
// messages to the lead arrive as their own, messages to subagents become relay
// lines so the lead can pass them on. Draining keeps a prompt from repeating
// messages a previous pass already saw.
//
// An agent that has not started yet does not come through here: its inbox is
// drained straight into its prompt when it starts (runTask), so waiting agents
// hear from peers directly. A message to an agent that is *running* has no
// channel into a one-shot harness process — it waits here for the next pass and
// is relayed by the lead, by design.
func (o *Orchestrator) drainPeerMessages(state *domain.ThreadState) string {
	if o.runner.Mailbox() == nil {
		return ""
	}
	var b strings.Builder
	for _, id := range state.AgentOrder {
		agent := state.Agents[id]
		for _, msg := range o.runner.Mailbox().Drain(id) {
			// Compare against the lead's id rather than IsOrchestrator():
			// parentage marks the team lead, and the roster can hold agents
			// dispatched without one.
			if id == orchestratorAgentID {
				fmt.Fprintf(&b, "- from %s: %s\n", msg.From, msg.Body)
			} else {
				fmt.Fprintf(&b, "- for %s from %s: %s\n", agent.Name, msg.From, msg.Body)
			}
		}
	}
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

	var finalText string
	var dispatched []contracts.DispatchTask
	for pass := 0; pass < o.runner.maxPasses(); pass++ {
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
		// Peer messages that arrived while work was running are weighed here
		// too: the lead's own arrive directly, the rest as relay lines.
		peerMessages := o.drainPeerMessages(state)
		// Auto mode gets the menu it may pick from; otherwise the prompt stays
		// as before.
		runtimeChoices := ""
		if o.runner.autoModeFor(ctx, threadID) {
			runtimeChoices = o.runner.runtimeChoices()
		}
		prompt := BuildOrchestratorPrompt(role, userMessage, inherited, o.runner.notebook.Render(), o.runner.Roster(state), pass, steering, peerMessages, o.runner.peerMailboxFor(ctx, threadID), runtimeChoices)

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
		tasks = append(tasks, o.toolTasks(ctx, threadID, orchID)...)
		// A pass that dispatched nothing still earns another pass if the user
		// steered while it was running.
		if len(tasks) == 0 && !o.runner.HasSteering(threadID) {
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

	// Late peer messages have no next pass to land in. Drain once more so they
	// surface in the final summary instead of being lost.
	if state, err := o.runner.engine.State(ctx, threadID); err == nil {
		if late := o.drainPeerMessages(state); late != "" {
			if strings.TrimSpace(finalText) != "" {
				finalText += "\n\n"
			}
			finalText += "Peer messages received after the final pass:\n" + late
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
