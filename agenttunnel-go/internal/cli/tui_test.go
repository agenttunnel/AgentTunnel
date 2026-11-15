package cli

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// fakeTuiTerm is the tuiTerminal tests draw on: a fixed size and a recorded
// stream of frames, each also delivered on a channel so tests wait for a
// specific paint instead of sleeping.
type fakeTuiTerm struct {
	mu     sync.Mutex
	frames []string
	drawn  chan string

	cols, rows int
}

func newFakeTuiTerm(cols, rows int) *fakeTuiTerm {
	return &fakeTuiTerm{drawn: make(chan string, 256), cols: cols, rows: rows}
}

func (f *fakeTuiTerm) Size() (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cols, f.rows, nil
}

func (f *fakeTuiTerm) Resize(cols, rows int) {
	f.mu.Lock()
	f.cols, f.rows = cols, rows
	f.mu.Unlock()
}

func (f *fakeTuiTerm) Draw(frame string) error {
	f.mu.Lock()
	f.frames = append(f.frames, frame)
	f.mu.Unlock()
	select {
	case f.drawn <- frame:
	default:
	}
	return nil
}

// waitNextDraw blocks until a frame painted from now on matches, skipping the
// non-matching frames in between. A timeout fails with the recent frames, so
// a stalled drive shows where the session actually was.
func (f *fakeTuiTerm) waitNextDraw(t *testing.T, want func(string) bool) string {
	t.Helper()
	for {
		select {
		case frame := <-f.drawn:
			if want(frame) {
				return frame
			}
		case <-time.After(5 * time.Second):
			f.mu.Lock()
			recent := f.frames
			f.mu.Unlock()
			if n := len(recent); n > 4 {
				recent = recent[n-4:]
			}
			for i, frame := range recent {
				t.Logf("painted before the wait gave up (%d):\n%s", i, frame)
			}
			t.Fatal("no matching frame was painted")
			return ""
		}
	}
}

func containsLine(part string) func(string) bool {
	return func(frame string) bool { return strings.Contains(frame, part) }
}

// tuiServer answers every method the TUI session uses for one thread with a
// running lead.
func tuiServer(threadID string, agentStatus string) *scriptedServer {
	s := newScriptedServer()
	s.results["thread.list"] = []threadListRow{{
		ID: threadID, Title: "the tui job", ProjectID: "prj_1",
		Agents: 1, RunningAgents: 1, UpdatedAt: time.Now(),
	}}
	s.results["project.list"] = []projectsRow{{ID: "prj_1", Name: "AgentTunnel", Path: "/tmp/at"}}
	s.results["thread.subscribe"] = map[string]any{"subscribed": threadID}
	s.results["thread.logs"] = []contracts.LogEntry{{
		AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "backlog line",
	}}
	s.results["agents.list"] = []map[string]any{
		{"agent": map[string]any{"id": "agent-orchestrator", "status": agentStatus}},
	}
	s.results["thread.steer"] = map[string]any{"ok": true}
	s.results["thread.turn.start"] = map[string]any{"ok": true}
	s.results["thread.turn.stop"] = map[string]any{"ok": true, "stopped": true}
	s.results["question.answer"] = map[string]any{"ok": true}
	return s
}

// typeLine sends a line of text followed by Enter.
func typeLine(keys chan<- tuiKey, line string) {
	for _, r := range line {
		keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	keys <- tuiKey{Kind: keyEnter}
}

// The full drive-through: the list, a thread's backlog and live output, an
// answered question, a steer, a stop, and the way back out. Every step waits
// on a painted frame or a recorded RPC, never on a timer.
func TestTUISessionDrivesAThreadEndToEnd(t *testing.T) {
	s := tuiServer("th_1", "running")
	c := dialScripted(t, s)
	term := newFakeTuiTerm(100, 24)
	keys := make(chan tuiKey, 32)
	m := newTuiModel(time.Now())

	done := make(chan error, 1)
	go func() {
		done <- tuiSession(context.Background(), c, m, "", keys, term)
	}()

	// The list paints the thread; Enter opens it.
	frame := term.waitNextDraw(t, containsLine("the tui job"))
	if !strings.Contains(frame, "Threads") {
		t.Fatalf("list frame missing its header:\n%s", frame)
	}
	keys <- tuiKey{Kind: keyEnter}

	frame = term.waitNextDraw(t, containsLine("backlog line"))
	for _, want := range []string{"the tui job", "AgentTunnel", "agent-orchestrator:running"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("thread frame missing %q:\n%s", want, frame)
		}
	}

	// Live output lands in the log pane, tailed at the bottom.
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
		AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "live from the daemon",
	}})
	s.push(t)
	term.waitNextDraw(t, containsLine("live from the daemon"))

	// A question owns the next line: the prompt switches and the answer goes
	// to question.answer.
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtQuestionAsked, payload: contracts.QuestionAsked{
		QuestionID: "q1", Kind: contracts.QuestionChoice,
		Question: "which database?", Options: []string{"postgres", "sqlite"},
	}})
	s.push(t)
	frame = term.waitNextDraw(t, containsLine("which database?"))
	for _, want := range []string{"1) postgres", "2) sqlite", "? "} {
		if !strings.Contains(frame, want) {
			t.Fatalf("question frame missing %q:\n%s", want, frame)
		}
	}
	typeLine(keys, "2")
	var answer map[string]any
	if err := json.Unmarshal(s.waitCall(t, "question.answer").params, &answer); err != nil {
		t.Fatalf("question.answer params: %v", err)
	}
	if answer["questionId"] != "q1" {
		t.Fatalf("answer params = %+v", answer)
	}
	selected, _ := answer["selected"].([]any)
	if len(selected) != 1 || selected[0] != "sqlite" {
		t.Fatalf("selected = %v, want sqlite", answer["selected"])
	}

	// On a running turn, a submitted line steers.
	typeLine(keys, "tighten the loop")
	var steer map[string]string
	if err := json.Unmarshal(s.waitCall(t, "thread.steer").params, &steer); err != nil {
		t.Fatalf("thread.steer params: %v", err)
	}
	if steer["threadId"] != "th_1" || steer["text"] != "tighten the loop" {
		t.Fatalf("steer params = %+v", steer)
	}

	// /stop stops the turn.
	typeLine(keys, "/stop")
	var stop map[string]string
	if err := json.Unmarshal(s.waitCall(t, "thread.turn.stop").params, &stop); err != nil {
		t.Fatalf("thread.turn.stop params: %v", err)
	}
	if stop["threadId"] != "th_1" {
		t.Fatalf("stop params = %+v", stop)
	}

	// /detach leaves the view the way Esc does, keeping the turn; the thread
	// reopens cleanly afterwards, and Esc then leaves the same way. (The
	// scripted backlog is static, so the tail's resumption is proven by the
	// next live frame, not by a replay the fixture cannot record.)
	typeLine(keys, "/detach")
	term.waitNextDraw(t, containsLine("Threads"))
	keys <- tuiKey{Kind: keyEnter}
	term.waitNextDraw(t, containsLine("backlog line"))
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
		AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "live again",
	}})
	s.push(t)
	term.waitNextDraw(t, containsLine("live again"))
	keys <- tuiKey{Kind: keyEsc}
	term.waitNextDraw(t, containsLine("Threads"))
	keys <- tuiKey{Kind: keyRune, Rune: 'q'}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tuiSession: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("q did not leave the session")
	}
}

// Enter on an idle thread starts a detached turn asynchronously rather than
// steering, and the start RPC carries Detach so it never blocks the UI.
func TestTUISessionStartsATurnOnAnIdleThread(t *testing.T) {
	s := tuiServer("th_1", "succeeded")
	s.results["thread.list"] = []threadListRow{{
		ID: "th_1", Title: "the next job", ProjectID: "prj_1", Agents: 1, UpdatedAt: time.Now(),
	}}
	c := dialScripted(t, s)
	term := newFakeTuiTerm(100, 24)
	keys := make(chan tuiKey, 32)
	m := newTuiModel(time.Now())

	done := make(chan error, 1)
	go func() {
		done <- tuiSession(context.Background(), c, m, "th_1", keys, term)
	}()

	// The ref argument opens the thread directly.
	frame := term.waitNextDraw(t, containsLine("the next job"))
	if !strings.Contains(frame, "agent-orchestrator:succeeded") {
		t.Fatalf("idle roster not shown:\n%s", frame)
	}

	typeLine(keys, "kick off the next run")
	var start runTurnStartParams
	if err := json.Unmarshal(s.waitCall(t, "thread.turn.start").params, &start); err != nil {
		t.Fatalf("thread.turn.start params: %v", err)
	}
	if start.ThreadID != "th_1" || start.Text != "kick off the next run" || !start.Detach {
		t.Fatalf("turn.start params = %+v, want a detached start on th_1", start)
	}
	s.callsMu.Lock()
	var steered bool
	for _, call := range s.calls {
		if call.method == "thread.steer" {
			steered = true
		}
	}
	s.callsMu.Unlock()
	if steered {
		t.Fatal("an idle thread must start a turn, not steer")
	}

	// Ctrl-C leaves the whole session, not just the thread view.
	keys <- tuiKey{Kind: keyCtrlC}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tuiSession: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not leave the session")
	}
}

func TestParseTUIArgs(t *testing.T) {
	ref, err := parseTuiArgs([]string{"th_1"})
	if err != nil || ref != "th_1" {
		t.Fatalf("parseTuiArgs(th_1) = %q, %v", ref, err)
	}
	if ref, err := parseTuiArgs(nil); err != nil || ref != "" {
		t.Fatalf("parseTuiArgs(nil) = %q, %v", ref, err)
	}
	for _, bad := range [][]string{{"-x"}, {"a", "b"}} {
		if _, err := parseTuiArgs(bad); err == nil {
			t.Fatalf("parseTuiArgs(%v) should fail", bad)
		}
	}
}

// A thread created while a registered tab is active opens on the thread
// surface: the open request switches the tab back, and the thread is never
// opened blind behind it. The create reply parks behind a gate so the tab
// switch happens mid-create, deterministically.
func TestTUISessionDraftOpensOnTheThreadSurface(t *testing.T) {
	// notesView loads without an RPC: the create reply parks the scripted
	// daemon's one connection mid-read, so a Load that called the daemon here
	// would itself park — the tab must stay switchable, which is the point.
	tuiTestRegistry(t, notesView())
	s := tuiServer("th_1", "idle")
	s.results["thread.list"] = []threadListRow{
		{ID: "th_1", Title: "the tui job", ProjectID: "prj_1", Agents: 1, UpdatedAt: time.Now()},
		{ID: "th_draft1", Title: "the draft thread", ProjectID: "prj_1", UpdatedAt: time.Now()},
	}
	s.results["thread.create"] = map[string]any{"threadId": "th_draft1"}
	release := s.gate("thread.create")

	c := dialScripted(t, s)
	term := newFakeTuiTerm(100, 24)
	keys := make(chan tuiKey, 32)
	m := newTuiModel(time.Now())

	done := make(chan error, 1)
	go func() {
		done <- tuiSession(context.Background(), c, m, "", keys, term)
	}()
	term.waitNextDraw(t, containsLine("the tui job"))

	// New thread: pick the project from the picker. thread.create's request
	// is recorded, then parks behind the gate.
	keys <- tuiKey{Kind: keyCtrlK}
	term.waitNextDraw(t, containsLine("Commands"))
	for _, r := range "new thread" {
		keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	term.waitNextDraw(t, containsLine("Threads: new thread"))
	keys <- tuiKey{Kind: keyEnter}
	term.waitNextDraw(t, containsLine("new thread in"))
	keys <- tuiKey{Kind: keyEnter} // the first project
	s.waitCall(t, "thread.create")
	if tuiCallCount(s, "thread.create") != 1 {
		t.Fatal("the draft was not created through thread.create")
	}

	// While the create is in flight, tab away to the registered view.
	keys <- tuiKey{Kind: keyTab}
	term.waitNextDraw(t, containsLine("notes body loaded"))

	// Release the reply: the draft opens, and the tab switches back with it.
	close(release)
	frame := term.waitNextDraw(t, containsLine("the draft thread"))
	if strings.Contains(frame, "notes body loaded") {
		t.Fatalf("the draft opened behind the notes tab:\n%s", frame)
	}
	if !strings.Contains(frame, "backlog line") {
		t.Fatalf("the draft view is missing its backlog:\n%s", frame)
	}

	keys <- tuiKey{Kind: keyCtrlC}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tuiSession: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not leave the session")
	}
}
