package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiTestRegistry swaps the view registry for the test's own, restored on
// cleanup, so a test view never leaks into another test's tab order.
func tuiTestRegistry(t *testing.T, views ...tuiView) {
	t.Helper()
	saved := tuiViewRegistry
	tuiViewRegistry = nil
	t.Cleanup(func() { tuiViewRegistry = saved })
	for _, v := range views {
		registerTuiView(v)
	}
}

// notesView is a registered tab: it loads a flag, consumes 'x' into its own
// state, and renders both, so tests can see each hook run.
func notesView() tuiView {
	return tuiView{
		ID:    "notes",
		Title: "Notes",
		Load: func(_ context.Context, _ cliCaller, m *tuiModel) {
			m.viewState["notes"] = "loaded"
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			body := "notes body"
			if s, ok := m.viewState["notes"].(string); ok {
				body += " " + s
			}
			return []tuiRow{tuiText(body, tuiInk{})}
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			if k.Kind == keyRune && k.Rune == 'x' {
				m.viewState["notes"] = "x-pressed"
				return true
			}
			return false
		},
		Actions: []tuiAction{{Title: "Notes: clear", Run: func(m *tuiModel) {
			m.viewState["notes"] = "cleared"
		}}},
	}
}

func TestTUIViewRegistryRejectsBadRegistrations(t *testing.T) {
	tuiTestRegistry(t, notesView())
	for name, v := range map[string]tuiView{
		"empty id":     {Title: "x", Render: func(*tuiModel, int, int) []tuiRow { return nil }},
		"threads id":   {ID: tuiThreadsViewID, Title: "x", Render: func(*tuiModel, int, int) []tuiRow { return nil }},
		"no render":    {ID: "bare", Title: "x"},
		"duplicate id": notesView(),
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("registerTuiView accepted an invalid view")
				}
			}()
			registerTuiView(v)
		})
	}
}

func TestTUIViewTabsCycleAndRender(t *testing.T) {
	tuiTestRegistry(t, notesView())
	m := newTuiModel(tuiTestClock())
	if got := tuiViewOrder(); len(got) != 2 || got[0] != tuiThreadsViewID || got[1] != "notes" {
		t.Fatalf("tab order = %q", got)
	}

	m.cycleView(1)
	if m.view != "notes" {
		t.Fatalf("cycle forward: view = %q, want notes", m.view)
	}
	frame := tuiPlain(render(m, 60, 8))
	if !strings.Contains(frame, " Notes ") || !strings.Contains(frame, "notes body") {
		t.Fatalf("notes tab not drawn:\n%s", frame)
	}
	if !strings.Contains(frame, "tab views") {
		t.Fatalf("view footer missing the tab hint:\n%s", frame)
	}

	m.cycleView(1)
	if m.view != tuiThreadsViewID {
		t.Fatalf("cycle wrap: view = %q, want the thread list", m.view)
	}
	m.cycleView(-1)
	if m.view != "notes" {
		t.Fatalf("cycle back: view = %q, want notes", m.view)
	}
}

// Switching tabs keeps an open thread open underneath: the tab reads that
// thread, and coming back finds the transcript and the draft as they were.
func TestTUIViewSwitchKeepsTheOpenThread(t *testing.T) {
	tuiTestRegistry(t, notesView())
	now := tuiTestClock()
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_open0000001", Title: "the open job"}, "demo",
		[]contracts.LogEntry{tuiEntry(now, "lead", "still here")}, nil)
	for _, r := range "half typed" {
		m.typeRune(r)
	}
	m.switchView("notes")
	if m.screen != tuiScreenThread || m.thread.ID != "th_open0000001" {
		t.Fatalf("the tab switch closed the thread: screen=%v thread=%q", m.screen, m.thread.ID)
	}
	if row, ok := m.focusRow(); !ok || row.thread.ID != "th_open0000001" {
		t.Fatalf("the tab does not read the open thread: %+v", row.thread)
	}
	if frame := tuiPlain(render(m, 80, 12)); !strings.Contains(frame, "notes body") || strings.Contains(frame, "still here") {
		t.Fatalf("the tab is not the one on screen:\n%s", frame)
	}
	m.switchView(tuiThreadsViewID)
	frame := tuiPlain(render(m, 80, 12))
	for _, want := range []string{"the open job", "still here", "half typed"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("back on the thread, %q is gone:\n%s", want, frame)
		}
	}
}

func TestTUIPaletteFiltersRunsAndCloses(t *testing.T) {
	tuiTestRegistry(t, notesView())
	m := newTuiModel(tuiTestClock())

	m.openPalette()
	if got := len(m.paletteMatches()); got < 4 {
		t.Fatalf("palette lists %d actions, want the built-ins plus the view's", got)
	}
	for _, r := range "notes" {
		tuiPaletteKey(m, tuiKey{Kind: keyRune, Rune: r})
	}
	titles := []string{}
	for _, a := range m.paletteMatches() {
		titles = append(titles, a.Title)
	}
	if strings.Join(titles, "|") != "Show Notes|Notes: clear" {
		t.Fatalf("filter 'notes' matched %q", titles)
	}

	// Enter runs the highlighted action and closes the palette.
	tuiPaletteKey(m, tuiKey{Kind: keyEnter})
	if m.palette != nil || m.view != "notes" {
		t.Fatalf("enter did not run Show Notes: palette=%v view=%q", m.palette != nil, m.view)
	}

	// Esc closes without running.
	m.openPalette()
	tuiPaletteKey(m, tuiKey{Kind: keyRune, Rune: 'q'})
	tuiPaletteKey(m, tuiKey{Kind: keyEsc})
	if m.palette != nil || m.quit {
		t.Fatalf("esc should close without running: palette=%v quit=%v", m.palette != nil, m.quit)
	}

	// A filter with no match keeps the palette open and Enter does nothing.
	m.openPalette()
	for _, r := range "zzz" {
		tuiPaletteKey(m, tuiKey{Kind: keyRune, Rune: r})
	}
	tuiPaletteKey(m, tuiKey{Kind: keyEnter})
	if m.palette == nil {
		t.Fatal("enter on an empty match list closed the palette")
	}
}

func TestTUIPaletteModalKeepsTheScreenOnEitherSide(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	m.info = tuiInfo{daemon: "127.0.0.1:7777"}
	m.openPalette()
	frame := strings.Split(render(m, 80, 14), "\n")
	joined := tuiPlain(strings.Join(frame, "\n"))
	if !strings.Contains(joined, "Commands") || !strings.Contains(joined, "Quit") {
		t.Fatalf("palette not drawn:\n%s", joined)
	}
	if !strings.Contains(tuiPlain(frame[0]), "AgentTunnel") {
		t.Fatalf("header hidden under the palette: %q", tuiPlain(frame[0]))
	}
	for i, line := range frame {
		if w := len([]rune(tuiPlain(line))); w != 80 && i != len(frame)-1 {
			t.Fatalf("row %d is %d wide under the palette, want 80", i, w)
		}
	}
}

// A thread that gained messages while idle is unread; the first sighting and
// a running thread are not; opening then closing a thread reads it.
func TestTUIUnreadMarker(t *testing.T) {
	now := tuiTestClock()
	row := func(id string, messages, running int) psRow {
		r := psRow{thread: threadListRow{ID: id, Title: id, Messages: messages, RunningAgents: running, Agents: 1, UpdatedAt: now}, project: "demo"}
		r.status = threadRowStatus(r.thread)
		return r
	}
	m := newTuiModel(now)
	m.setList([]psRow{row("th_seed000001", 2, 0)})
	if m.unreadCount() != 0 {
		t.Fatal("first sighting marked history as unread")
	}

	m.setList([]psRow{row("th_seed000001", 3, 0)})
	if m.unreadCount() != 1 {
		t.Fatal("a grown idle thread is not unread")
	}
	if line := tuiPlain(m.listRow(row("th_seed000001", 3, 0), 80, false).plain()); !strings.Contains(line, "unread") {
		t.Fatalf("unread thread row lacks the marker: %q", line)
	}

	m.setList([]psRow{row("th_seed000001", 4, 1)})
	if m.unreadCount() != 0 {
		t.Fatal("a running thread counts as unread")
	}

	m.setList([]psRow{row("th_seed000001", 4, 0)})
	tuiOpenWith(m, threadListRow{ID: "th_seed000001", Messages: 4}, "demo", nil, nil)
	m.setList([]psRow{row("th_seed000001", 5, 0)})
	if m.unreadCount() != 0 {
		t.Fatal("the open thread is unread")
	}
	m.closeThread()
	m.setList([]psRow{row("th_seed000001", 5, 0)})
	if m.unreadCount() != 0 {
		t.Fatal("reading a thread on close did not mark it read")
	}
}

func tuiCallCount(s *scriptedServer, method string) int {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

// waitMoreCalls blocks until the daemon has seen method more than before times.
func waitMoreCalls(t *testing.T, s *scriptedServer, method string, before int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for tuiCallCount(s, method) <= before {
		select {
		case <-s.recorded:
		case <-deadline:
			t.Fatalf("no new %s call after %d", method, before)
		}
	}
}

// The session drives tabs, a registered view's own keys and load hook, and the
// palette end to end. Every step waits on a painted frame or a recorded RPC.
func TestTUISessionTabsViewsAndPalette(t *testing.T) {
	tuiTestRegistry(t, notesView())
	s := tuiServer("th_1", "running")
	c := dialScripted(t, s)
	term := newFakeTuiTerm(100, 24)
	keys := make(chan tuiKey, 32)
	m := newTuiModel(time.Now())

	done := make(chan error, 1)
	go func() {
		done <- tuiSession(context.Background(), c, m, "", keys, term, nil)
	}()
	term.waitNextDraw(t, containsLine("the tui job"))

	// Tab opens the registered view: its Load runs, its body draws.
	keys <- tuiKey{Kind: keyTab}
	term.waitNextDraw(t, containsLine("notes body loaded"))

	// The view consumes 'x' before the global keys see it.
	keys <- tuiKey{Kind: keyRune, Rune: 'x'}
	term.waitNextDraw(t, containsLine("notes body x-pressed"))

	// Esc returns to the thread list.
	keys <- tuiKey{Kind: keyEsc}
	term.waitNextDraw(t, containsLine("the tui job"))

	// Ctrl-K opens the palette; a refresh action re-reads the thread list.
	before := tuiCallCount(s, "thread.list")
	keys <- tuiKey{Kind: keyCtrlK}
	term.waitNextDraw(t, containsLine("Commands"))
	for _, r := range "refresh" {
		keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	term.waitNextDraw(t, containsLine("Threads: refresh"))
	keys <- tuiKey{Kind: keyEnter}
	waitMoreCalls(t, s, "thread.list", before)

	// Colon opens the palette on the list too; Esc closes it again.
	keys <- tuiKey{Kind: keyRune, Rune: ':'}
	term.waitNextDraw(t, containsLine("Commands"))
	keys <- tuiKey{Kind: keyEsc}
	term.waitNextDraw(t, func(f string) bool { return !strings.Contains(f, "Commands") })

	// The palette's Quit leaves the whole session.
	keys <- tuiKey{Kind: keyRune, Rune: ':'}
	term.waitNextDraw(t, containsLine("Commands"))
	for _, r := range "quit" {
		keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	keys <- tuiKey{Kind: keyEnter}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tuiSession: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("palette Quit did not leave the session")
	}
}

// The tab order is deliberate, not file-init luck: Threads first, then the
// app views by their Order field.
func TestTUIViewOrderIsDeliberate(t *testing.T) {
	want := "" // the thread list
	for _, id := range []string{"master", "files", "review", "pr", "usage", "settings", "remote", "projects", "issues", "worktrees"} {
		want += "," + id
	}
	if got := strings.Join(tuiViewOrder(), ","); got != want {
		t.Fatalf("tab order = %q, want %q", got, want)
	}
}

// A thread a background job asked to open (a fresh draft) lands on the thread
// surface even when a registered tab is active, never behind it.
func TestTUIOpenRequestSwitchesBackFromAViewTab(t *testing.T) {
	tuiTestRegistry(t, notesView())
	m := newTuiModel(tuiTestClock())
	m.setList([]psRow{{thread: threadListRow{ID: "th_new0000001"}, project: "demo"}})
	m.switchView("notes")
	m.openID = "th_new0000001"

	if id := m.takeOpenID(); id != "th_new0000001" {
		t.Fatalf("takeOpenID = %q, want the pending id", id)
	}
	if m.openID != "" {
		t.Fatal("takeOpenID left the request pending")
	}
	if m.view != tuiThreadsViewID {
		t.Fatalf("view = %q after taking the open request, want the threads list", m.view)
	}
	// A thread that opens normally, with no tab active, must not close one.
	m.view = tuiThreadsViewID
	m.openID = "th_2"
	if id := m.takeOpenID(); id != "th_2" || m.view != tuiThreadsViewID {
		t.Fatalf("a plain open request disturbed the surface: id=%q view=%q", id, m.view)
	}
}
