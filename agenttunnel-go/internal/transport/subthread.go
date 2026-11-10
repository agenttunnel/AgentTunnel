package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// subthreadSender labels a subthread's report in the parent's mailbox. It is not
// an agent id: the report comes from a thread, not from one of its agents.
const subthreadSender = contracts.AgentID("subthread")

// startSubthread opens a new thread for unrelated work the parent's lead wants
// kept in its own context, and runs the task as that thread's first turn. The
// subthread is a thread of its own with its own orchestrator, filed under the
// parent's project. The turn runs in the background; the parent's tool call
// returns as soon as the subthread exists.
func (d *Dispatcher) startSubthread(ctx context.Context, parent contracts.ThreadID, task string) (contracts.ThreadID, error) {
	if d.orch == nil || d.runner == nil {
		return "", errors.New("subthreads are unavailable")
	}
	if strings.TrimSpace(task) == "" {
		return "", fmt.Errorf("%w: subthread task is required", domain.ErrInvalidCommand)
	}
	st, err := d.engine.State(ctx, parent)
	if err != nil {
		return "", err
	}
	if !st.Exists {
		return "", fmt.Errorf("%w: thread %q", domain.ErrThreadNotFound, parent)
	}
	// The child shares the parent's worktree, so the parent's must be live first:
	// re-cut if it vanished. If that cannot be done, no subthread is created.
	if err := d.ensureWorktree(ctx, parent); err != nil {
		return "", err
	}
	if st, err = d.engine.State(ctx, parent); err != nil {
		return "", err
	}

	// The spawn is validated against the parent first, so a refused subthread
	// creates nothing.
	child := d.newThreadID()
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdSubthreadSpawn, ThreadID: parent,
		Subthread: &contracts.SubthreadSpawned{ThreadID: child},
	}); err != nil {
		return "", err
	}
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdThreadCreate, ThreadID: child,
		ProjectID: st.ProjectID, ParentThreadID: parent, Worktree: inheritWorktree(st.Worktree),
	}); err != nil {
		return "", err
	}
	// The first turn is what materializes the subthread in its own log.
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdTurnStart, ThreadID: child, Text: task,
	}); err != nil {
		return "", err
	}

	// The turn outlives the tool call that started it, so it must not inherit
	// that call's cancellation. Claiming the thread's turn slot lets the user
	// stop the subthread like any other thread.
	turnCtx, done, err := d.beginTurn(context.WithoutCancel(ctx), child)
	if err != nil {
		return "", err
	}
	go func() {
		defer done()
		// The same gate as a user turn: if the shared directory vanished since the
		// spawn, the child re-follows the parent's live worktree before any agent runs.
		runErr := d.ensureWorktree(turnCtx, child)
		if runErr == nil {
			runErr = d.orch.RunTurn(turnCtx, child, task)
		}
		d.reportSubthread(turnCtx, parent, child, runErr)
	}()
	return child, nil
}

// reportSubthread hands a finished subthread's outcome to the parent's lead. The
// summary is the subthread's final orchestrator message; a turn that failed or
// was stopped reports that instead. The message goes through the parent's peer
// mailbox, so the lead reads it on the parent's next pass.
func (d *Dispatcher) reportSubthread(turnCtx context.Context, parent, child contracts.ThreadID, runErr error) {
	ctx := context.WithoutCancel(turnCtx)
	title := string(child)
	summary := ""
	if st, err := d.engine.State(ctx, child); err == nil {
		if st.Title != "" && st.Title != contracts.DefaultThreadTitle {
			title = st.Title
		}
		for i := len(st.Messages) - 1; i >= 0; i-- {
			if st.Messages[i].Author == "orchestrator" {
				summary = st.Messages[i].Text
				break
			}
		}
	}

	var body string
	switch {
	case runErr != nil && turnCtx.Err() != nil:
		body = fmt.Sprintf("Subthread %q was stopped before it finished.", title)
	case runErr != nil:
		body = fmt.Sprintf("Subthread %q failed: %s", title, runErr.Error())
	case strings.TrimSpace(summary) == "":
		body = fmt.Sprintf("Subthread %q finished without a summary.", title)
	default:
		body = fmt.Sprintf("Subthread %q finished. Its summary:\n%s", title, summary)
	}

	// The lead reads its mailbox only on a pass. When the parent's turn is already
	// over, the report would otherwise sit there unseen, so it always lands in the
	// transcript too, where the user can read it.
	if err := d.runner.PostMessage(ctx, parent, subthreadSender, "orchestrator", body); err != nil {
		// The lead has no inbox to read from (peer messaging was turned off after
		// the subthread started).
		d.auditLog("subthread.report", map[string]any{
			"threadId": string(parent), "subthreadId": string(child), "error": err.Error(),
		})
	}
	_ = d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdMessageAppend, ThreadID: parent,
		Message: &contracts.MessageSent{MessageID: d.nextID(), Author: string(subthreadSender), Text: body},
	})
}
