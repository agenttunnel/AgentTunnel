package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// ReviewCall applies one review call at once. A reviewer opens a thread on a diff
// range, any agent replies on one, and the reviewer that opened a thread resolves
// or reopens it. Who may do what is the decider's call; this only shapes the
// command. It returns the review thread's id.
func (r *Runner) ReviewCall(ctx context.Context, threadID contracts.ThreadID, agentID contracts.AgentID, call toolbox.Call) (string, error) {
	now := r.clock()
	var (
		id  = call.Thread
		cmd contracts.Command
	)
	switch call.Kind {
	case toolbox.KindReviewComment:
		id = "review-" + r.nextID()
		cmd = contracts.Command{Type: contracts.CmdReviewThreadOpen, ReviewOpen: &contracts.ReviewThreadOpened{
			ReviewCommentAdded: contracts.ReviewCommentAdded{
				ID: id, File: call.File, Line: call.Line, EndLine: call.EndLine, Side: call.Side,
				Body: call.Text, AuthorID: agentID, CreatedAt: now,
			},
		}}
	case toolbox.KindReviewReply:
		cmd = contracts.Command{Type: contracts.CmdReviewThreadReply, ReviewReply: &contracts.ReviewThreadReplied{
			ThreadID: call.Thread, AuthorID: agentID, Body: call.Text, CreatedAt: now,
		}}
	case toolbox.KindReviewResolve:
		status := contracts.ReviewStatusResolved
		if call.Reopen {
			status = contracts.ReviewStatusOpen
		}
		cmd = contracts.Command{Type: contracts.CmdReviewThreadStatus, ReviewStatus: &contracts.ReviewThreadStatusChanged{
			ThreadID: call.Thread, AuthorID: agentID, Status: status, Body: call.Text, CreatedAt: now,
		}}
	default:
		return "", fmt.Errorf("%w: %q is not a review call", domain.ErrInvalidCommand, call.Kind)
	}
	cmd.ID = r.nextID()
	cmd.ThreadID = threadID
	if err := r.engine.Dispatch(ctx, cmd); err != nil {
		return "", err
	}
	return id, nil
}

// applyPrintedReview applies a review call an agent printed instead of running.
// Harnesses that run the command also print it, so a call the thread already
// holds is skipped rather than applied twice.
func (r *Runner) applyPrintedReview(ctx context.Context, threadID contracts.ThreadID, agentID contracts.AgentID, call toolbox.Call) {
	if st, err := r.engine.State(ctx, threadID); err == nil && isReviewEcho(st, agentID, call) {
		return
	}
	line := "tool " + string(call.Kind)
	if _, err := r.ReviewCall(ctx, threadID, agentID, call); err != nil {
		line = "tool " + string(call.Kind) + " failed: " + err.Error()
		_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: agentID, Kind: contracts.LogError, Text: line, At: r.clock()})
		r.publishOutput(threadID, agentID, contracts.LogError, line)
		return
	}
	_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: agentID, Kind: contracts.LogTool, Text: line, At: r.clock()})
	r.publishOutput(threadID, agentID, contracts.LogTool, line)
}

// isReviewEcho reports whether the thread already holds the effect of a printed
// review call: the same comment, or a reply or resolution with the same text by
// the same agent.
func isReviewEcho(st *domain.ThreadState, agentID contracts.AgentID, call toolbox.Call) bool {
	if call.Kind == toolbox.KindReviewComment {
		// The projector reads an unset end line as the start line.
		end := call.EndLine
		if end == 0 {
			end = call.Line
		}
		for _, t := range st.ReviewThreads {
			if t.AuthorID == agentID && t.File == call.File && t.Line == call.Line &&
				t.EndLine == end && t.Side == call.Side && t.Body == call.Text {
				return true
			}
		}
		return false
	}
	for _, t := range st.ReviewThreads {
		if t.ID != call.Thread {
			continue
		}
		replied := false
		if n := len(t.Replies); n > 0 {
			last := t.Replies[n-1]
			replied = last.AuthorID == agentID && last.Body == call.Text
		}
		if call.Kind == toolbox.KindReviewReply {
			return replied
		}
		wantStatus := contracts.ReviewStatusResolved
		if call.Reopen {
			wantStatus = contracts.ReviewStatusOpen
		}
		return t.Status == wantStatus && (strings.TrimSpace(call.Text) == "" || replied)
	}
	return false
}

// openReviewsFor renders the open review threads a subagent must answer. A
// reviewer is not handed them: its own threads reach it through its re-check.
func (r *Runner) openReviewsFor(ctx context.Context, threadID contracts.ThreadID, role contracts.RoleID) string {
	if role == contracts.RoleReviewer || r.engine == nil {
		return ""
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil {
		return ""
	}
	return renderOpenReviews(st)
}

// renderOpenReviews lists the thread's open review threads as the block a
// subagent answers, empty when none are open.
func renderOpenReviews(st *domain.ThreadState) string {
	open := domain.OpenReviewThreads(st)
	if len(open) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Open review threads\n")
	b.WriteString("A reviewer left these comments on the code. Address each one that applies, then answer it with:\n\n")
	b.WriteString("  \"$AT_TOOL\" tool review-reply --thread <id> --text \"<what you changed, or why not>\"\n\n")
	for _, t := range open {
		fmt.Fprintf(&b, "- %s %s (from %s): %s\n", t.ID, reviewLocation(t.File, t.Line, t.EndLine, t.Side), agentLabel(st, t.AuthorID), indentBody(t.Body))
	}
	return strings.TrimRight(b.String(), "\n")
}

// reviewRecheckTask is the task a reviewer is re-dispatched with: each of its own
// open threads that drew a reply it has not yet weighed, with the full reply
// history. It is empty when there is nothing to re-check.
func reviewRecheckTask(st *domain.ThreadState, reviewer contracts.AgentID) string {
	var threads []domain.ReviewThread
	for _, t := range domain.OpenReviewThreads(st) {
		if t.AuthorID != reviewer || len(t.Replies) == 0 {
			continue
		}
		if t.Replies[len(t.Replies)-1].AuthorID == reviewer {
			continue
		}
		threads = append(threads, t)
	}
	if len(threads) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("This is a review re-check. Executors answered the review threads you opened. ")
	b.WriteString("For each thread below, read the reply and the code as it is now. If the change satisfies your comment, close it with:\n\n")
	b.WriteString("  \"$AT_TOOL\" tool review-resolve --thread <id> [--text \"<note>\"]\n\n")
	b.WriteString("If it does not, say what is still missing with:\n\n")
	b.WriteString("  \"$AT_TOOL\" tool review-reply --thread <id> --text \"<what is still missing>\"\n\n")
	b.WriteString("Post any new finding as a thread with \"$AT_TOOL\" tool review-comment --file <file> --line <n> [--end-line <n>] [--side old|new] --text \"<finding>\".\n")
	for _, t := range threads {
		fmt.Fprintf(&b, "\nThread %s at %s. Your comment: %s\n", t.ID, reviewLocation(t.File, t.Line, t.EndLine, t.Side), indentBody(t.Body))
		for _, rp := range t.Replies {
			fmt.Fprintf(&b, "  - %s: %s\n", agentLabel(st, rp.AuthorID), indentBody(rp.Body))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// recheckReviews re-dispatches each reviewer whose threads drew a reply it has
// not weighed. It runs once per turn, after the pass loop, under the turn's
// context, and reuses the reviewer's agent id and parent so the roster keeps one
// entry per reviewer. A cancelled turn skips it.
func (r *Runner) recheckReviews(ctx context.Context, threadID contracts.ThreadID) error {
	if ctx.Err() != nil {
		return nil
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil {
		return err
	}
	var tasks []contracts.DispatchTask
	for _, id := range st.AgentOrder {
		a := st.Agents[id]
		if a.Role != contracts.RoleReviewer {
			continue
		}
		task := reviewRecheckTask(st, id)
		if task == "" {
			continue
		}
		tasks = append(tasks, contracts.DispatchTask{
			AgentID:       id,
			ParentAgentID: a.ParentAgentID,
			Name:          a.Name,
			Role:          a.Role,
			Task:          task,
			Harness:       a.Harness,
			Model:         a.Model,
			Pipeline:      a.PipelineID,
		})
	}
	if len(tasks) == 0 {
		return nil
	}
	if err := r.Dispatch(ctx, threadID, tasks); err != nil {
		return err
	}
	return r.Run(ctx, threadID, tasks)
}

// reviewLocation renders a comment's anchor as file:line or file:start-end, with
// removed lines marked, since their numbers are old-file numbers.
func reviewLocation(file string, line, endLine int, side string) string {
	loc := fmt.Sprintf("%s:%d", file, line)
	if endLine > line {
		loc = fmt.Sprintf("%s:%d-%d", file, line, endLine)
	}
	if side == "old" {
		loc += " (removed lines)"
	}
	return loc
}

// agentLabel names an agent for a prompt: its roster name, or its id when unnamed.
func agentLabel(st *domain.ThreadState, id contracts.AgentID) string {
	if a, ok := st.Agents[id]; ok && a.Name != "" {
		return a.Name
	}
	return string(id)
}

// indentBody keeps a multi-line body inside its list item.
func indentBody(body string) string {
	return strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n  ")
}
