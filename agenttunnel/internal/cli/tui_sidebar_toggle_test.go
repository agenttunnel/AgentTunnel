package cli

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// sidebarToggleModel is an open thread with a roster, so the sidebar has
// agents to draw. One role name is wide (two cells a rune) to hold the width
// math to cells, not runes.
func sidebarToggleModel() *tuiModel {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	m.setRoster(tuiRoster{
		statuses: map[string]contracts.AgentStatus{
			"lead": contracts.AgentRunning, "exec-1": contracts.AgentSucceeded,
		},
		started: map[string]time.Time{"lead": now.Add(-time.Minute)},
		parent:  map[string]string{"exec-1": "lead"},
		role:    map[string]string{"lead": "orchestrator", "exec-1": "执行者"},
	})
	return m
}

func tuiPlainFrame(rows []tuiRow) string {
	lines := make([]string, len(rows))
	for i, row := range rows {
		lines[i] = row.plain()
	}
	return strings.Join(lines, "\n")
}

func tuiHasHint(hints []tuiHint, key string) bool {
	for _, h := range hints {
		if h.key == key {
			return true
		}
	}
	return false
}

// The width decides whether the sidebar shows, and ctrl+b flips that either
// way: a wide terminal hides it on request, a narrow one shows it on request.
func TestTUISidebarToggleFollowsTheWidthDefault(t *testing.T) {
	const rows = 20
	cases := []struct {
		name       string
		cols       int
		wantShown  bool
		wantAfterK bool
	}{
		{name: "wide starts shown", cols: 120, wantShown: true, wantAfterK: false},
		{name: "narrow starts hidden", cols: 80, wantShown: false, wantAfterK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := sidebarToggleModel()
			shown := func() bool {
				return strings.Contains(tuiPlainFrame(m.renderThread(tc.cols, rows)), "Agents")
			}
			if got := shown(); got != tc.wantShown {
				t.Fatalf("default at %d cols: sidebar shown=%v, want %v", tc.cols, got, tc.wantShown)
			}
			if got := m.hints(tc.cols, rows); tuiHasHint(got, "ctrl+b") != !tc.wantShown {
				t.Fatalf("hint bar names ctrl+b=%v at default; the hint belongs only to a hidden sidebar", !tc.wantShown)
			}

			tuiThreadKey(m, new(*turnTracker), tuiKey{Kind: keyCtrlB}, rows, func(string) {})
			if got := shown(); got != tc.wantAfterK {
				t.Fatalf("after ctrl+b at %d cols: sidebar shown=%v, want %v", tc.cols, got, tc.wantAfterK)
			}
			if got := m.hints(tc.cols, rows); tuiHasHint(got, "ctrl+b") != !tc.wantAfterK {
				t.Fatalf("after ctrl+b at %d cols: hint names ctrl+b=%v, want %v", tc.cols, !tc.wantAfterK, !tc.wantAfterK)
			}

			tuiThreadKey(m, new(*turnTracker), tuiKey{Kind: keyCtrlB}, rows, func(string) {})
			if got := shown(); got != tc.wantShown {
				t.Fatalf("second ctrl+b at %d cols: sidebar shown=%v, want the default %v", tc.cols, got, tc.wantShown)
			}
		})
	}
}

// At a narrow width the shown sidebar takes the body but leaves the composer
// usable, so the user can still type while reading the agents.
func TestTUISidebarNarrowPaneKeepsTheComposer(t *testing.T) {
	m := sidebarToggleModel()
	tuiThreadKey(m, new(*turnTracker), tuiKey{Kind: keyCtrlB}, 20, func(string) {})
	m.input.set("draft")
	frame := tuiPlainFrame(m.renderThread(80, 20))
	if !strings.Contains(frame, "draft") {
		t.Fatalf("the composer vanished under the narrow sidebar:\n%s", frame)
	}
	if !strings.Contains(frame, "┃") {
		t.Fatalf("no composer gutter drawn under the narrow sidebar:\n%s", frame)
	}
}

// Every row fills exactly the width it is given, whichever way the sidebar is
// set, and wide runes count as two cells: a row that drifts breaks the frame.
func TestTUISidebarRowsFillTheirWidth(t *testing.T) {
	const rows = 20
	for _, cols := range []int{20, 40, 60, 80, 109, 110, 120, 150} {
		for _, choice := range []tuiSideChoice{tuiSideAuto, tuiSideShown, tuiSideHidden} {
			m := sidebarToggleModel()
			m.side.choice = choice
			for i, row := range m.renderThread(cols, rows) {
				if got := row.width(); got != cols {
					t.Fatalf("cols %d choice %d: row %d is %d cells, want %d: %q",
						cols, choice, i, got, cols, row.plain())
				}
			}
		}
	}
}

// The key itself, through the session's key channel and the painted frames:
// a narrow terminal shows the sidebar on ctrl+b and hides it again, and the
// hint names the key only while the sidebar is hidden.
func TestTUISidebarToggleKeyThroughTheSession(t *testing.T) {
	t.Run("narrow", func(t *testing.T) {
		s := tuiServer("th_1", "running")
		run := startTuiSession(t, s, "th_1", newFakeTuiTerm(80, 30), nil)
		frame := run.term.waitNextDraw(t, containsLine("backlog line"))
		if strings.Contains(frame, "Agents") || !strings.Contains(frame, "ctrl+b sidebar") {
			t.Fatalf("a narrow thread should hide the sidebar and name the key:\n%s", frame)
		}

		run.keys <- tuiKey{Kind: keyCtrlB}
		frame = run.term.waitNextDraw(t, containsLine("Agents"))
		if strings.Contains(frame, "ctrl+b sidebar") {
			t.Fatalf("the shown sidebar still offers the toggle hint:\n%s", frame)
		}

		run.keys <- tuiKey{Kind: keyCtrlB}
		frame = run.term.waitNextDraw(t, func(f string) bool {
			return strings.Contains(f, "ctrl+b sidebar") && !strings.Contains(f, "Agents")
		})
		if strings.Contains(frame, "Agents") {
			t.Fatalf("the second ctrl+b did not hide the sidebar:\n%s", frame)
		}
	})

	t.Run("wide", func(t *testing.T) {
		s := tuiServer("th_1", "running")
		run := startTuiSession(t, s, "th_1", newFakeTuiTerm(120, 30), nil)
		run.term.waitNextDraw(t, containsLine("Agents"))

		run.keys <- tuiKey{Kind: keyCtrlB}
		frame := run.term.waitNextDraw(t, containsLine("ctrl+b sidebar"))
		if strings.Contains(frame, "Agents") {
			t.Fatalf("ctrl+b did not hide the wide sidebar:\n%s", frame)
		}
		if !strings.Contains(frame, "backlog line") {
			t.Fatalf("the chat lost its transcript when the sidebar hid:\n%s", frame)
		}
	})
}

// An explicit ctrl+b choice survives a resize; auto follows the width. The
// model is painted at each size the way the session paints it.
func TestTUISidebarChoiceSurvivesResize(t *testing.T) {
	const rows = 20
	shownAt := func(m *tuiModel, cols int) bool {
		return strings.Contains(tuiPlainFrame(m.renderThread(cols, rows)), "Agents")
	}

	t.Run("shown on a narrow terminal stays shown when widened", func(t *testing.T) {
		m := sidebarToggleModel()
		shownAt(m, 80)
		tuiThreadKey(m, new(*turnTracker), tuiKey{Kind: keyCtrlB}, rows, func(string) {})
		for _, cols := range []int{120, 80, 120} {
			if !shownAt(m, cols) {
				t.Fatalf("the sidebar the user showed hid at %d cols", cols)
			}
		}
	})

	t.Run("hidden on a wide terminal stays hidden when narrowed", func(t *testing.T) {
		m := sidebarToggleModel()
		shownAt(m, 120)
		tuiThreadKey(m, new(*turnTracker), tuiKey{Kind: keyCtrlB}, rows, func(string) {})
		for _, cols := range []int{80, 120, 80} {
			if shownAt(m, cols) {
				t.Fatalf("the sidebar the user hid came back at %d cols", cols)
			}
		}
	})

	t.Run("auto follows the width across a resize", func(t *testing.T) {
		m := sidebarToggleModel()
		for _, tc := range []struct {
			cols int
			want bool
		}{{120, true}, {80, false}, {120, true}} {
			if got := shownAt(m, tc.cols); got != tc.want {
				t.Fatalf("auto at %d cols: shown=%v, want %v", tc.cols, got, tc.want)
			}
		}
	})
}

// The fake terminal signals no resize, so a key forces the paint that reads the
// new size. The first line's rune count is the frame's width.
func TestTUISidebarChoiceSurvivesResizeThroughTheSession(t *testing.T) {
	widthIs := func(cols int, want func(string) bool) func(string) bool {
		return func(f string) bool {
			return utf8.RuneCountInString(strings.SplitN(f, "\n", 2)[0]) == cols && want(f)
		}
	}
	s := tuiServer("th_1", "running")
	term := newFakeTuiTerm(80, 30)
	run := startTuiSession(t, s, "th_1", term, nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	run.keys <- tuiKey{Kind: keyCtrlB}
	run.term.waitNextDraw(t, containsLine("Agents"))

	term.Resize(120, 30)
	run.keys <- tuiKey{Kind: keyLeft}
	frame := run.term.waitNextDraw(t, widthIs(120, containsLine("Agents")))
	if !strings.Contains(frame, "Agents") {
		t.Fatalf("the sidebar the user showed hid when the terminal widened:\n%s", frame)
	}

	run.keys <- tuiKey{Kind: keyCtrlB}
	run.term.waitNextDraw(t, widthIs(120, containsLine("ctrl+b sidebar")))

	term.Resize(80, 30)
	run.keys <- tuiKey{Kind: keyLeft}
	frame = run.term.waitNextDraw(t, widthIs(80, containsLine("ctrl+b sidebar")))
	if strings.Contains(frame, "Agents") {
		t.Fatalf("the sidebar the user hid came back when the terminal narrowed:\n%s", frame)
	}
}

// The narrow pane paints no transcript, so it must not leave the last wide
// paint's page height behind for scrolling to measure against.
func TestTUINarrowPaneRecordsItsGeometry(t *testing.T) {
	const rows = 20
	m := sidebarToggleModel()
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "line"
	}
	m.appendLogLines(lines...)

	m.renderThread(80, rows) // hidden at 80 cols: a painted transcript sets logH
	tuiThreadKey(m, new(*turnTracker), tuiKey{Kind: keyCtrlB}, rows, func(string) {})
	frame := tuiPlainFrame(m.renderThread(80, rows))
	if !strings.Contains(frame, "Agents") {
		t.Fatalf("the narrow pane did not show the sidebar:\n%s", frame)
	}
	if m.logH != 0 {
		t.Fatalf("narrow pane page height = %d, want 0 (no transcript is painted)", m.logH)
	}
	if want := 80 - 2; m.logW != want {
		t.Fatalf("narrow pane width = %d, want %d", m.logW, want)
	}

	m.scroll = 1 << 20
	m.clampScroll()
	if want := max(m.logRows()-1, 0); m.scroll != want {
		t.Fatalf("scroll clamped to %d, want %d: scrolling measured a transcript that is not painted", m.scroll, want)
	}
}

// The hint names ctrl+b only where the sidebar can actually show, so a very
// short terminal is never told about a toggle that does nothing.
func TestTUISidebarHintOmittedWhereTheSidebarCannotShow(t *testing.T) {
	for _, cols := range []int{80, 120} {
		m := sidebarToggleModel()
		if tuiHasHint(m.hints(cols, tuiSideMinRows-1), "ctrl+b") {
			t.Fatalf("%d cols, %d rows: hint names ctrl+b where the sidebar cannot show", cols, tuiSideMinRows-1)
		}
		m.side.choice = tuiSideHidden
		if tuiHasHint(m.hints(cols, tuiSideMinRows-1), "ctrl+b") {
			t.Fatalf("%d cols, %d rows, sidebar hidden by choice: hint names ctrl+b", cols, tuiSideMinRows-1)
		}
	}
	m := sidebarToggleModel()
	if !tuiHasHint(m.hints(80, tuiSideMinRows), "ctrl+b") {
		t.Fatalf("%d rows at 80 cols: the hidden sidebar's hint is missing", tuiSideMinRows)
	}
}
