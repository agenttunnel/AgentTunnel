package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
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
// folding notes into the shared notebook.
func (o *Orchestrator) toolTasks(threadID contracts.ThreadID, orchID contracts.AgentID) []contracts.DispatchTask {
	var tasks []contracts.DispatchTask
	for i, call := range o.runner.DrainTools(threadID) {
		switch call.Kind {
		case toolbox.KindNote, toolbox.KindProgress, toolbox.KindDone:
			if strings.TrimSpace(call.Text) != "" {
				o.runner.notebook.Append("notes", call.Text, string(call.AgentID))
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

// RunTurn executes one user turn end to end.
func (o *Orchestrator) RunTurn(ctx context.Context, threadID contracts.ThreadID, userMessage string) error {
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

	var finalText string
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
		prompt := BuildOrchestratorPrompt(role, userMessage, inherited, o.runner.notebook.Render(), o.runner.Roster(state), pass, steering)

		text, calls, err := o.runner.runOrchestratorPass(ctx, threadID, orchID, role, prompt, pass)
		if err != nil {
			o.runner.setStatus(ctx, threadID, orchID, contracts.AgentFailed, err.Error())
			return err
		}
		if strings.TrimSpace(text) != "" {
			finalText = text
		}

		// Work can arrive two ways: the tool command the agent runs, or the
		// legacy text protocol.
		tasks := ToDispatchTasks(orchID, Dispatches(calls))
		tasks = append(tasks, o.toolTasks(threadID, orchID)...)
		// A pass that dispatched nothing still earns another pass if the user
		// steered while it was running.
		if len(tasks) == 0 && !o.runner.HasSteering(threadID) {
			break
		}
		if len(tasks) == 0 {
			continue
		}
		if err := o.runner.Dispatch(ctx, threadID, tasks); err != nil {
			return err
		}
		if err := o.runner.Run(ctx, threadID, tasks); err != nil {
			return err
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
