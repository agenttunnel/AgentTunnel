package transport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
)

// Master conversations are durable engine threads owned by
// contracts.ThreadOwnerMaster. They hold no project and no turns: just the
// user's messages and the master's replies, so the home screen's chat history
// survives restarts the way work threads do.

// masterAuthorUser and masterAuthorMaster are the two transcript authors a
// master conversation records. Both are distinct from the plain "user" author
// so the projector never opens a turn for a master exchange (the AuthorStatus
// precedent); history.open can tell the sides apart without guessing from
// message order. The plain "user" author is still read back for one side, so
// logs recorded before the split authors replay identically.
const (
	masterAuthorUser   = contracts.AuthorMasterUser
	masterAuthorMaster = contracts.AuthorMaster
)

// MasterRuntimeView is the master chat's runtime for master.runtime.get and
// set. Harness, model, and variant are the stored override, where an empty
// field inherits the Orchestrator role's runtime; effective is what a master
// chat actually runs on today.
type MasterRuntimeView struct {
	Harness   string `json:"harness"`
	Model     string `json:"model"`
	Variant   string `json:"variant"`
	Effective struct {
		Harness string `json:"harness"`
		Model   string `json:"model"`
		Variant string `json:"variant"`
	} `json:"effective"`
}

// MasterThreadSummary is one row of master.history.list.
type MasterThreadSummary struct {
	ID        contracts.ThreadID `json:"id"`
	Title     string             `json:"title"`
	UpdatedAt time.Time          `json:"updatedAt"`
	Messages  int                `json:"messages"`
}

// MasterHistoryMessage is one exchange of a master conversation, oldest first.
// Role is "user" or "assistant".
type MasterHistoryMessage struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// MasterThreadDetail is master.history.open's payload: the conversation the
// home screen reopens.
type MasterThreadDetail struct {
	ID        contracts.ThreadID     `json:"id"`
	Title     string                 `json:"title"`
	UpdatedAt time.Time              `json:"updatedAt"`
	Messages  []MasterHistoryMessage `json:"messages"`
}

// masterThreadState loads a thread and requires it to be a master
// conversation: no project thread or subthread can be read or written as one.
func (d *Dispatcher) masterThreadState(ctx context.Context, id contracts.ThreadID) (*domain.ThreadState, error) {
	if d.engine == nil {
		return nil, errors.New("threads are unavailable")
	}
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return nil, err
	}
	if !st.Exists || st.Deleted {
		return nil, fmt.Errorf("thread %q does not exist", id)
	}
	if st.Owner.Normalized() != contracts.ThreadOwnerMaster {
		return nil, fmt.Errorf("thread %q is not a master conversation", id)
	}
	return st, nil
}

// refuseMasterConversation is the work-turn surface's mirror of
// masterThreadState: a master conversation is home-screen chat history — no
// worktree, no roster, no turns — so no agent may be dispatched into one. Work
// threads pass through untouched.
func refuseMasterConversation(st *domain.ThreadState, id contracts.ThreadID) error {
	if st.Owner.Normalized() == contracts.ThreadOwnerMaster {
		return fmt.Errorf("thread %q is a master conversation", id)
	}
	return nil
}

// refuseMasterTurn reads the thread and refuses it when it is a master
// conversation, for the agent-running surfaces that have no state in hand.
func (d *Dispatcher) refuseMasterTurn(ctx context.Context, id contracts.ThreadID) error {
	if d.engine == nil {
		return nil
	}
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return err
	}
	return refuseMasterConversation(st, id)
}

// sendMasterWithThread runs a durable master.send. It claims the
// conversation's busy slot — the same serialization work turns get through
// beginTurn — before reading history and recording the user message, so a
// concurrent send on the same conversation is refused before anything it says
// reaches the transcript, and neither prompt can miss the other's exchange.
// The slot is released after the reply is recorded. A reply that cannot be
// recorded degrades to a warning on the result: the exchange already ran, so
// the client keeps the answer and the correlation id instead of an error.
func (d *Dispatcher) sendMasterWithThread(ctx context.Context, threadID contracts.ThreadID, text string) (orchestrator.MasterResult, error) {
	var zero orchestrator.MasterResult
	if _, err := d.masterThreadState(ctx, threadID); err != nil {
		return zero, err
	}
	turnCtx, release, err := d.beginTurn(ctx, threadID)
	if err != nil {
		return zero, errors.New("a master reply is already running on this conversation; wait for it to finish")
	}
	defer release()
	// History is read under the claim, so an exchange a racing send recorded
	// before the slot freed cannot be missed.
	st, err := d.masterThreadState(turnCtx, threadID)
	if err != nil {
		return zero, err
	}
	// The user's message is durable before the model runs, so a failed turn
	// still leaves the transcript truthful.
	if err := d.recordMasterUserMessage(ctx, threadID, strings.TrimSpace(text)); err != nil {
		return zero, err
	}
	result, err := d.master.Send(turnCtx, text, masterHistory(st))
	if err != nil {
		return zero, err
	}
	d.runMaster(turnCtx, &result)
	result.MasterThreadID = threadID
	if err := d.recordMasterReply(ctx, threadID, &result); err != nil {
		result.Warning = "the reply could not be saved to this conversation: " + err.Error()
	}
	return result, nil
}

// newMasterConversation creates one durable master conversation thread. The
// title defaults from the decider when empty.
func (d *Dispatcher) newMasterConversation(ctx context.Context, title string) (contracts.ThreadID, *domain.ThreadState, error) {
	id := d.newThreadID()
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdThreadCreate, ThreadID: id,
		Title: strings.TrimSpace(title), Owner: contracts.ThreadOwnerMaster,
	}); err != nil {
		return "", nil, err
	}
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return "", nil, err
	}
	return id, st, nil
}

// masterHistory renders a master conversation's transcript as the master
// prompt's history: the user's messages and the master's replies, oldest
// first. The send that is about to happen has not been recorded yet.
func masterHistory(st *domain.ThreadState) []orchestrator.MasterMessage {
	var out []orchestrator.MasterMessage
	for _, msg := range st.Messages {
		switch msg.Author {
		case masterAuthorUser, "user":
			out = append(out, orchestrator.MasterMessage{Role: "user", Text: msg.Text})
		case masterAuthorMaster:
			out = append(out, orchestrator.MasterMessage{Role: "assistant", Text: msg.Text})
		}
	}
	return out
}

// openMasterMessages projects a master conversation into history.open's
// reply, mapping the stored authors onto the roles the home screen renders.
func openMasterMessages(st *domain.ThreadState) []MasterHistoryMessage {
	out := make([]MasterHistoryMessage, 0, len(st.Messages))
	for _, msg := range st.Messages {
		var role string
		switch msg.Author {
		case masterAuthorUser, "user":
			role = "user"
		case masterAuthorMaster:
			role = "assistant"
		default:
			continue
		}
		if strings.TrimSpace(msg.Text) == "" {
			continue
		}
		out = append(out, MasterHistoryMessage{Role: role, Text: msg.Text, At: msg.At})
	}
	return out
}

// masterThreadSummaries lists every master conversation, newest activity
// first. The list is never nil, so clients always see an array.
func (d *Dispatcher) masterThreadSummaries(ctx context.Context) ([]MasterThreadSummary, error) {
	if d.engine == nil {
		return nil, errors.New("threads are unavailable")
	}
	ids, err := d.engine.Threads(ctx)
	if err != nil {
		return nil, err
	}
	type row struct {
		summary MasterThreadSummary
		at      time.Time
	}
	var rows []row
	for _, id := range ids {
		st, err := d.engine.State(ctx, id)
		if err != nil || st == nil || !st.Exists || st.Deleted {
			continue
		}
		if st.Owner.Normalized() != contracts.ThreadOwnerMaster {
			continue
		}
		rows = append(rows, row{summary: MasterThreadSummary{
			ID: id, Title: st.Title, Messages: len(st.Messages),
		}, at: st.UpdatedAt})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].at.Equal(rows[j].at) {
			return rows[i].summary.ID > rows[j].summary.ID
		}
		return rows[i].at.After(rows[j].at)
	})
	out := make([]MasterThreadSummary, 0, len(rows))
	for _, r := range rows {
		r.summary.UpdatedAt = r.at
		out = append(out, r.summary)
	}
	return out, nil
}

// recordMasterExchange appends the user's message to a master conversation
// before the master answers, so the exchange is durable even if the model call
// fails afterwards.
func (d *Dispatcher) recordMasterUserMessage(ctx context.Context, id contracts.ThreadID, text string) error {
	return d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdMessageAppend, ThreadID: id,
		Message: &contracts.MessageSent{MessageID: d.nextID(), Author: masterAuthorUser, Text: text},
	})
}

// recordMasterReply appends the master's answer, followed by a compact note of
// the actions that ran, so reopening the conversation shows what happened and
// not only what was said.
func (d *Dispatcher) recordMasterReply(ctx context.Context, id contracts.ThreadID, res *orchestrator.MasterResult) error {
	return d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdMessageAppend, ThreadID: id,
		Message: &contracts.MessageSent{MessageID: d.nextID(), Author: masterAuthorMaster, Text: masterTranscriptText(res)},
	})
}

// masterTranscriptText renders one exchange for the durable transcript: the
// reply, then the executed actions as a short list. Rejected proposals stay
// in the reply's own error text, which Send already joined. The fallback keeps
// the transcript non-empty when the model gave prose-free actions that all
// failed validation, so the append can never be refused for empty text.
func masterTranscriptText(res *orchestrator.MasterResult) string {
	var b strings.Builder
	if reply := strings.TrimSpace(res.Reply); reply != "" {
		b.WriteString(reply)
	}
	var ran []string
	for _, r := range res.Results {
		if !r.OK {
			continue
		}
		line := r.Type
		if r.ThreadID != "" {
			line += " " + string(r.ThreadID)
		}
		ran = append(ran, line)
	}
	if len(ran) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Actions taken:\n")
		b.WriteString("- " + strings.Join(ran, "\n- "))
	}
	if b.Len() == 0 {
		if res.Error != "" {
			b.WriteString("No action ran: " + res.Error)
		} else {
			b.WriteString("No action was taken.")
		}
	}
	return b.String()
}

// masterRuntimeView renders the runtime payload for master.runtime.get and the
// set reply.
func (d *Dispatcher) masterRuntimeView() MasterRuntimeView {
	var view MasterRuntimeView
	if d.master == nil {
		return view
	}
	if ov, ok := d.master.RuntimeOverride(); ok {
		if ov.Harness.Valid() {
			view.Harness = string(ov.Harness)
		}
		view.Model = ov.Model
		view.Variant = ov.Variant
	}
	h, model, variant := d.master.Runtime()
	view.Effective.Harness = string(h)
	view.Effective.Model = model
	view.Effective.Variant = variant
	return view
}

// applyMasterRuntimeSet merges the patch onto the stored override and
// validates the result. Absent fields keep the stored value; a present field
// replaces it, and an empty string clears it back to inheritance.
func (d *Dispatcher) applyMasterRuntimeSet(harness, model, variant *string) error {
	if d.master == nil {
		return errors.New("master chat is unavailable")
	}
	next, _ := d.master.RuntimeOverride()
	if harness != nil {
		next.Harness = contracts.Harness(strings.TrimSpace(*harness))
	}
	if model != nil {
		next.Model = strings.TrimSpace(*model)
	}
	if variant != nil {
		next.Variant = strings.TrimSpace(*variant)
	}
	return d.master.SetRuntime(next.Harness, next.Model, next.Variant)
}

// MasterReport renders reporter mode's status report across threads. It is
// the daemon's wiring point for the WhatsApp bridge.
func (d *Dispatcher) MasterReport(ctx context.Context) string {
	if d.master == nil {
		return ""
	}
	return d.master.Report(ctx)
}

// MasterReporterEnabled reports whether reporter mode is on.
func (d *Dispatcher) MasterReporterEnabled() bool {
	return d.settings != nil && d.settings.MasterReporter()
}
