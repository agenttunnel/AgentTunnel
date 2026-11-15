package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// Rejections a lead gets for a steer. Nothing is queued when one is returned.
var (
	ErrSteerNotLead    = errors.New("only the team lead can steer an agent")
	ErrSteerTargetLead = errors.New("steer targets a subagent; the team lead is not steered")
	ErrSteerFinished   = errors.New("agent has already finished, so it has no next run")
)

// steerKey partitions queued steers by thread and agent. Lead ids are fixed, so
// an agent id alone would let one thread's steer reach another thread's run.
type steerKey struct {
	threadID contracts.ThreadID
	agentID  contracts.AgentID
}

// steerQueue holds steers the team lead sent to a subagent that was not running
// its prompt-building step at the time. Each agent reads its queued steers once,
// when its next run prompt is built.
type steerQueue struct {
	mu      sync.Mutex
	pending map[steerKey][]string
}

func newSteerQueue() *steerQueue {
	return &steerQueue{pending: map[steerKey][]string{}}
}

// Post queues one steer for an agent's next run.
func (q *steerQueue) Post(threadID contracts.ThreadID, agentID contracts.AgentID, text string) {
	text = strings.TrimSpace(text)
	if threadID == "" || agentID == "" || text == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	k := steerKey{threadID, agentID}
	q.pending[k] = append(q.pending[k], text)
}

// Drain returns and clears the steers queued for one agent in one thread.
func (q *steerQueue) Drain(threadID contracts.ThreadID, agentID contracts.AgentID) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	k := steerKey{threadID, agentID}
	msgs := q.pending[k]
	delete(q.pending, k)
	return msgs
}

// hasSubagents reports whether the thread has any agent that is not a lead, so a
// lead's prompt documents the steer tool only when there is someone to steer.
func hasSubagents(st *domain.ThreadState) bool {
	for id := range st.Agents {
		if !IsLead(id) {
			return true
		}
	}
	return false
}

// Steer queues a course correction from the team lead for one subagent. It does
// not reach a run already in progress: the agent reads it when its next run
// prompt is built. The returned text is the outcome the lead is shown.
func (r *Runner) Steer(ctx context.Context, threadID contracts.ThreadID, from contracts.AgentID, to, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || strings.TrimSpace(to) == "" {
		return "", ErrEmptyMessage
	}
	if !IsLead(from) {
		return "", ErrSteerNotLead
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil {
		return "", err
	}
	id, ok := resolveRecipient(st, to)
	if !ok {
		return "", fmt.Errorf("%w %s", ErrUnknownRecipient, to)
	}
	if IsLead(id) {
		return "", ErrSteerTargetLead
	}
	name := recipientName(st, id)
	if st.Agents[id].Status.Terminal() {
		return "", fmt.Errorf("%w: %s", ErrSteerFinished, name)
	}

	r.steers.Post(threadID, id, text)
	r.auditLog("steer.queue", map[string]any{
		"threadId": string(threadID), "from": string(from), "to": string(id),
	})
	line := "⇢ " + name + ": " + text
	_ = r.logs.Append(threadID, contracts.LogEntry{AgentID: from, Kind: contracts.LogTool, Text: line, At: r.clock()})
	r.publishOutput(threadID, from, contracts.LogTool, line)
	return fmt.Sprintf("queued for %s's next run; it was not interrupted mid-run", name), nil
}
