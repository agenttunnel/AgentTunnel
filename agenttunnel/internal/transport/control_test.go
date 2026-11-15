package transport

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/steering"
)

// fakeControlActions records the whitelisted calls the router makes. It is the
// whole surface a chat message can reach, so asserting on it asserts that no
// other action is possible.
type fakeControlActions struct {
	threads     []ControlThread
	threadsErr  error
	question    *ControlQuestion
	questionErr error
	turnErr     error
	steerErr    error
	answerErr   error
	btwAnswer   string
	btwErr      error

	turns   []controlTextCall
	steers  []controlTextCall
	answers []controlAnswerCall
	btws    []controlTextCall

	masterResult orchestrator.MasterResult
	masterErr    error
	masters      []controlMasterCall
}

type controlMasterCall struct {
	text    string
	history []orchestrator.MasterMessage
}

// unthrottle advances the clock on every call, so a command test exercises the
// routing rather than the limiter.
func unthrottle(r *ControlRouter) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time {
		now = now.Add(time.Minute)
		return now
	}
}

type controlTextCall struct {
	thread contracts.ThreadID
	text   string
}

type controlAnswerCall struct {
	thread   contracts.ThreadID
	question string
	selected []string
	text     string
}

func (f *fakeControlActions) ControlThreads(context.Context) ([]ControlThread, error) {
	return f.threads, f.threadsErr
}

func (f *fakeControlActions) ControlTurn(_ context.Context, id contracts.ThreadID, text string) error {
	f.turns = append(f.turns, controlTextCall{id, text})
	return f.turnErr
}

func (f *fakeControlActions) ControlSteer(_ context.Context, id contracts.ThreadID, text string) error {
	f.steers = append(f.steers, controlTextCall{id, text})
	return f.steerErr
}

func (f *fakeControlActions) ControlPendingQuestion(context.Context, contracts.ThreadID) (*ControlQuestion, error) {
	return f.question, f.questionErr
}

func (f *fakeControlActions) ControlAnswer(_ context.Context, id contracts.ThreadID, questionID string, selected []string, text string) error {
	f.answers = append(f.answers, controlAnswerCall{id, questionID, selected, text})
	return f.answerErr
}

func (f *fakeControlActions) ControlBtw(_ context.Context, id contracts.ThreadID, question string) (string, error) {
	f.btws = append(f.btws, controlTextCall{id, question})
	return f.btwAnswer, f.btwErr
}

func (f *fakeControlActions) ControlMaster(_ context.Context, text string, history []orchestrator.MasterMessage) (orchestrator.MasterResult, error) {
	f.masters = append(f.masters, controlMasterCall{text, append([]orchestrator.MasterMessage(nil), history...)})
	return f.masterResult, f.masterErr
}

func newFakeActions() *fakeControlActions {
	return &fakeControlActions{threads: []ControlThread{
		{ID: "t1", Title: "Fix flaky test"},
		{ID: "t2", Title: "Write docs", Running: true},
	}}
}

func TestControlRouterPlainTextSteersRunningThreadAndTurnsIdleOne(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	router := NewControlRouter(actions)
	unthrottle(router)

	if reply := router.Handle(ctx, "ship it"); !strings.Contains(reply, "No thread selected") {
		t.Fatalf("unselected reply = %q", reply)
	}
	if len(actions.turns)+len(actions.steers) != 0 {
		t.Fatal("an unselected message must not act on a thread")
	}

	reply := router.Handle(ctx, "/use 2")
	if !strings.Contains(reply, "Selected") || !strings.Contains(reply, "Write docs") {
		t.Fatalf("use reply = %q", reply)
	}
	reply = router.Handle(ctx, "please add examples")
	if !strings.Contains(reply, "Steering") {
		t.Fatalf("running-thread reply = %q, want steering", reply)
	}
	if len(actions.steers) != 1 || actions.steers[0] != (controlTextCall{"t2", "please add examples"}) {
		t.Fatalf("steers = %+v", actions.steers)
	}

	reply = router.Handle(ctx, "/use t1")
	if !strings.Contains(reply, "Fix flaky test") {
		t.Fatalf("use-by-id reply = %q", reply)
	}
	reply = router.Handle(ctx, "run the full suite")
	if !strings.Contains(reply, "Started a turn") {
		t.Fatalf("idle-thread reply = %q, want a turn", reply)
	}
	if len(actions.turns) != 1 || actions.turns[0] != (controlTextCall{"t1", "run the full suite"}) {
		t.Fatalf("turns = %+v", actions.turns)
	}
}

func TestControlRouterExplicitCommands(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	actions.question = &ControlQuestion{ID: "q1", Text: "Ship it?", Kind: contracts.QuestionChoice, Options: []string{"yes", "no"}}
	router := NewControlRouter(actions)
	unthrottle(router)
	router.Handle(ctx, "/use 1")

	if reply := router.Handle(ctx, "/steer focus on tests"); !strings.Contains(reply, "Steering") {
		t.Fatalf("steer reply = %q", reply)
	}
	if len(actions.steers) != 1 || actions.steers[0].text != "focus on tests" {
		t.Fatalf("steers = %+v", actions.steers)
	}

	// An explicit /turn starts a turn even though a steer would be possible.
	if reply := router.Handle(ctx, "/turn do it"); !strings.Contains(reply, "Started a turn") {
		t.Fatalf("turn reply = %q", reply)
	}
	if len(actions.turns) != 1 || actions.turns[0].text != "do it" {
		t.Fatalf("turns = %+v", actions.turns)
	}

	// A numbered answer selects that option.
	if reply := router.Handle(ctx, "/answer 2"); !strings.Contains(reply, "Answered: no") {
		t.Fatalf("answer reply = %q", reply)
	}
	if len(actions.answers) != 1 || actions.answers[0].question != "q1" ||
		len(actions.answers[0].selected) != 1 || actions.answers[0].selected[0] != "no" || actions.answers[0].text != "" {
		t.Fatalf("answers = %+v", actions.answers)
	}

	// Free text goes through as text, for "other".
	if reply := router.Handle(ctx, "/answer maybe later"); !strings.Contains(reply, "Answered: maybe later") {
		t.Fatalf("free answer reply = %q", reply)
	}
	if got := actions.answers[1]; len(got.selected) != 0 || got.text != "maybe later" {
		t.Fatalf("free answer = %+v", got)
	}

	// Out-of-range options are refused without an action.
	before := len(actions.answers)
	if reply := router.Handle(ctx, "/answer 9"); !strings.Contains(reply, "no option 9") {
		t.Fatalf("out-of-range reply = %q", reply)
	}
	if len(actions.answers) != before {
		t.Fatal("an out-of-range option must not answer")
	}

	// Nothing pending is reported, not silently swallowed.
	actions.question = nil
	if reply := router.Handle(ctx, "/answer yes"); !strings.Contains(reply, "Nothing is waiting") {
		t.Fatalf("no-question reply = %q", reply)
	}
}

// /btw asks the selected thread a side question: recorded nowhere, no turn
// slot, no steer — on a running thread and an idle one alike, which is the
// app's /btw contract.
func TestControlRouterBtw(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	actions.btwAnswer = "It is running the suite."
	router := NewControlRouter(actions)
	unthrottle(router)

	// Without a selection, and with no question, nothing reaches a thread.
	if reply := router.Handle(ctx, "/btw what is it doing?"); !strings.Contains(reply, "No thread selected") {
		t.Fatalf("unselected reply = %q", reply)
	}
	if reply := router.Handle(ctx, "/btw"); !strings.Contains(reply, "Send /btw <question>") {
		t.Fatalf("bare /btw reply = %q, want usage", reply)
	}
	if len(actions.btws) != 0 {
		t.Fatalf("btws = %+v, want no ask yet", actions.btws)
	}

	// A running thread is asked without steering or turning it.
	router.Handle(ctx, "/use 2")
	if reply := router.Handle(ctx, "/btw what is it doing?"); reply != "Btw: It is running the suite." {
		t.Fatalf("btw reply = %q", reply)
	}
	if len(actions.btws) != 1 || actions.btws[0] != (controlTextCall{"t2", "what is it doing?"}) {
		t.Fatalf("btws = %+v", actions.btws)
	}
	if len(actions.turns)+len(actions.steers) != 0 {
		t.Fatal("/btw must not start a turn or steer, even on a running thread")
	}

	// An idle thread is just as askable; the app's /btw has no running gate.
	router.Handle(ctx, "/use t1")
	if reply := router.Handle(ctx, "/btw status?"); reply != "Btw: It is running the suite." {
		t.Fatalf("idle-thread reply = %q", reply)
	}

	// Action errors surface, and an empty answer is reported, not sent.
	actions.btwErr = errors.New("side questions are unavailable")
	if reply := router.Handle(ctx, "/btw again"); !strings.Contains(reply, "side questions are unavailable") {
		t.Fatalf("error reply = %q", reply)
	}
	actions.btwErr = nil
	actions.btwAnswer = "  "
	if reply := router.Handle(ctx, "/btw hi"); !strings.Contains(reply, "no answer") {
		t.Fatalf("empty answer reply = %q", reply)
	}
}

// A long side answer is clipped, so one stays a readable chat message.
func TestControlRouterBtwClipsLongAnswers(t *testing.T) {
	actions := newFakeActions()
	actions.btwAnswer = strings.Repeat("a", controlBtwMax+100)
	router := NewControlRouter(actions)
	unthrottle(router)
	router.Handle(context.Background(), "/use 1")

	if reply, want := router.Handle(context.Background(), "/btw tell me more"), "Btw: "+strings.Repeat("a", controlBtwMax)+"…"; reply != want {
		t.Fatalf("reply length = %d runes, want the clipped %d", len([]rune(reply)), len([]rune(want)))
	}
}

func TestControlRouterThreadsAndUnknownCommands(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	router := NewControlRouter(actions)
	unthrottle(router)
	router.Handle(ctx, "/use 1")

	reply := router.Handle(ctx, "/threads")
	for _, want := range []string{"1. Fix flaky test (selected)", "2. Write docs (running)", "/use <number>"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("threads reply = %q, want %q", reply, want)
		}
	}

	before := len(actions.turns) + len(actions.steers) + len(actions.answers)
	if reply := router.Handle(ctx, "/exec rm -rf /"); !strings.Contains(reply, "Unknown command") || !strings.Contains(reply, "/btw") {
		t.Fatalf("unknown command reply = %q, want the unknown-command notice naming /btw", reply)
	}
	if after := len(actions.turns) + len(actions.steers) + len(actions.answers); after != before {
		t.Fatal("an unknown slash command must not reach any action")
	}

	// A selection that disappeared is cleared with a useful reply.
	actions.threads = nil
	if reply := router.Handle(ctx, "hello"); !strings.Contains(reply, "gone") {
		t.Fatalf("vanished-thread reply = %q", reply)
	}
}

func TestControlRouterReportsActionErrors(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	actions.turnErr = errors.New("a turn is already running on t1")
	actions.steerErr = errors.New("steering is unavailable")
	actions.threadsErr = errors.New("engine is down")
	router := NewControlRouter(actions)

	if reply := router.Handle(ctx, "/threads"); !strings.Contains(reply, "engine is down") {
		t.Fatalf("threads error reply = %q", reply)
	}
	actions.threadsErr = nil
	router.Handle(ctx, "/use 1")
	if reply := router.Handle(ctx, "/turn go"); !strings.Contains(reply, "already running") {
		t.Fatalf("turn error reply = %q", reply)
	}
	if reply := router.Handle(ctx, "/steer go"); !strings.Contains(reply, "steering is unavailable") {
		t.Fatalf("steer error reply = %q", reply)
	}
}

// /use only accepts the numbers /threads printed, so a page-limited list cannot
// select a row the user never saw.
func TestControlRouterUseStaysWithinDisplayedPage(t *testing.T) {
	actions := &fakeControlActions{}
	for i := 0; i < controlListMax+2; i++ {
		actions.threads = append(actions.threads, ControlThread{
			ID:    contracts.ThreadID(fmt.Sprintf("t%d", i+1)),
			Title: fmt.Sprintf("Thread %d", i+1),
		})
	}
	router := NewControlRouter(actions)
	unthrottle(router)

	if reply := router.Handle(context.Background(), fmt.Sprintf("/use %d", controlListMax+1)); !strings.Contains(reply, "not in the list") {
		t.Fatalf("past-page /use reply = %q", reply)
	}
	if got := router.selection(); got != "" {
		t.Fatalf("selection = %q, want none after an unshown index", got)
	}
	if reply := router.Handle(context.Background(), fmt.Sprintf("/use %d", controlListMax)); !strings.Contains(reply, "Selected") {
		t.Fatalf("last-shown /use reply = %q", reply)
	}
}

func TestControlRouterRateLimitsInbound(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	router := NewControlRouter(actions)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	router.now = func() time.Time { return now }

	for i := 0; i < controlBurst; i++ {
		if reply := router.Handle(ctx, "/threads"); !strings.Contains(reply, "Threads:") {
			t.Fatalf("message %d in the burst replied %q", i, reply)
		}
	}
	if reply := router.Handle(ctx, "/threads"); !strings.Contains(reply, "Too many messages") {
		t.Fatalf("over-burst reply = %q, want the rate-limit notice", reply)
	}
	// A second drop inside the notice window stays silent.
	if reply := router.Handle(ctx, "/threads"); reply != "" {
		t.Fatalf("repeated over-burst reply = %q, want silence", reply)
	}

	now = now.Add(controlRefillInterval*2 + controlNoticeInterval)
	if reply := router.Handle(ctx, "/threads"); !strings.Contains(reply, "Threads:") {
		t.Fatalf("after refilling, reply = %q", reply)
	}
}

// The dispatcher implementation is the real action set: thread listing through
// thread.list (top-level, non-archived), steering through thread.steer, and
// answers through question.answer.
func newControlFixture(t *testing.T) (*domain.Engine, *Dispatcher) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:  eng,
		Logs:    logstore.New(t.TempDir()),
		Mailbox: contextstore.NewMailbox(),
	})
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Runner: runner, Steering: steering.New()})
	for _, c := range []contracts.Command{
		{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "Fix flaky test"},
		{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2", Title: "Write docs"},
		{ID: "c3", Type: contracts.CmdThreadCreate, ThreadID: "t3", Title: "Subthread", ParentThreadID: "t1"},
	} {
		if err := eng.Dispatch(ctx, c); err != nil {
			t.Fatalf("create thread: %v", err)
		}
	}
	return eng, disp
}

func controlMessageText(t *testing.T, eng *domain.Engine, thread, author string) string {
	t.Helper()
	st, err := eng.State(context.Background(), contracts.ThreadID(thread))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range st.Messages {
		if m.Author == author {
			return m.Text
		}
	}
	t.Fatalf("no %s message on %s", author, thread)
	return ""
}

func TestControlRouterDrivesDispatcher(t *testing.T) {
	ctx := context.Background()
	eng, disp := newControlFixture(t)
	router := NewControlRouter(disp)
	unthrottle(router)

	reply := router.Handle(ctx, "/threads")
	if !strings.Contains(reply, "Fix flaky test") || !strings.Contains(reply, "Write docs") {
		t.Fatalf("threads reply = %q, want both top-level threads", reply)
	}
	if strings.Contains(reply, "Subthread") {
		t.Fatalf("threads reply = %q, want subthreads omitted", reply)
	}

	// Archiving removes a thread from the chat's list.
	archived := true
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c4", Type: contracts.CmdArchiveSet, ThreadID: "t2", Archived: &archived,
	}); err != nil {
		t.Fatal(err)
	}
	if reply := router.Handle(ctx, "/threads"); strings.Contains(reply, "Write docs") {
		t.Fatalf("threads reply = %q, want the archived thread omitted", reply)
	}

	if reply := router.Handle(ctx, "/use t1"); !strings.Contains(reply, "Selected") {
		t.Fatalf("use reply = %q", reply)
	}
	// A running turn holds the thread's slot, so plain text and /turn are busy.
	_, release, err := disp.beginTurn(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if reply := router.Handle(ctx, "/turn go"); !strings.Contains(reply, "already running") {
		t.Fatalf("busy turn reply = %q", reply)
	}
	if reply := router.Handle(ctx, "please look at the logs"); !strings.Contains(reply, "Steering") {
		t.Fatalf("busy plain-text reply = %q, want steering", reply)
	}
	if got := controlMessageText(t, eng, "t1", "steering"); !strings.Contains(got, "please look at the logs") {
		t.Fatalf("steering message = %q, want the chat text recorded", got)
	}

	// An answer wakes the blocked ask and is recorded as the selected option.
	waiter := disp.questions.wait("t1", "q1")
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c5", Type: contracts.CmdQuestionAsk, ThreadID: "t1",
		QuestionAsk: &contracts.QuestionAsked{
			QuestionID: "q1", AgentID: "agent-hub", Kind: contracts.QuestionChoice,
			Question: "Ship it?", Options: []string{"yes", "no"},
		},
	}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if reply := router.Handle(ctx, "/answer 2"); !strings.Contains(reply, "Answered: no") {
		t.Fatalf("answer reply = %q", reply)
	}
	select {
	case <-waiter:
	default:
		t.Fatal("the answer did not wake the blocked ask")
	}
	if q := questionOf(t, eng, "t1", "q1"); q.Status != domain.QuestionAnswered ||
		len(q.Selected) != 1 || q.Selected[0] != "no" {
		t.Fatalf("question = %+v, want the selected option recorded", q)
	}
}

// A turn already holding the slot is refused before a second one is recorded.
func TestControlTurnRefusesBusyThread(t *testing.T) {
	ctx := context.Background()
	_, disp := newControlFixture(t)
	_, release, err := disp.beginTurn(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := disp.ControlTurn(ctx, "t1", "go"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("ControlTurn on a busy thread = %v, want the busy refusal", err)
	}
}

// /use master points plain text at Master, and /use <number> hands it back to a
// thread. Routing is sequential, so the order of these calls is the order the
// bridge's worker would run them.
func TestControlRouterUseMasterThenTextGoesToMaster(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	actions.masterResult = orchestrator.MasterResult{Reply: "Fix flaky test is running."}
	router := NewControlRouter(actions)
	unthrottle(router)

	if reply := router.Handle(ctx, "/use master"); !strings.Contains(reply, "Selected Master") {
		t.Fatalf("use master reply = %q", reply)
	}
	reply := router.Handle(ctx, "what is running?")
	if reply != "Master: Fix flaky test is running." {
		t.Fatalf("master reply = %q", reply)
	}
	if len(actions.masters) != 1 || actions.masters[0].text != "what is running?" {
		t.Fatalf("masters = %+v, want the text sent to Master", actions.masters)
	}
	if len(actions.turns)+len(actions.steers) != 0 {
		t.Fatal("plain text with Master selected must not act on a thread")
	}

	if reply := router.Handle(ctx, "/use 2"); !strings.Contains(reply, "Write docs") {
		t.Fatalf("use thread reply = %q", reply)
	}
	router.Handle(ctx, "please add examples")
	if len(actions.masters) != 1 {
		t.Fatalf("masters = %d after /use 2, want no further Master calls", len(actions.masters))
	}
	if len(actions.steers) != 1 || actions.steers[0] != (controlTextCall{"t2", "please add examples"}) {
		t.Fatalf("steers = %+v, want text back on the selected thread", actions.steers)
	}
}

// /master <text> is one-shot: it reaches Master without changing the selection.
func TestControlRouterMasterCommandIsOneShot(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	actions.masterResult = orchestrator.MasterResult{Reply: "Two threads are running."}
	router := NewControlRouter(actions)
	unthrottle(router)

	if reply := router.Handle(ctx, "/master"); !strings.Contains(reply, "Send /master <text>") || len(actions.masters) != 0 {
		t.Fatalf("empty /master reply = %q, masters = %d", reply, len(actions.masters))
	}
	if reply := router.Handle(ctx, "/master what is running?"); reply != "Master: Two threads are running." {
		t.Fatalf("master reply = %q", reply)
	}
	if len(actions.masters) != 1 || actions.masters[0].text != "what is running?" {
		t.Fatalf("masters = %+v", actions.masters)
	}
	if reply := router.Handle(ctx, "ship it"); !strings.Contains(reply, "No thread selected") {
		t.Fatalf("plain text after one-shot = %q, want the thread target unchanged", reply)
	}
}

// The chat carries its own short exchange with Master into the next question.
func TestControlRouterMasterKeepsChatHistory(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	actions.masterResult = orchestrator.MasterResult{Reply: "First answer."}
	router := NewControlRouter(actions)
	unthrottle(router)
	router.Handle(ctx, "/use master")

	router.Handle(ctx, "first question")
	actions.masterResult.Reply = "Second answer."
	router.Handle(ctx, "second question")

	if len(actions.masters) != 2 {
		t.Fatalf("masters = %d, want 2", len(actions.masters))
	}
	want := []orchestrator.MasterMessage{
		{Role: "user", Text: "first question"},
		{Role: "assistant", Text: "First answer."},
	}
	got := actions.masters[1].history
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("second history = %+v, want %+v", got, want)
	}
}

// Thread-only commands are refused while Master is the target, so a /steer
// cannot silently land on a thread the user is no longer looking at.
func TestControlRouterMasterTargetRefusesThreadCommands(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	actions.question = &ControlQuestion{ID: "q1", Text: "Ship it?"}
	router := NewControlRouter(actions)
	unthrottle(router)
	router.Handle(ctx, "/use 1")
	router.Handle(ctx, "/use master")

	for _, cmd := range []string{"/steer focus", "/turn go", "/answer yes", "/btw what is left?"} {
		if reply := router.Handle(ctx, cmd); !strings.Contains(reply, "Master is selected") {
			t.Fatalf("%s reply = %q, want the Master target named", cmd, reply)
		}
	}
	if len(actions.steers)+len(actions.turns)+len(actions.answers)+len(actions.btws)+len(actions.masters) != 0 {
		t.Fatalf("thread commands acted: steers=%+v turns=%+v answers=%+v btws=%+v masters=%+v",
			actions.steers, actions.turns, actions.answers, actions.btws, actions.masters)
	}
}

// A proposed thread is reported by title, and a proposal the daemon could not
// honor says why, so the chat never implies work started when it did not.
func TestControlRouterMasterReportsProposals(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	router := NewControlRouter(actions)
	unthrottle(router)

	actions.masterResult = orchestrator.MasterResult{
		Reply:    "Starting the docs pass.",
		Action:   &orchestrator.MasterAction{Title: "Docs pass"},
		ThreadID: "t9",
	}
	if reply := router.Handle(ctx, "/master start a docs pass"); !strings.Contains(reply, "Started a thread: Docs pass.") {
		t.Fatalf("started reply = %q", reply)
	}

	actions.masterResult = orchestrator.MasterResult{
		Reply: "Which project?",
		Error: `project "x" is not configured`,
	}
	if reply := router.Handle(ctx, "/master start it"); !strings.Contains(reply, `No thread was started: project "x" is not configured.`) {
		t.Fatalf("refused reply = %q", reply)
	}
}

func TestControlRouterMasterFailureReply(t *testing.T) {
	ctx := context.Background()
	actions := newFakeActions()
	actions.masterErr = errors.New("harness down")
	router := NewControlRouter(actions)
	unthrottle(router)

	if reply := router.Handle(ctx, "/master hello"); reply != "Master failed: harness down" {
		t.Fatalf("failure reply = %q", reply)
	}
}

// Through the real dispatcher, /master reaches the orchestrator's one-shot send
// and the scripted model's answer comes back in the chat.
func TestControlMasterAnswersThroughDispatcher(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := masterDispatchScript(filepath.Join(dir, "prompt.txt"), filepath.Join(dir, "release"), "Two threads are idle.")
	_, disp := newMasterRPCFixture(t, script)
	router := NewControlRouter(disp)
	unthrottle(router)

	if reply := router.Handle(ctx, "/master what is idle?"); reply != "Master: Two threads are idle." {
		t.Fatalf("master reply = %q", reply)
	}
}

// Through the real dispatcher, /btw rides thread.btw: the same answer the
// desktop's /btw gets, no turn slot taken, and nothing recorded — so it keeps
// working while a turn holds the thread.
func TestControlBtwAnswersThroughDispatcher(t *testing.T) {
	ctx := context.Background()
	eng, disp := newBtwFixture(t)
	router := NewControlRouter(disp)
	unthrottle(router)

	if reply := router.Handle(ctx, "/use 1"); !strings.Contains(reply, "Selected") {
		t.Fatalf("use reply = %q", reply)
	}

	// A turn holding the slot must not refuse the side question; the app's
	// /btw answers beside a running turn.
	_, release, err := disp.beginTurn(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before, _ := eng.State(ctx, "t1")
	if reply := router.Handle(ctx, "/btw what is it doing?"); reply != "Btw: ANSWER" {
		t.Fatalf("busy-thread btw reply = %q, want the scripted answer", reply)
	}
	after, _ := eng.State(ctx, "t1")
	if len(after.Messages) != len(before.Messages) {
		t.Fatalf("messages = %d, want unchanged at %d: a side question is recorded nowhere", len(after.Messages), len(before.Messages))
	}

	// A blank question is refused before the runner is reached, matching the
	// RPC's validation the desktop also sees.
	if reply := router.Handle(ctx, "/btw   "); !strings.Contains(reply, "Send /btw <question>") {
		t.Fatalf("blank question reply = %q", reply)
	}
}
