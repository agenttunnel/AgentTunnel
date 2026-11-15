package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
)

// A question is pending only while the `tool ask` call that asked it is blocked
// waiting for the answer. Every way that call can end, or that the turn can end,
// closes the question with a reason, so the log never holds an answer nobody can
// receive.

const (
	defaultQuestionTimeout = 10 * time.Minute
	maxQuestionTimeout     = time.Hour
	// notifyOrchestrator is who hears the answer to a subagent's question when
	// the asker names no one: the lead is the agent that can act on it.
	notifyOrchestrator = "orchestrator"
	// questionFrom is the mailbox sender for an answer: the human, not an agent.
	questionFrom = contracts.AgentID("user")
)

var errQuestionNotWaiting = errors.New("question is no longer waiting")

// questionKey partitions waiters by thread: question ids are random, but the
// broker never assumes that, so an answer on one thread cannot wake another's ask.
type questionKey struct {
	threadID contracts.ThreadID
	id       string
}

// questionBroker holds the blocked `tool ask` calls. A waiter is woken when its
// question closes; the answer itself is read back from the log, which stays the
// only source of truth.
type questionBroker struct {
	mu      sync.Mutex
	waiters map[questionKey]chan struct{}
}

func newQuestionBroker() *questionBroker {
	return &questionBroker{waiters: map[questionKey]chan struct{}{}}
}

// wait registers a waiter for a question and returns the channel closed when it
// closes.
func (b *questionBroker) wait(threadID contracts.ThreadID, id string) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan struct{})
	b.waiters[questionKey{threadID, id}] = ch
	return ch
}

// forget drops a waiter, once its call has returned.
func (b *questionBroker) forget(threadID contracts.ThreadID, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.waiters, questionKey{threadID, id})
}

// wake releases the waiter for a question, if one is blocked.
func (b *questionBroker) wake(threadID contracts.ThreadID, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := questionKey{threadID, id}
	if ch, ok := b.waiters[k]; ok {
		close(ch)
		delete(b.waiters, k)
	}
}

// waiting reports whether a call is blocked on the question.
func (b *questionBroker) waiting(threadID contracts.ThreadID, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.waiters[questionKey{threadID, id}]
	return ok
}

func (d *Dispatcher) newQuestionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "q_" + hex.EncodeToString(b[:])
}

// closeQuestion records how a question ended and wakes its waiter. A close that
// loses the race (already answered, dismissed or timed out) returns the decider's
// error and wakes nothing: the winning close wakes the waiter itself.
func (d *Dispatcher) closeQuestion(ctx context.Context, threadID contracts.ThreadID, id string, closed contracts.QuestionClosed) error {
	closed.QuestionID = id
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdQuestionClose, ThreadID: threadID, QuestionClose: &closed,
	}); err != nil {
		return err
	}
	d.questions.wake(threadID, id)
	return nil
}

// resolveQuestion applies a human's answer or dismissal. It refuses a question
// no call is waiting on, closing it so it cannot linger pending.
func (d *Dispatcher) resolveQuestion(ctx context.Context, threadID contracts.ThreadID, id string, closed contracts.QuestionClosed) error {
	if !d.questions.waiting(threadID, id) {
		_ = d.closeQuestion(ctx, threadID, id, contracts.QuestionClosed{Reason: "no longer waiting"})
		return errQuestionNotWaiting
	}
	return d.closeQuestion(ctx, threadID, id, closed)
}

// closeOpenQuestions ends every question still pending on a thread whose turn
// has finished, so nothing is left waiting for an answer that cannot arrive.
func (d *Dispatcher) closeOpenQuestions(threadID contracts.ThreadID, reason string) {
	ctx := context.Background()
	st, err := d.engine.State(ctx, threadID)
	if err != nil {
		return
	}
	for _, q := range st.Questions {
		if q.Status == domain.QuestionPending {
			_ = d.closeQuestion(ctx, threadID, q.QuestionID, contracts.QuestionClosed{Reason: reason})
		}
	}
}

// CloseStaleQuestions ends every question left pending by a previous daemon run.
// Their callers died with it, so none can ever receive an answer. Call it once,
// before the daemon serves.
func (d *Dispatcher) CloseStaleQuestions(ctx context.Context) error {
	threads, err := d.engine.Threads(ctx)
	if err != nil {
		return err
	}
	for _, threadID := range threads {
		st, err := d.engine.State(ctx, threadID)
		if err != nil {
			return err
		}
		for _, q := range st.Questions {
			if q.Status != domain.QuestionPending {
				continue
			}
			if err := d.closeQuestion(ctx, threadID, q.QuestionID, contracts.QuestionClosed{Reason: "daemon restarted"}); err != nil {
				return fmt.Errorf("close stale question %s: %w", q.QuestionID, err)
			}
		}
	}
	return nil
}

// handleAskCall asks the human a question and holds the request until it closes.
// The response carries the answer, so the agent's command prints it in the same
// run; a question that closed unanswered says why.
func (s *Server) handleAskCall(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	timeout := defaultQuestionTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}
	if timeout > maxQuestionTimeout {
		http.Error(w, fmt.Sprintf("timeout must be at most %d seconds", int(maxQuestionTimeout/time.Second)), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	id := s.disp.newQuestionID()
	// The waiter is registered before the question is recorded, so an answer that
	// arrives the instant it appears always finds this call.
	waiter := s.disp.questions.wait(req.ThreadID, id)
	defer s.disp.questions.forget(req.ThreadID, id)

	notify := strings.TrimSpace(req.To)
	if notify == "" && req.AgentID != "" && !orchestrator.IsLead(req.AgentID) {
		notify = notifyOrchestrator
	}
	if err := s.engine.Dispatch(ctx, contracts.Command{
		ID: s.disp.nextID(), Type: contracts.CmdQuestionAsk, ThreadID: req.ThreadID,
		QuestionAsk: &contracts.QuestionAsked{
			QuestionID: id,
			AgentID:    req.AgentID,
			Kind:       contracts.QuestionKind(req.QuestionKind),
			Question:   req.Text,
			Options:    req.Options,
			Notify:     notify,
		},
	}); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrInvalidCommand) || errors.Is(err, domain.ErrThreadNotFound) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waiter:
	case <-ctx.Done():
		// The caller is gone: the turn stopped, or its command was killed.
		_ = s.disp.closeQuestion(context.WithoutCancel(ctx), req.ThreadID, id, contracts.QuestionClosed{Reason: "asker gone"})
	case <-timer.C:
		_ = s.disp.closeQuestion(ctx, req.ThreadID, id, contracts.QuestionClosed{Reason: "timed out"})
	}

	// Whatever closed the question, the log holds the outcome. Read it back so the
	// caller gets exactly what was recorded, even when another close won the race.
	st, err := s.engine.State(context.WithoutCancel(ctx), req.ThreadID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var q *domain.Question
	for i := range st.Questions {
		if st.Questions[i].QuestionID == id {
			q = &st.Questions[i]
			break
		}
	}
	if q == nil || q.Status == domain.QuestionPending {
		http.Error(w, "question was not recorded", http.StatusInternalServerError)
		return
	}

	resp := map[string]any{"ok": true, "answered": q.Status == domain.QuestionAnswered}
	if q.Status == domain.QuestionAnswered {
		resp["selected"] = q.Selected
		resp["text"] = q.Text
		if notify != "" && s.runner != nil {
			s.postAnswer(ctx, req.ThreadID, notify, q)
		}
	} else {
		resp["reason"] = q.Reason
	}
	s.disp.auditLog("tool", map[string]any{
		"kind": req.Kind, "agentId": string(req.AgentID), "threadId": string(req.ThreadID),
		"questionId": id, "outcome": q.Status,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// postAnswer tells the notify recipient what the human answered, over the peer
// mailbox. It is best effort: the answer is already recorded, so a refused
// message (mailbox off, recipient gone) is only logged.
func (s *Server) postAnswer(ctx context.Context, threadID contracts.ThreadID, to string, q *domain.Question) {
	body := fmt.Sprintf("User answered the question %q: %s", q.Question, answerSummary(q))
	if err := s.runner.PostMessage(ctx, threadID, questionFrom, to, body); err != nil {
		s.disp.auditLog("ask.notify", map[string]any{
			"threadId": string(threadID), "questionId": q.QuestionID, "to": to, "error": err.Error(),
		})
	}
}

// answerSummary renders an answer as one line: the chosen options, then any
// other text.
func answerSummary(q *domain.Question) string {
	var parts []string
	if len(q.Selected) > 0 {
		parts = append(parts, strings.Join(q.Selected, ", "))
	}
	if q.Text != "" {
		parts = append(parts, q.Text)
	}
	return strings.Join(parts, "; ")
}
