package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// The scheduling tool kinds. A thread that a schedule triggered can read the
// state and results of the threads it was triggered by, and relaunch one with a
// new prompt when the result was not enough. The kinds ride the same tool inbox
// as dispatch and message: an agent invokes them mid-pass and the orchestrator
// drain executes them at the pass boundary. The target thread rides Call.Thread
// (as review calls do) and the relaunch prompt rides Call.Text.
const (
	KindThreadRelaunch toolbox.Kind = "thread-relaunch"
	KindThreadResults  toolbox.Kind = "thread-results"
)

// Rejections the caller can present to the agent that asked. Reading and
// relaunching share the ownership gate, so they share the errors too.
var (
	// ErrThreadNotYours refuses a target that is neither in the caller's
	// TriggeredFrom set nor controlled by the caller.
	ErrThreadNotYours = errors.New("thread is not one this thread was triggered by or controls")
	// ErrThreadBusy refuses a relaunch of a thread with work still running.
	ErrThreadBusy = errors.New("thread still has work running")
)

// Bounds for the thread-results render, in the master thread read's spirit: a
// bounded block, not a whole transcript.
const (
	threadResultsMessages    = 8
	threadResultsMessageClip = 400
)

// mayDrive reports whether the caller thread may read or relaunch the target: a
// dependency that fired the caller's last scheduled turn, or a thread already
// controlled by the caller — a relaunched dependency stays relaunchable on
// later triggers.
func mayDrive(caller, target *domain.ThreadState) bool {
	for _, dep := range caller.TriggeredFrom {
		if dep == target.ID {
			return true
		}
	}
	return target.ControlledBy == caller.ID
}

// canDrive resolves the target under the shared gate: the call must name a
// thread, a thread cannot drive itself, and the caller must own it through
// TriggeredFrom or control. It returns the target's state for the caller to
// act on.
func (r *Runner) canDrive(ctx context.Context, caller, target contracts.ThreadID) (*domain.ThreadState, error) {
	if target == "" {
		return nil, fmt.Errorf("%w: target thread is required", domain.ErrInvalidCommand)
	}
	if target == caller {
		return nil, fmt.Errorf("%w: a thread cannot drive itself", domain.ErrInvalidCommand)
	}
	callerState, err := r.engine.State(ctx, caller)
	if err != nil {
		return nil, err
	}
	if !callerState.Exists {
		return nil, fmt.Errorf("%w: %s", domain.ErrThreadNotFound, caller)
	}
	targetState, err := r.engine.State(ctx, target)
	if err != nil {
		return nil, err
	}
	if !targetState.Exists {
		return nil, fmt.Errorf("%w: %s", domain.ErrThreadNotFound, target)
	}
	if !mayDrive(callerState, targetState) {
		return nil, fmt.Errorf("%w: %s", ErrThreadNotYours, target)
	}
	return targetState, nil
}

// RelaunchThread starts a new turn on the target with the caller's prompt. It
// first claims control — the turn is then authored by the controller in the
// transcript, never "user", so the read model records who relaunched the
// thread and per-turn spend stays honest — and runs the target's turn
// through the runtime's turn machinery, so the relaunched thread actually
// executes the prompt rather than hanging in an open, agentless turn.
// Control is scoped to the relaunched turn: it is released when the turn
// ends, whatever the outcome, because a claim that outlived its turn would
// misattribute every later user- or scheduler-started turn to the
// controller. Nothing is lost by the release — the durable permission to
// relaunch again rides on the caller's TriggeredFrom record, which is never
// cleared. Refusals name the reason and are shown to the agent that asked.
func (r *Runner) RelaunchThread(ctx context.Context, caller contracts.ThreadID, agent contracts.AgentID, call toolbox.Call) error {
	prompt := strings.TrimSpace(call.Text)
	if prompt == "" {
		return fmt.Errorf("%w: a relaunch needs a prompt", domain.ErrInvalidCommand)
	}
	targetState, err := r.canDrive(ctx, caller, contracts.ThreadID(strings.TrimSpace(call.Thread)))
	if err != nil {
		return err
	}
	// A busy target would fold two runs into one transcript; the caller waits
	// for its current turn to finish instead. The check is the read model's
	// agent statuses — the launch path below re-claims the thread's turn slot
	// before anything is recorded, which is the authority while a turn is in
	// flight.
	for id, a := range targetState.Agents {
		if !a.Status.Terminal() {
			return fmt.Errorf("%w: agent %q is still %s", ErrThreadBusy, id, a.Status)
		}
	}
	target := targetState.ID
	turnText := relaunchPrompt(caller, prompt)
	// One text serves both the recorded opener and the turn the target runs,
	// so priorMessages finds the turn boundary and the lead is not handed
	// its own opener a second time as history.
	record := func() error {
		if err := r.engine.Dispatch(ctx, contracts.Command{
			ID:         r.nextID(),
			Type:       contracts.CmdThreadControl,
			ThreadID:   target,
			Controller: caller,
		}); err != nil {
			return err
		}
		return r.engine.Dispatch(ctx, contracts.Command{
			ID:       r.nextID(),
			Type:     contracts.CmdTurnStart,
			ThreadID: target,
			Text:     turnText,
		})
	}
	if r.launchTurn != nil {
		finish := func(turnCtx context.Context) { r.releaseThreadControl(turnCtx, target) }
		if err := r.launchTurn(ctx, target, turnText, record, finish); err != nil {
			return err
		}
	} else {
		// No transport launcher: record now and run the turn on this Runner's
		// own orchestrator, detached from the caller's turn context so the
		// relaunch outlives the turn that asked for it.
		if err := record(); err != nil {
			return err
		}
		go r.runBackgroundTurn(context.WithoutCancel(ctx), caller, target, turnText)
	}
	r.auditLog("thread.relaunch", map[string]any{
		"threadId": string(caller), "agentId": string(agent), "target": string(target),
	})
	line := fmt.Sprintf("→ relaunched %s; read its state with \"tool thread-results --thread %s\"", target, target)
	_ = r.logs.Append(caller, contracts.LogEntry{AgentID: agent, Kind: contracts.LogTool, Text: line, At: r.clock()})
	r.publishOutput(caller, agent, contracts.LogTool, line)
	return nil
}

// runBackgroundTurn runs one relaunched thread's turn on the Runner's own
// orchestrator — the fallback for a Runner with no transport launcher
// installed — and closes the relaunch out: errors are audited, and control is
// released once the turn has ended.
func (r *Runner) runBackgroundTurn(turnCtx context.Context, caller, target contracts.ThreadID, turnText string) {
	if err := r.background.RunTurn(turnCtx, target, turnText); err != nil && turnCtx.Err() == nil {
		r.auditLog("thread.relaunch.turn.failed", map[string]any{
			"threadId": string(target), "controller": string(caller), "error": err.Error(),
		})
	}
	r.releaseThreadControl(turnCtx, target)
}

// releaseThreadControl gives up the controller's claim on a thread once the
// turn it governed has ended, whatever the outcome. The dispatch is detached
// from the turn context so a stopped turn is still released.
func (r *Runner) releaseThreadControl(turnCtx context.Context, target contracts.ThreadID) {
	_ = r.engine.Dispatch(context.WithoutCancel(turnCtx), contracts.Command{
		ID:         r.nextID(),
		Type:       contracts.CmdThreadControl,
		ThreadID:   target,
		Controller: "",
	})
}

// relaunchPrompt carries the report-back instruction inside the turn the
// relaunched thread runs: the thread learns who relaunched it and how to
// answer. There is no cross-thread peer message today, so the final message is
// the report, and the controller reads it with the thread-results tool.
func relaunchPrompt(controller contracts.ThreadID, prompt string) string {
	return fmt.Sprintf("%s\n\n(This turn was relaunched by thread %s because your earlier result was not enough. "+
		"When you finish, put your outcome — and anything you still need — in your final message; "+
		"the relaunching thread reads it with its thread-results tool.)", prompt, controller)
}

// ThreadResults renders one dependency's state for the caller's agent: title,
// worktree, and the final messages, each on one clipped line. The read shares
// the relaunch gate; a running dependency is still readable — its transcript so
// far is exactly the point.
func (r *Runner) ThreadResults(ctx context.Context, caller contracts.ThreadID, agent contracts.AgentID, call toolbox.Call) (string, error) {
	targetState, err := r.canDrive(ctx, caller, contracts.ThreadID(strings.TrimSpace(call.Thread)))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "thread %s: %s\n", targetState.ID, clipTitle(oneLine(targetState.Title), threadResultsMessageClip))
	if targetState.Worktree == nil {
		b.WriteString("worktree: none (project checkout)\n")
	} else {
		fmt.Fprintf(&b, "worktree: %s (branch %s)\n", targetState.Worktree.Path, targetState.Worktree.Branch)
	}
	if n := len(targetState.Messages); n == 0 {
		b.WriteString("messages: (none yet)")
	} else {
		b.WriteString("messages:\n")
		from := max(0, n-threadResultsMessages)
		rows := make([]string, 0, n-from)
		for _, m := range targetState.Messages[from:] {
			rows = append(rows, fmt.Sprintf("- %s: %s", m.Author, clipTitle(oneLine(m.Text), threadResultsMessageClip)))
		}
		b.WriteString(strings.Join(rows, "\n"))
	}
	r.auditLog("thread.results", map[string]any{
		"threadId": string(caller), "agentId": string(agent), "target": string(targetState.ID),
	})
	return b.String(), nil
}

// RenderDependencyThreads lists the threads this one was triggered by, with
// titles, for the scheduling tool offer. Threads the thread already controls
// are covered by the offer's text; the gate accepts both.
func (r *Runner) RenderDependencyThreads(ctx context.Context, state *domain.ThreadState) string {
	if len(state.TriggeredFrom) == 0 {
		return ""
	}
	var b strings.Builder
	for _, dep := range state.TriggeredFrom {
		title := ""
		if st, err := r.engine.State(ctx, dep); err == nil && st.Exists {
			title = clipTitle(oneLine(st.Title), threadResultsMessageClip)
		}
		if title == "" {
			fmt.Fprintf(&b, "- %s\n", dep)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", dep, title)
		}
	}
	return b.String()
}
