package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiSessionRun starts a session against s and returns the pieces a test
// drives. The session is left with Ctrl-C when the test ends.
type tuiSessionRun struct {
	s    *scriptedServer
	c    *Client
	term *fakeTuiTerm
	keys chan tuiKey
	m    *tuiModel
	done chan error
}

// startTuiSession's optional last argument is the directory the TUI was
// started in.
func startTuiSession(t *testing.T, s *scriptedServer, ref string, term tuiTerminal, redial func(context.Context) (*Client, error), cwd ...string) *tuiSessionRun {
	t.Helper()
	run := &tuiSessionRun{s: s, keys: make(chan tuiKey, 64), m: newTuiModel(time.Now()), done: make(chan error, 1)}
	switch tt := term.(type) {
	case *fakeTuiTerm:
		run.term = tt
	case *tuiLoudTerm:
		run.term = tt.fakeTuiTerm
	}
	// Opening a thread marks it read in the background; left unanswered, the
	// refusal would land in the transcript at a moment no test controls.
	s.mu.Lock()
	if _, ok := s.results["thread.markSeen"]; !ok {
		s.results["thread.markSeen"] = map[string]any{"ok": true}
	}
	s.mu.Unlock()
	c := dialScripted(t, s)
	run.c = c
	if len(cwd) > 0 {
		run.m.cwd = cwd[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		run.done <- tuiSession(ctx, c, run.m, ref, run.keys, term, redial)
	}()
	t.Cleanup(func() {
		run.keys <- tuiKey{Kind: keyCtrlC}
		select {
		case err := <-run.done:
			if err != nil {
				t.Errorf("tuiSession: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Ctrl-C did not leave the session")
		}
		cancel() // releases a redial still waiting for the daemon
	})
	return run
}

// tuiLoudTerm is a terminal that also takes the window title and the
// notifications, each on a channel a test can wait on.
type tuiLoudTerm struct {
	*fakeTuiTerm
	titles chan string
	notes  chan string
	copies chan string
}

func newTuiLoudTerm(cols, rows int) *tuiLoudTerm {
	return &tuiLoudTerm{
		fakeTuiTerm: newFakeTuiTerm(cols, rows),
		titles:      make(chan string, 64), notes: make(chan string, 64), copies: make(chan string, 64),
	}
}

func (l *tuiLoudTerm) SetTitle(title string) { l.titles <- title }
func (l *tuiLoudTerm) Notify(text string)    { l.notes <- text }
func (l *tuiLoudTerm) Copy(text string)      { l.copies <- text }

// Edit plays an editor that appends a line to what it was given.
func (l *tuiLoudTerm) Edit(text string, done func(string, error)) error {
	go done(text+"\nadded in the editor\n", nil)
	return nil
}

func tuiWaitFor(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	for {
		select {
		case got := <-ch:
			if got == want {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("never saw %q", want)
		}
	}
}

// What the user sends is part of the conversation: the prompt comes back on
// the stream and reads in line, and the reply that streams in chunks reads as
// one passage under the agent's name, not a row per chunk.
func TestTUISessionShowsTheConversation(t *testing.T) {
	s := tuiServer("th_1", "running")
	s.results["thread.get"] = map[string]any{"messages": []map[string]any{
		{"id": "m0", "author": "user", "text": "the earlier ask", "at": time.Now().Add(-time.Minute)},
	}}
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	frame := run.term.waitNextDraw(t, containsLine("the earlier ask"))
	if !strings.Contains(frame, "backlog line") {
		t.Fatalf("the backlog lost the agent's output:\n%s", frame)
	}

	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtMessageSent, payload: contracts.MessageSent{
		MessageID: "m1", Author: "user", Text: "tighten the loop",
	}})
	// The same message twice, as a reconnect or a late backlog would deliver it.
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtMessageSent, payload: contracts.MessageSent{
		MessageID: "m1", Author: "user", Text: "tighten the loop",
	}})
	for _, chunk := range []string{"Sure, ", "I will ", "tighten it."} {
		s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
			AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: chunk,
		}})
	}
	s.push(t)
	frame = run.term.waitNextDraw(t, containsLine("Sure, I will tighten it."))
	if got := strings.Count(frame, "┃ tighten the loop"); got != 1 {
		t.Fatalf("the user's message appears %d times, want once:\n%s", got, frame)
	}
	if !strings.Contains(frame, "you") {
		t.Fatalf("the user's message is not named:\n%s", frame)
	}
}

// A paste goes into the composer whole: its line breaks survive to the
// prompt and to the daemon.
func TestTUISessionPasteKeepsItsLineBreaks(t *testing.T) {
	s := tuiServer("th_1", "running")
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	run.keys <- tuiKey{Kind: keyPaste, Text: "line one\r\nline two"}
	frame := run.term.waitNextDraw(t, containsLine("┃ line two▌"))
	if !strings.Contains(frame, "┃ line one") {
		t.Fatalf("the pasted lines did not land on their own rows:\n%s", frame)
	}
	run.keys <- tuiKey{Kind: keyEnter}
	var steer map[string]string
	if err := json.Unmarshal(s.waitCall(t, "thread.steer").params, &steer); err != nil {
		t.Fatal(err)
	}
	if steer["text"] != "line one\nline two" {
		t.Fatalf("steer text = %q, want both lines", steer["text"])
	}
}

// Alt+Enter starts a new line and Enter sends; Up recalls the prompt just
// sent, the way a shell does.
func TestTUISessionComposerNewlineAndHistory(t *testing.T) {
	s := tuiServer("th_1", "running")
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	for _, r := range "first" {
		run.keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	run.keys <- tuiKey{Kind: keyNewline}
	typeLine(run.keys, "second")
	var steer map[string]string
	if err := json.Unmarshal(s.waitCall(t, "thread.steer").params, &steer); err != nil {
		t.Fatal(err)
	}
	if steer["text"] != "first\nsecond" {
		t.Fatalf("steer text = %q", steer["text"])
	}
	run.keys <- tuiKey{Kind: keyUp}
	run.term.waitNextDraw(t, containsLine("┃ second▌"))
}

// A tab opened from a thread sits over it: the thread stays subscribed, output
// that arrives meanwhile is in the transcript on the way back, and so is the
// half-typed prompt.
func TestTUISessionKeepsTheThreadUnderATab(t *testing.T) {
	tuiTestRegistry(t, notesView())
	s := tuiServer("th_1", "running")
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	for _, r := range "half typed" {
		run.keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	run.keys <- tuiKey{Kind: keyTab}
	run.term.waitNextDraw(t, containsLine("notes body loaded"))

	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
		AgentID: "agent-orchestrator", Kind: contracts.LogTool, Text: "$ ran while away",
	}})
	s.push(t)
	// A key the tab owns proves the frame after it was painted with the
	// event already applied or still queued; the thread frame settles it.
	run.keys <- tuiKey{Kind: keyRune, Rune: 'x'}
	run.term.waitNextDraw(t, containsLine("notes body x-pressed"))

	run.keys <- tuiKey{Kind: keyEsc}
	frame := run.term.waitNextDraw(t, containsLine("$ ran while away"))
	for _, want := range []string{"backlog line", "half typed▌"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("back on the thread, %q is gone:\n%s", want, frame)
		}
	}
}

// A dropped connection does not end the session: the header says the daemon
// is away, and once it answers again the open thread is subscribed and read
// afresh, with the typed prompt untouched.
func TestTUISessionReconnectsAfterADrop(t *testing.T) {
	s := tuiServer("th_1", "running")
	// The daemon comes back once, on a second listener, when back closes. The
	// session owns the client it is handed and closes it on the way out.
	again := httptest.NewServer(s)
	t.Cleanup(again.Close)
	back := make(chan struct{})
	var dialed atomic.Bool
	redial := func(ctx context.Context) (*Client, error) {
		if dialed.Swap(true) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		select {
		case <-back:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return Dial(ctx, "ws"+strings.TrimPrefix(again.URL, "http"), "")
	}
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), redial)
	run.term.waitNextDraw(t, containsLine("backlog line"))
	for _, r := range "keep me" {
		run.keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	run.term.waitNextDraw(t, containsLine("keep me▌"))

	s.mu.Lock()
	for conn := range s.conns {
		conn.CloseNow()
	}
	s.mu.Unlock()
	run.term.waitNextDraw(t, containsLine("reconnecting"))

	// While it was away the daemon's backlog moved on.
	s.mu.Lock()
	s.results["thread.logs"] = []contracts.LogEntry{
		{AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "backlog line\n"},
		{AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "written while offline"},
	}
	s.mu.Unlock()
	subscribed := tuiCallCount(s, "thread.subscribe")
	close(back)
	waitMoreCalls(t, s, "thread.subscribe", subscribed)
	frame := run.term.waitNextDraw(t, containsLine("written while offline"))
	if strings.Contains(frame, "reconnecting") {
		t.Fatalf("the header still says reconnecting:\n%s", frame)
	}
	if !strings.Contains(frame, "keep me▌") {
		t.Fatalf("the typed prompt did not survive the reconnect:\n%s", frame)
	}

	// The new connection carries the live stream.
	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentOutput, payload: contracts.AgentOutput{
		AgentID: "agent-orchestrator", Kind: contracts.LogTool, Text: "$ live again",
	}})
	s.push(t)
	run.term.waitNextDraw(t, containsLine("$ live again"))
}

// The window title names the open thread and marks it while it works, and a
// user who is looking elsewhere is told when the turn ends or an agent asks.
func TestTUISessionTitlesAndNotifies(t *testing.T) {
	s := tuiServer("th_1", "running")
	term := newTuiLoudTerm(100, 30)
	run := startTuiSession(t, s, "th_1", term, nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))
	tuiWaitFor(t, term.titles, "● the tui job · AgentTunnel")

	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtQuestionAsked, payload: contracts.QuestionAsked{
		QuestionID: "q1", Kind: contracts.QuestionChoice, Question: "which database?", Options: []string{"postgres", "sqlite"},
	}})
	s.push(t)
	tuiWaitFor(t, term.notes, "the tui job asks: which database?")

	// Down then an empty Enter answers with the highlighted option.
	run.term.waitNextDraw(t, containsLine("▸ 1) postgres"))
	run.keys <- tuiKey{Kind: keyDown}
	run.term.waitNextDraw(t, containsLine("▸ 2) sqlite"))
	run.keys <- tuiKey{Kind: keyEnter}
	var answer map[string]any
	if err := json.Unmarshal(s.waitCall(t, "question.answer").params, &answer); err != nil {
		t.Fatal(err)
	}
	if selected, _ := answer["selected"].([]any); len(selected) != 1 || selected[0] != "sqlite" {
		t.Fatalf("selected = %v, want sqlite", answer["selected"])
	}

	s.queue(threadFrame{threadID: "th_1", typ: contracts.EvtAgentStatusChanged, payload: contracts.AgentStatusChanged{
		AgentID: "agent-orchestrator", Status: contracts.AgentSucceeded,
	}})
	s.push(t)
	tuiWaitFor(t, term.notes, "the tui job finished")
	tuiWaitFor(t, term.titles, "the tui job · AgentTunnel")
}

// Ctrl+G writes the prompt in the user's editor and brings back what they
// saved; /copy puts the newest reply on the clipboard through the terminal.
func TestTUISessionEditorAndCopy(t *testing.T) {
	s := tuiServer("th_1", "running")
	term := newTuiLoudTerm(100, 30)
	run := startTuiSession(t, s, "th_1", term, nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	for _, r := range "draft" {
		run.keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	run.keys <- tuiKey{Kind: keyEditor}
	frame := run.term.waitNextDraw(t, containsLine("┃ added in the editor▌"))
	if !strings.Contains(frame, "┃ draft") {
		t.Fatalf("the editor did not start from the typed draft:\n%s", frame)
	}

	run.keys <- tuiKey{Kind: keyKillLine}
	run.keys <- tuiKey{Kind: keyBackspace}
	run.keys <- tuiKey{Kind: keyKillLine}
	typeLine(run.keys, "/copy")
	tuiWaitFor(t, term.copies, "backlog line")
	run.term.waitNextDraw(t, containsLine("copied the last reply"))
}

// /delete asks which way to delete, then goes back to the list.
func TestTUISessionDeleteThread(t *testing.T) {
	s := tuiServer("th_1", "idle")
	s.results["thread.delete"] = map[string]any{"ok": true}
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	typeLine(run.keys, "/delete")
	run.term.waitNextDraw(t, containsLine("Delete thread and its worktree"))
	run.keys <- tuiKey{Kind: keyDown}
	run.keys <- tuiKey{Kind: keyEnter}
	var params struct {
		ThreadID       string `json:"threadId"`
		RemoveWorktree bool   `json:"removeWorktree"`
	}
	if err := json.Unmarshal(s.waitCall(t, "thread.delete").params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "th_1" || !params.RemoveWorktree {
		t.Fatalf("thread.delete params = %+v", params)
	}
	run.term.waitNextDraw(t, containsLine("deleted thread"))
}

// Keys in the composer are text and editing, nothing else: q and : type, the
// readline keys edit, the wheel scrolls rows, and Tab leaves for the tabs only
// when it has no slash command to complete.
func TestTUIThreadKeysEditScrollAndSwitch(t *testing.T) {
	tuiTestRegistry(t, notesView())
	now := tuiTestClock()
	m := newTuiModel(now)
	lines := make([]contracts.LogEntry, 30)
	for i := range lines {
		lines[i] = contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "row", At: now}
	}
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", lines, nil)
	render(m, 60, 12)
	var tracker *turnTracker
	var sent []string
	press := func(keys ...tuiKey) {
		for _, k := range keys {
			tuiThreadKey(m, &tracker, k, 12, func(line string) { sent = append(sent, line) })
		}
	}

	press(tuiKey{Kind: keyRune, Rune: 'q'}, tuiKey{Kind: keyRune, Rune: ':'})
	if m.screen != tuiScreenThread || m.palette != nil || m.input.string() != "q:" {
		t.Fatalf("q and : did not type: screen=%v palette=%v input=%q", m.screen, m.palette != nil, m.input.string())
	}
	m.input.set("alpha beta gamma")
	press(tuiKey{Kind: keyKillWord})
	if got := m.input.string(); got != "alpha beta " {
		t.Fatalf("ctrl+w left %q", got)
	}
	press(tuiKey{Kind: keyHome}, tuiKey{Kind: keyWordRight}, tuiKey{Kind: keyRune, Rune: '!'}, tuiKey{Kind: keyEnd}, tuiKey{Kind: keyKillLine})
	if got := m.input.string(); got != "" {
		t.Fatalf("home, word right, type, end, ctrl+u left %q, want it cleared", got)
	}

	press(tuiKey{Kind: keyWheelUp})
	if m.scroll != tuiWheelRows {
		t.Fatalf("one wheel notch scrolled %d rows, want %d", m.scroll, tuiWheelRows)
	}
	press(tuiKey{Kind: keyEnd}) // an empty prompt: End follows the tail again
	if !m.following() {
		t.Fatalf("End on an empty prompt left scroll at %d", m.scroll)
	}

	m.input.set("/re")
	press(tuiKey{Kind: keyTab})
	if m.view != tuiThreadsViewID || !strings.HasPrefix(m.input.string(), "/re") || m.input.string() == "/re" {
		t.Fatalf("Tab with a slash prefix must complete it: view=%q input=%q", m.view, m.input.string())
	}
	m.input.set("plain text")
	press(tuiKey{Kind: keyTab})
	if m.view != "notes" || m.screen != tuiScreenThread {
		t.Fatalf("Tab without a slash prefix must move to the next tab over the thread: view=%q screen=%v", m.view, m.screen)
	}
	if len(sent) != 0 {
		t.Fatalf("editing keys submitted %q", sent)
	}
}

// "/" narrows the thread list as it is typed; the cursor and Enter work on
// the rows shown, and Esc brings every row back.
func TestTUIListFilterNarrowsTheRows(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	m.setList([]psRow{
		{thread: threadListRow{ID: "th_a", Title: "fix the parser"}, project: "compiler"},
		{thread: threadListRow{ID: "th_b", Title: "write release notes"}, project: "docs"},
		{thread: threadListRow{ID: "th_c", Title: "parser fuzzing"}, project: "compiler"},
	})
	if !tuiListFilterKey(m, tuiKey{Kind: keyRune, Rune: '/'}) || !m.filtering() {
		t.Fatal("/ did not start the filter")
	}
	for _, r := range "pars" {
		tuiListFilterKey(m, tuiKey{Kind: keyRune, Rune: r})
	}
	shown := m.listRows()
	if len(shown) != 2 || shown[0].thread.ID != "th_a" || shown[1].thread.ID != "th_c" {
		t.Fatalf("filter 'pars' shows %+v", shown)
	}
	frame := tuiPlain(render(m, 80, 12))
	if !strings.Contains(frame, "/pars▌") || strings.Contains(frame, "release notes") {
		t.Fatalf("filtered list frame:\n%s", frame)
	}
	if tuiListFilterKey(m, tuiKey{Kind: keyDown}) {
		t.Fatal("the filter swallowed an arrow key")
	}
	m.moveCursor(5)
	if row, ok := m.selected(); !ok || row.thread.ID != "th_c" {
		t.Fatalf("selected = %+v, want the last shown row", row.thread)
	}
	tuiListFilterKey(m, tuiKey{Kind: keyEnter})
	if m.filtering() || m.filter == nil {
		t.Fatal("Enter must keep the filter and hand the keys back to the list")
	}
	tuiListFilterKey(m, tuiKey{Kind: keyEsc})
	if m.filter != nil || len(m.listRows()) != 3 {
		t.Fatal("Esc did not clear the filter")
	}
}

func TestTUIFuzzyMatchesInOrderAndRanksSubstrings(t *testing.T) {
	if _, ok := tuiFuzzy("nt", "Threads: new thread"); !ok {
		t.Fatal("nt should match 'new thread' as a subsequence")
	}
	if _, ok := tuiFuzzy("tn", "ant"); ok {
		t.Fatal("runes out of order must not match")
	}
	if _, ok := tuiFuzzy("", "anything"); !ok {
		t.Fatal("an empty query matches everything")
	}
	order := tuiFuzzyOrder("thr", []string{"the hard row", "Thread: rename", "zebra"})
	if len(order) != 2 || order[0] != 1 {
		t.Fatalf("order = %v, want the substring match first and the non-match dropped", order)
	}
	// Equal matches keep the order they were listed in.
	if order := tuiFuzzyOrder("notes", []string{"Show Notes", "Notes: clear"}); len(order) != 2 || order[0] != 0 {
		t.Fatalf("order = %v, want listed order among equals", order)
	}
}

// The agents are drawn as the tree they form, each under the agent that
// spawned it, with a clock on the ones still working.
func TestTUISidebarDrawsTheAgentTree(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	m.setRoster(tuiRoster{
		statuses: map[string]contracts.AgentStatus{
			"lead": contracts.AgentRunning, "exec-2": contracts.AgentSucceeded, "exec-1": contracts.AgentRunning, "helper": contracts.AgentPending,
		},
		started: map[string]time.Time{"lead": now.Add(-70 * time.Second), "exec-1": now.Add(-9 * time.Second)},
		parent:  map[string]string{"exec-1": "lead", "exec-2": "lead", "helper": "exec-1"},
		role:    map[string]string{"lead": "orchestrator", "exec-1": "executor"},
	})
	var got []string
	for _, row := range m.agentTreeRows(40) {
		got = append(got, row.plain())
	}
	want := []string{
		"● lead  running 1m10s",
		"  orchestrator",
		"└ ● exec-1  running 9s",
		"    executor",
		"  └ ● helper  pending",
		"└ ● exec-2  succeeded",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("agent tree:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Cell widths follow what terminals draw: combining marks, joiners and
// variation selectors take no cell of their own, so a row with them stays
// aligned.
func TestTUICellWidthsOfZeroWidthRunes(t *testing.T) {
	for text, want := range map[string]int{
		"é":   1, // e + combining acute
		"❤️":   1, // heart + variation selector: the selector adds nothing
		"a‍b":  2, // zero width joiner
		"─●…▌": 4, // the frame's own glyphs are one cell each
		"中文":   4,
	} {
		if got := stringCells(text); got != want {
			t.Errorf("stringCells(%q) = %d, want %d", text, got, want)
		}
	}
}

// The cached escape pair is the same on every call, wraps the text and
// nothing else, and carries the colors at the depth asked for.
func TestTUIThemeCachedStyleIsStable(t *testing.T) {
	const text = "some words — and 中文"
	ink := tuiInk{fg: tuiColAccent, bg: tuiColBar, bold: true}
	for depth, want := range map[tuiDepth]string{
		tuiDepthTrueColor: "38;2;250;178;131",
		tuiDepth256:       "38;5;216",
	} {
		theme := tuiTheme{depth: depth}
		first := theme.style(text, ink)
		for range 2 { // the second pass reads the cache
			if got := theme.style(text, ink); got != first {
				t.Fatalf("depth %d: style changed between calls: %q vs %q", depth, got, first)
			}
		}
		if !strings.Contains(first, want) || !strings.Contains(first, ";1m") && !strings.Contains(first, "[1;") {
			t.Fatalf("depth %d: %q lacks %s or bold", depth, first, want)
		}
		if !strings.Contains(first, text) || !strings.HasSuffix(first, "\x1b[0m") {
			t.Fatalf("depth %d: %q does not wrap the text and reset", depth, first)
		}
	}
	if got := (tuiTheme{}).style(text, ink); got != text {
		t.Fatalf("a plain theme styled the text: %q", got)
	}
}

// A repaint after the palette changes renders blocks again: the rows a block
// remembers carry the colors of the palette they were built in.
func TestTUIRepaintAllDropsRememberedRows(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{tuiEntry(now, "lead", "hello")}, nil)
	render(m, 60, 10)
	if m.log[0].wrapped == nil {
		t.Fatal("the block did not remember its rows")
	}
	m.repaintAll()
	if m.log[0].wrapped != nil {
		t.Fatal("repaintAll left remembered rows in place")
	}
}

// A light terminal gets dark text, and a desktop theme lends its accent. The
// palette is package state, so the test puts it back.
func TestTUIPaletteForLightTerminalsAndSavedThemes(t *testing.T) {
	saved := []*tuiRGB{&tuiColBar, &tuiColBorder, &tuiColText, &tuiColDim, &tuiColFaint, &tuiColAccent,
		&tuiColAccent2, &tuiColAgent, &tuiColOK, &tuiColWarn, &tuiColErr, &tuiColInk, &tuiColSelect}
	values := make([]tuiRGB, len(saved))
	for i, p := range saved {
		values[i] = *p
	}
	harness := map[string]tuiRGB{}
	for name, c := range tuiHarnessColors {
		harness[name] = c
	}
	t.Cleanup(func() {
		for i, p := range saved {
			*p = values[i]
		}
		for name, c := range harness {
			tuiHarnessColors[name] = c
		}
	})
	luma := func(c tuiRGB) int { return (299*int(c.r) + 587*int(c.g) + 114*int(c.b)) / 1000 }

	applyTuiPalette(false, "nord")
	if tuiColAccent != rgb(0x88c0d0) || tuiColSelect != tuiColAccent {
		t.Fatalf("nord accent on a dark terminal = %v (select %v)", tuiColAccent, tuiColSelect)
	}
	if luma(tuiColText) < 180 {
		t.Fatalf("dark terminal text is not light: %v", tuiColText)
	}
	// The palette is rebuilt from its own values each time, so an unknown theme
	// brings the default accent back instead of keeping the last theme's.
	applyTuiPalette(false, "no-such-theme")
	if tuiColAccent != tuiDarkColors.accent {
		t.Fatalf("an unknown theme left accent %v, want the default", tuiColAccent)
	}

	applyTuiPalette(true, "warp")
	if luma(tuiColText) > 80 || luma(tuiColBar) < 200 {
		t.Fatalf("light terminal palette: text %v on panel %v", tuiColText, tuiColBar)
	}
	if tuiColAccent != rgb(0x0a7f84) {
		t.Fatalf("warp accent on a light terminal = %v", tuiColAccent)
	}
	for name, c := range tuiHarnessColors {
		if luma(c) > 160 {
			t.Fatalf("harness tint %s stays a pastel on a light terminal: %v", name, c)
		}
	}
}

func TestTUINotifyTextCannotBreakOutOfItsSequence(t *testing.T) {
	if got := tuiNotifyText("job\x07\x1b]0;evil\x07; done\n"); strings.ContainsAny(got, "\x07\x1b;\n") {
		t.Fatalf("notify text kept a control byte or a separator: %q", got)
	}
}
