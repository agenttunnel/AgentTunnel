package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// orchestratorAgentID is the team lead's agent id. Subagents reach it with
// `--to orchestrator`; the runtime inbox it drains is keyed by the same id.
const orchestratorAgentID = contracts.AgentID("agent-orchestrator")

// leadAgentID is the id of the i-th lead on a thread's team. Lead zero keeps the
// original id. Later leads end in "-lead<n>" rather than "-<n>": subagent ids
// always end in a sequence number, so no subagent can ever take a lead's id.
func leadAgentID(i int) contracts.AgentID {
	if i == 0 {
		return orchestratorAgentID
	}
	return contracts.AgentID(fmt.Sprintf("agent-orchestrator-lead%d", i+1))
}

// leadName is the roster name the i-th lead is addressed by. Lead zero is the
// classic "orchestrator".
func leadName(i int) string {
	if i == 0 {
		return "orchestrator"
	}
	return fmt.Sprintf("orchestrator-%d", i+1)
}

// isLeadID reports whether id is a lead on any team size up to MaxLeads.
func isLeadID(id contracts.AgentID) bool {
	for i := 0; i < contracts.MaxLeads; i++ {
		if id == leadAgentID(i) {
			return true
		}
	}
	return false
}

// Rejections a caller can present to the agent that sent the message. Nothing
// is recorded durably when one of these is returned.
var (
	ErrPeerMailboxDisabled = errors.New("peer mailbox is disabled for this thread")
	ErrUnknownRecipient    = errors.New("unknown agent")
	ErrEmptyMessage        = errors.New("message text is required")
)

// PostMessage delivers one peer message end to end: it resolves the recipient,
// drops the message in the runtime mailbox so the next pass prompt sees it,
// records the durable MailboxPosted event, and leaves a trail on the sender's
// log. Both the HTTP tool endpoint and the printed-line fallback go through it.
func (r *Runner) PostMessage(ctx context.Context, threadID contracts.ThreadID, from contracts.AgentID, to, body string) error {
	return r.postMessage(ctx, threadID, from, to, body, nil)
}

// postMessage resolves the recipient against the thread roster and then against
// `pending` — dispatches minted in the pass being processed but not yet recorded
// in the thread state — so a lead can message an agent it dispatched moments
// ago, while that agent still waits on its dependencies.
func (r *Runner) postMessage(ctx context.Context, threadID contracts.ThreadID, from contracts.AgentID, to, body string, pending []contracts.DispatchTask) error {
	if strings.TrimSpace(body) == "" {
		return ErrEmptyMessage
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil {
		return err
	}
	if !st.PeerMailbox {
		r.auditLog("mailbox.reject", map[string]any{
			"threadId": string(threadID), "from": string(from), "to": to, "reason": "peer mailbox disabled",
		})
		return ErrPeerMailboxDisabled
	}
	resolved, ok := resolveRecipient(st, to)
	display := ""
	if ok {
		display = recipientName(st, resolved)
	} else {
		// Not in the roster (yet): a dispatch minted in this same pass names it.
		task, pendingOk := pendingRecipient(pending, to)
		if !pendingOk {
			r.auditLog("mailbox.reject", map[string]any{
				"threadId": string(threadID), "from": string(from), "to": to, "reason": "unknown agent",
			})
			return fmt.Errorf("%w %s", ErrUnknownRecipient, to)
		}
		resolved, display = task.AgentID, task.Name
	}

	msg := contracts.MailboxPosted{From: from, To: resolved, Kind: "message", Body: body}
	// A harness that executes the message command AND echoes the same line
	// into its text output delivers the message twice: once over `/tool/call`
	// mid-pass, once through the printed-line drain. The durable record is
	// the truth — an identical from/to/body already posted this turn is the
	// echo, not a new message, so absorb it instead of duplicating it.
	for _, m := range st.Mailbox {
		if m.Turn == st.Turn && m.From == from && m.To == resolved && m.Body == body {
			r.auditLog("mailbox.duplicate", map[string]any{
				"threadId": string(threadID), "from": string(from), "to": string(resolved),
			})
			return nil
		}
	}
	if r.mailbox != nil {
		if err := r.mailbox.Post(threadID, msg); err != nil {
			return err
		}
	}
	if err := r.engine.Dispatch(ctx, contracts.Command{
		ID:       r.nextID(),
		Type:     contracts.CmdMailboxPost,
		ThreadID: threadID,
		Mailbox:  &msg,
	}); err != nil {
		return err
	}

	r.auditLog("mailbox.post", map[string]any{
		"threadId": string(threadID), "from": string(from), "to": string(resolved), "body": body,
	})
	line := "→ " + display + ": " + body
	_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: from, Kind: contracts.LogTool, Text: line, At: r.clock()})
	r.publishOutput(threadID, from, contracts.LogTool, line)
	return nil
}

// pendingRecipient finds a dispatch minted in the current pass by roster name.
func pendingRecipient(pending []contracts.DispatchTask, to string) (contracts.DispatchTask, bool) {
	for _, t := range pending {
		if t.Name == to {
			return t, true
		}
	}
	return contracts.DispatchTask{}, false
}

// resolveRecipient maps a `--to` value to an agent id: "orchestrator" names the
// team lead, an exact agent id wins, otherwise the newest agent carrying that
// name is addressed — preferring one that has not finished yet.
func resolveRecipient(st *domain.ThreadState, to string) (contracts.AgentID, bool) {
	if to == "orchestrator" {
		return orchestratorAgentID, true
	}
	if _, ok := st.Agents[contracts.AgentID(to)]; ok {
		return contracts.AgentID(to), true
	}
	var live, latest contracts.AgentID
	for _, id := range st.AgentOrder {
		if st.Agents[id].Name != to {
			continue
		}
		latest = id
		if !st.Agents[id].Status.Terminal() {
			live = id
		}
	}
	if live != "" {
		return live, true
	}
	return latest, latest != ""
}

// recipientName renders a recipient the way its peers would address it: the
// agent's roster name when it has one, otherwise the raw id.
func recipientName(st *domain.ThreadState, id contracts.AgentID) string {
	if a, ok := st.Agents[id]; ok && a.Name != "" {
		return a.Name
	}
	return string(id)
}
