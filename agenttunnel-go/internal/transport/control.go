package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
)

// The chat control surface: the user's own WhatsApp chat can list threads, pick
// one, and steer or start its lead orchestrator. It can only ever take the
// whitelisted actions below — a message never names an RPC.

// ControlThread is one thread as the chat sees it.
type ControlThread struct {
	ID      contracts.ThreadID
	Title   string
	Running bool
}

// ControlQuestion is a thread's oldest unanswered question.
type ControlQuestion struct {
	ID      string
	Text    string
	Kind    contracts.QuestionKind
	Options []string
}

// ControlActions is the entire reach of the control surface. Every method is a
// deliberate, named capability; there is no generic RPC passthrough.
type ControlActions interface {
	// ControlThreads lists the top-level threads the chat may drive, most
	// recently active first.
	ControlThreads(ctx context.Context) ([]ControlThread, error)
	// ControlTurn starts a user turn on a thread and returns at once.
	ControlTurn(ctx context.Context, id contracts.ThreadID, text string) error
	// ControlSteer queues text for the thread's next orchestrator pass.
	ControlSteer(ctx context.Context, id contracts.ThreadID, text string) error
	// ControlPendingQuestion reports the oldest unanswered question, or nil.
	ControlPendingQuestion(ctx context.Context, id contracts.ThreadID) (*ControlQuestion, error)
	// ControlAnswer answers one question. selected carries a choice option;
	// text carries free text.
	ControlAnswer(ctx context.Context, id contracts.ThreadID, questionID string, selected []string, text string) error
	// ControlMaster asks the home-screen orchestrator one message, with the
	// chat's own prior exchange as history.
	ControlMaster(ctx context.Context, text string, history []orchestrator.MasterMessage) (orchestrator.MasterResult, error)
}

// ControlRouter turns one chat message into one whitelisted action and a reply.
// It holds only the selection and the rate limiter: thread state is read fresh
// for every command.
type ControlRouter struct {
	actions ControlActions

	mu       sync.Mutex
	selected contracts.ThreadID
	// master points plain text at the home-screen orchestrator instead of a
	// thread. masterHistory is the chat's own exchange with it, so follow-ups
	// have context without sharing the desktop's conversation.
	master        bool
	masterHistory []orchestrator.MasterMessage
	tokens        float64
	refilledAt    time.Time
	noticedAt     time.Time
	now           func() time.Time
}

// NewControlRouter builds a router over the daemon's control actions.
func NewControlRouter(actions ControlActions) *ControlRouter {
	return &ControlRouter{actions: actions, now: time.Now}
}

// Handle executes one chat message. The reply is what the bridge sends back; an
// empty reply means nothing goes out (the action's own updates still will, for
// example the summary at the end of a turn).
func (r *ControlRouter) Handle(ctx context.Context, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if reply, ok := r.takeToken(); !ok {
		return reply
	}

	cmd, arg := splitControlCommand(text)
	switch cmd {
	case "/threads":
		return r.listThreads(ctx)
	case "/use":
		return r.use(ctx, arg)
	case "/steer":
		return r.steer(ctx, arg)
	case "/answer":
		return r.answer(ctx, arg)
	case "/turn":
		return r.turn(ctx, arg)
	case "/master":
		if arg == "" {
			return "Ask Master what? Send /master <text>."
		}
		return r.askMaster(ctx, arg)
	}
	if strings.HasPrefix(cmd, "/") {
		return fmt.Sprintf("Unknown command %s. Use /threads, /use, /master, /steer, /answer, or /turn.", clipRunes(cmd, 24))
	}
	return r.message(ctx, text)
}

// splitControlCommand separates a leading command word from its argument. Plain
// text keeps an empty command.
func splitControlCommand(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		return text[:i], strings.TrimSpace(text[i+1:])
	}
	return text, ""
}

func (r *ControlRouter) listThreads(ctx context.Context) string {
	threads, err := r.actions.ControlThreads(ctx)
	if err != nil {
		return "Could not list threads: " + err.Error()
	}
	if len(threads) == 0 {
		return "No threads yet."
	}
	selected := r.selection()
	masterActive := r.isMaster()
	lines := make([]string, 0, len(threads)+3)
	lines = append(lines, "Threads:")
	for i, t := range threads {
		if i >= controlListMax {
			lines = append(lines, fmt.Sprintf("…and %d more.", len(threads)-controlListMax))
			break
		}
		line := fmt.Sprintf("%d. %s", i+1, controlThreadLabel(t))
		if t.ID == selected {
			line += " (selected)"
		}
		if t.Running {
			line += " (running)"
		}
		lines = append(lines, line)
	}
	if masterActive {
		lines = append(lines, "", "Active: Master (home screen).")
	}
	lines = append(lines, "", "Send /use <number> to select one, or /use master.")
	return strings.Join(lines, "\n")
}

func (r *ControlRouter) use(ctx context.Context, arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "Which target? Send /threads, then /use <number>, or /use master."
	}
	if arg == "master" {
		return r.selectMaster()
	}
	threads, err := r.actions.ControlThreads(ctx)
	if err != nil {
		return "Could not list threads: " + err.Error()
	}
	if n, err := strconv.Atoi(arg); err == nil {
		// Only the numbers /threads actually printed are selectable: past the
		// list page the ordering shifts between calls, so the user must re-send
		// /threads rather than pick a row they never saw.
		shown := len(threads)
		if shown > controlListMax {
			shown = controlListMax
		}
		if n < 1 || n > shown {
			return fmt.Sprintf("Thread %d is not in the list. Send /threads.", n)
		}
		return r.selectThread(threads[n-1])
	}
	for _, t := range threads {
		if string(t.ID) == arg {
			return r.selectThread(t)
		}
	}
	return "No thread has that id. Send /threads."
}

func (r *ControlRouter) selectThread(t ControlThread) string {
	r.mu.Lock()
	r.selected = t.ID
	r.master = false
	r.mu.Unlock()
	return "Selected " + controlThreadLabel(t) + "."
}

// selectMaster points plain text at the home-screen orchestrator. It clears the
// thread selection so the two targets can never be active at once.
func (r *ControlRouter) selectMaster() string {
	r.mu.Lock()
	r.selected = ""
	r.master = true
	r.mu.Unlock()
	return "Selected Master (home screen). Plain text goes to Master; send /use <number> to return to a thread."
}

// askMaster sends one message to the home-screen orchestrator and replies with
// its answer. A thread the orchestrator proposes is started by the actions, the
// same way the desktop starts it.
func (r *ControlRouter) askMaster(ctx context.Context, text string) string {
	r.mu.Lock()
	history := append([]orchestrator.MasterMessage(nil), r.masterHistory...)
	r.mu.Unlock()
	result, err := r.actions.ControlMaster(ctx, text, history)
	if err != nil {
		return "Master failed: " + err.Error()
	}
	reply := strings.TrimSpace(result.Reply)
	if reply == "" {
		reply = "Master had no answer."
	}
	r.remember(text, reply)
	out := "Master: " + reply
	switch {
	case result.Action != nil && result.ThreadID != "":
		out += "\n\nStarted a thread: " + result.Action.Title + "."
	case result.Error != "":
		out += "\n\nNo thread was started: " + result.Error + "."
	}
	return out
}

// remember appends one exchange to the chat's Master history, keeping only the
// most recent messages.
func (r *ControlRouter) remember(text, reply string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.masterHistory = append(r.masterHistory,
		orchestrator.MasterMessage{Role: "user", Text: text},
		orchestrator.MasterMessage{Role: "assistant", Text: reply},
	)
	if over := len(r.masterHistory) - controlMasterHistory; over > 0 {
		r.masterHistory = r.masterHistory[over:]
	}
}

// isMaster reports whether plain text currently goes to Master.
func (r *ControlRouter) isMaster() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.master
}

func (r *ControlRouter) steer(ctx context.Context, text string) string {
	if strings.TrimSpace(text) == "" {
		return "Steer with what? Send /steer <text>."
	}
	thread, problem := r.selectedThread(ctx)
	if problem != "" {
		return problem
	}
	if err := r.actions.ControlSteer(ctx, thread.ID, text); err != nil {
		return "Could not steer: " + err.Error()
	}
	return "Steering " + controlThreadLabel(thread) + "."
}

func (r *ControlRouter) turn(ctx context.Context, text string) string {
	if strings.TrimSpace(text) == "" {
		return "A turn needs a prompt. Send /turn <text>."
	}
	thread, problem := r.selectedThread(ctx)
	if problem != "" {
		return problem
	}
	if err := r.actions.ControlTurn(ctx, thread.ID, text); err != nil {
		return "Could not start a turn: " + err.Error()
	}
	return "Started a turn on " + controlThreadLabel(thread) + "."
}

func (r *ControlRouter) answer(ctx context.Context, arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "Answer with what? Send /answer <text|option#>."
	}
	thread, problem := r.selectedThread(ctx)
	if problem != "" {
		return problem
	}
	q, err := r.actions.ControlPendingQuestion(ctx, thread.ID)
	if err != nil {
		return "Could not read questions: " + err.Error()
	}
	if q == nil {
		return "Nothing is waiting for an answer on " + controlThreadLabel(thread) + "."
	}
	var selected []string
	text := arg
	if n, err := strconv.Atoi(arg); err == nil && len(q.Options) > 0 {
		if n < 1 || n > len(q.Options) {
			return fmt.Sprintf("There is no option %d; this question has %d.", n, len(q.Options))
		}
		selected, text = []string{q.Options[n-1]}, ""
	}
	if err := r.actions.ControlAnswer(ctx, thread.ID, q.ID, selected, text); err != nil {
		return "Could not answer: " + err.Error()
	}
	return "Answered: " + controlAnswerLabel(selected, text) + "."
}

// message routes plain text to the selected thread: steering while a turn is
// running, a new turn when it is idle.
func (r *ControlRouter) message(ctx context.Context, text string) string {
	if r.isMaster() {
		return r.askMaster(ctx, text)
	}
	thread, problem := r.selectedThread(ctx)
	if problem != "" {
		return problem
	}
	if thread.Running {
		if err := r.actions.ControlSteer(ctx, thread.ID, text); err != nil {
			return "Could not steer: " + err.Error()
		}
		return "Steering " + controlThreadLabel(thread) + "."
	}
	if err := r.actions.ControlTurn(ctx, thread.ID, text); err != nil {
		return "Could not start a turn: " + err.Error()
	}
	return "Started a turn on " + controlThreadLabel(thread) + "."
}

// selectedThread resolves the selection against the current list. A thread that
// was deleted clears the selection instead of leaving every command confused.
func (r *ControlRouter) selectedThread(ctx context.Context) (ControlThread, string) {
	if r.isMaster() {
		return ControlThread{}, "Master is selected, not a thread. Send /threads, then /use <number>."
	}
	id := r.selection()
	if id == "" {
		return ControlThread{}, "No thread selected. Send /threads, then /use <number>."
	}
	threads, err := r.actions.ControlThreads(ctx)
	if err != nil {
		return ControlThread{}, "Could not list threads: " + err.Error()
	}
	for _, t := range threads {
		if t.ID == id {
			return t, ""
		}
	}
	r.mu.Lock()
	r.selected = ""
	r.mu.Unlock()
	return ControlThread{}, "That thread is gone. Send /threads to pick another."
}

func (r *ControlRouter) selection() contracts.ThreadID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.selected
}

// takeToken applies the inbound rate limit. A drop answers once per notice
// window: replying to every dropped message would flood the chat.
func (r *ControlRouter) takeToken() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.refilledAt.IsZero() {
		r.tokens = controlBurst
	} else {
		r.tokens += now.Sub(r.refilledAt).Seconds() / controlRefillInterval.Seconds()
		if r.tokens > controlBurst {
			r.tokens = controlBurst
		}
	}
	r.refilledAt = now
	if r.tokens >= 1 {
		r.tokens--
		return "", true
	}
	if !r.noticedAt.IsZero() && now.Sub(r.noticedAt) < controlNoticeInterval {
		return "", false
	}
	r.noticedAt = now
	return "Too many messages; wait a moment and try again.", false
}

func controlThreadLabel(t ControlThread) string {
	title := strings.TrimSpace(t.Title)
	if title == "" {
		return string(t.ID)
	}
	return title
}

func controlAnswerLabel(selected []string, text string) string {
	switch {
	case len(selected) > 0 && text != "":
		return strings.Join(selected, ", ") + "; " + text
	case len(selected) > 0:
		return strings.Join(selected, ", ")
	default:
		return text
	}
}

const (
	// controlBurst is how many commands one chat may send back to back before
	// the limiter engages, and controlRefillInterval is the time each token
	// takes to come back.
	controlBurst          = 5
	controlRefillInterval = 2 * time.Second
	// controlNoticeInterval bounds how often a rate-limited chat is told.
	controlNoticeInterval = 10 * time.Second
	// controlListMax bounds /threads so the reply stays one readable message.
	controlListMax = 10
	// controlMasterHistory bounds the chat's exchange with Master. The
	// orchestrator reads at most this many messages anyway.
	controlMasterHistory = 12
)

// turnRunning reports whether a turn holds the thread's slot, which is the
// authoritative "busy" answer: agents can all be idle between passes while the
// turn is still running.
func (d *Dispatcher) turnRunning(id contracts.ThreadID) bool {
	d.turnsMu.Lock()
	defer d.turnsMu.Unlock()
	_, running := d.turns[id]
	return running
}

// ControlThreads lists the top-level threads the chat may drive.
func (d *Dispatcher) ControlThreads(ctx context.Context) ([]ControlThread, error) {
	out, err := d.Handle(ctx, Request{ID: d.nextID(), Method: "thread.list"})
	if err != nil {
		return nil, err
	}
	rows, ok := out.([]ThreadSummary)
	if !ok {
		return nil, errors.New("thread list is unavailable")
	}
	threads := make([]ControlThread, 0, len(rows))
	for _, row := range rows {
		// Subthreads belong to the agent that started them and archived threads
		// are out of the way; the chat drives top-level work only.
		if row.ParentThreadID != "" || row.Archived {
			continue
		}
		threads = append(threads, ControlThread{ID: row.ID, Title: row.Title, Running: d.turnRunning(row.ID)})
	}
	return threads, nil
}

// ControlTurn starts a turn in the background: thread.turn.start answers only
// when the whole turn is done, and a chat message must not wait for that. The
// slot is claimed before this returns, so a busy race, a worktree failure, or a
// dispatch failure reaches the chat as an error instead of "Started a turn"
// followed by silence.
func (d *Dispatcher) ControlTurn(ctx context.Context, id contracts.ThreadID, text string) error {
	if d.engine == nil {
		return errors.New("threads are unavailable")
	}
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return err
	}
	if !st.Exists {
		return fmt.Errorf("thread %q does not exist", id)
	}
	// Busy is checked before the orchestrator so a chat gets the real reason.
	if d.turnRunning(id) {
		return fmt.Errorf("a turn is already running on %s", id)
	}
	if d.orch == nil {
		return errors.New("the orchestrator is unavailable")
	}
	// Claim the slot synchronously: beginTurn is the atomic busy check, so two
	// chat commands cannot both believe they started a turn.
	turnCtx, done, err := d.beginTurn(ctx, id)
	if err != nil {
		return err
	}
	// The start gate runs before the reply too. The ctx is the bridge's, not a
	// request's, so shutdown cancels the turn and its RunTurn instead of being
	// detached from it.
	if err := d.ensureWorktree(turnCtx, id); err != nil {
		done()
		return err
	}
	cmd := contracts.Command{ID: d.nextID(), Type: contracts.CmdTurnStart, ThreadID: id, Text: text}
	if err := d.engine.Dispatch(turnCtx, cmd); err != nil {
		done()
		return err
	}
	go func() {
		defer done()
		if err := d.orch.RunTurn(turnCtx, id, text); err != nil && turnCtx.Err() == nil {
			d.auditLog("whatsapp.control", map[string]any{
				"threadId": string(id), "action": "turn", "error": err.Error(),
			})
		}
	}()
	return nil
}

// ControlSteer queues steering text through the same RPC the desktop uses, so
// the transcript records it exactly as a typed steer.
func (d *Dispatcher) ControlSteer(ctx context.Context, id contracts.ThreadID, text string) error {
	params, err := json.Marshal(map[string]any{"threadId": id, "text": text})
	if err != nil {
		return err
	}
	_, err = d.Handle(ctx, Request{ID: d.nextID(), Method: "thread.steer", Params: params})
	return err
}

// ControlPendingQuestion reads the oldest unanswered question from the thread's
// state, which is the same record the desktop renders.
func (d *Dispatcher) ControlPendingQuestion(ctx context.Context, id contracts.ThreadID) (*ControlQuestion, error) {
	if d.engine == nil {
		return nil, errors.New("threads are unavailable")
	}
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return nil, err
	}
	if !st.Exists {
		return nil, fmt.Errorf("thread %q does not exist", id)
	}
	for _, q := range st.Questions {
		if q.Status != domain.QuestionPending {
			continue
		}
		return &ControlQuestion{
			ID:      q.QuestionID,
			Text:    q.Question,
			Kind:    q.Kind,
			Options: append([]string(nil), q.Options...),
		}, nil
	}
	return nil, nil
}

// ControlAnswer answers through question.answer, the same call the desktop
// makes, so the blocked asker receives it.
func (d *Dispatcher) ControlAnswer(ctx context.Context, id contracts.ThreadID, questionID string, selected []string, text string) error {
	params, err := json.Marshal(map[string]any{
		"threadId":   id,
		"questionId": questionID,
		"selected":   selected,
		"text":       text,
	})
	if err != nil {
		return err
	}
	_, err = d.Handle(ctx, Request{ID: d.nextID(), Method: "question.answer", Params: params})
	return err
}

// ControlMaster asks the home-screen orchestrator one message through the same
// Send master.send uses, and starts a proposed thread the way master.send does,
// so the chat and the desktop agree on what a proposal does.
func (d *Dispatcher) ControlMaster(ctx context.Context, text string, history []orchestrator.MasterMessage) (orchestrator.MasterResult, error) {
	if d.master == nil {
		return orchestrator.MasterResult{}, errors.New("master chat is unavailable")
	}
	result, err := d.master.Send(ctx, text, history)
	if err != nil {
		return orchestrator.MasterResult{}, err
	}
	if result.Action != nil {
		if err := d.startMasterThread(ctx, result.Action, &result); err != nil {
			return orchestrator.MasterResult{}, err
		}
	}
	return result, nil
}
