package cli

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiPlain strips whole-line ANSI styling so tests assert the text content.
var tuiANSI = regexp.MustCompile("\x1b\\[[0-9;]*m")

func tuiPlain(line string) string {
	return tuiANSI.ReplaceAllString(line, "")
}

// tuiPane is the content of one conversation row: the two-cell margin and
// the two-cell gutter stripped, trailing fill dropped, indentation kept.
func tuiPane(plain string) string {
	return strings.TrimRight(strings.TrimPrefix(plain, "    "), " ")
}

// tuiFrameLines renders and returns one plain line per screen row.
func tuiFrameLines(t *testing.T, m *tuiModel, cols, rows int) []string {
	t.Helper()
	return strings.Split(render(m, cols, rows), "\n")
}

// tuiEntry builds one log entry with a deterministic timestamp.
func tuiEntry(at time.Time, agent, text string) contracts.LogEntry {
	return contracts.LogEntry{AgentID: contracts.AgentID(agent), Kind: contracts.LogText, Text: text, At: at}
}

// tuiTestClock builds deterministic timestamps. writeLogEntry renders
// At.Local(), so constructing in the local zone keeps stamps stable on any
// machine.
func tuiTestClock() time.Time {
	return time.Date(2024, 1, 1, 10, 30, 0, 0, time.Local)
}

func TestTUIScreenListRender(t *testing.T) {
	now := tuiTestClock()
	newRow := func(id, title, parent string, running, total int, age time.Duration) psRow {
		return psRow{thread: threadListRow{
			ID: id, Title: title, ParentThreadID: parent,
			RunningAgents: running, Agents: total, UpdatedAt: now.Add(-age),
		}, project: "demo"}
	}

	tests := []struct {
		name   string
		rows   []psRow
		cursor int
		want   []string // plain lines that must each appear in the frame
	}{
		{
			name: "nested rows with selected parent",
			rows: []psRow{
				newRow("th_parent000001", "fix the bug", "", 1, 2, 5*time.Minute),
				newRow("th_child000001", "sub-thread", "th_parent000001", 0, 1, 30*time.Second),
			},
			cursor: 0,
			want: []string{
				"▌ ● th_parent000  fix the bug  demo  running  1/2  5m ago",
				"  ● th_child0000  └ sub-thread  demo  idle  0/1  30s ago",
				"Threads  2",
			},
		},
		{
			name: "selected child",
			rows: []psRow{
				newRow("th_parent000001", "fix the bug", "", 0, 2, 5*time.Minute),
				newRow("th_child000001", "", "th_parent000001", 1, 1, 30*time.Second),
			},
			cursor: 1,
			want: []string{
				"  ● th_parent000  fix the bug  demo  idle  0/2  5m ago",
				"▌ ● th_child0000  └ (untitled)  demo  running  1/1  30s ago",
			},
		},
		{
			name: "empty list",
			rows: nil,
			want: []string{"no threads yet; start one with `agenttunnel run <template>`"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := make([]psRow, len(tt.rows))
			copy(rows, tt.rows)
			for i := range rows {
				rows[i].status = threadRowStatus(rows[i].thread)
			}
			m := newTuiModel(now)
			m.setList(nestPsRows(rows))
			m.cursor = tt.cursor
			got := tuiFrameLines(t, m, 80, 8)
			if len(got) > 8 {
				t.Fatalf("render produced %d lines for an 8-row terminal", len(got))
			}
			frame := strings.Join(got, "\n")
			if frame == "" {
				t.Fatal("empty frame")
			}
			for _, want := range tt.want {
				found := false
				for _, line := range got {
					if strings.Contains(tuiPlain(line), want) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("frame missing line %q:\n%s", want, frame)
				}
			}
		})
	}
}

func TestTUIScreenListCursorWindow(t *testing.T) {
	now := tuiTestClock()
	rows := make([]psRow, 6)
	for i := range rows {
		rows[i] = psRow{thread: threadListRow{
			ID: fmt.Sprintf("th_row%d", i), Title: fmt.Sprintf("t%d", i),
			UpdatedAt: now, Agents: 1,
		}, project: "demo"}
		rows[i].status = threadRowStatus(rows[i].thread)
	}
	m := newTuiModel(now)
	m.setList(rows)
	m.moveCursor(5) // off-screen for a 3-row terminal (one visible body row)

	got := tuiFrameLines(t, m, 40, 3)
	if len(got) != 3 {
		t.Fatalf("frame lines = %d, want 3:\n%s", len(got), strings.Join(got, "\n"))
	}
	// The one visible body row must be the selected one.
	if plain := tuiPlain(got[1]); !strings.Contains(plain, "▌ ● th_row5") {
		t.Fatalf("visible row = %q, want the selected th_row5", plain)
	}
}

func TestTUIScreenThreadRender(t *testing.T) {
	now := tuiTestClock()
	at := func(sec int) time.Time { return time.Date(2024, 1, 1, 10, 0, sec, 0, time.Local) }
	m := newTuiModel(now)
	m.openThread(threadListRow{
		ID: "th_thread000001", Title: "job", RunningAgents: 1, Agents: 2,
	}, "demo", []contracts.LogEntry{
		tuiEntry(at(0), "lead", "hello world"),
		{AgentID: "lead", Kind: contracts.LogText, Text: "line1\nline2", At: at(1)},
	}, map[string]contracts.AgentStatus{
		"worker-1": contracts.AgentSucceeded,
		"lead":     contracts.AgentRunning,
	})
	for _, r := range "hi there" {
		m.typeRune(r)
	}

	// 10 rows: header, title bar, three log rows, roster, input (pad, text, pad), footer.
	got := tuiFrameLines(t, m, 60, 10)
	if len(got) != 10 {
		t.Fatalf("frame has %d lines, want 10:\n%s", len(got), strings.Join(got, "\n"))
	}
	if bar := tuiPlain(got[1]); !strings.Contains(bar, "job  demo  th_thread000") || !strings.Contains(bar, "running  1/2 agents") {
		t.Fatalf("title bar = %q", bar)
	}
	want := map[int]string{
		2: "10:00:00 lead text hello world",
		3: "10:00:01 lead text line1",
		4: "                   line2", // continuation rows keep the prefix column
		5: "  agents lead:running  worker-1:succeeded",
	}
	for i, text := range want {
		if pane := tuiPane(tuiPlain(got[i])); pane != text {
			t.Fatalf("line %d pane = %q, want %q", i, pane, text)
		}
	}
	if input := strings.TrimRight(tuiPlain(got[7]), " "); input != "┃ hi there▌" {
		t.Fatalf("input line = %q, want %q", input, "┃ hi there▌")
	}
	if footer := tuiPlain(got[9]); !strings.Contains(footer, "enter send") || !strings.Contains(footer, "esc back") {
		t.Fatalf("footer = %q, want the thread key hints", footer)
	}
}

func TestTUIScreenInputAndQuestion(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil, nil)

	for _, r := range "  yes " {
		m.typeRune(r)
	}
	if got := tuiPlain(render(m, 60, 8)); !strings.Contains(got, "┃   yes ") {
		t.Fatalf("typed input not shown:\n%s", got)
	}
	for i := 0; i < 4; i++ {
		m.backspace()
	}
	if line := string(m.input); line != "  " {
		t.Fatalf("after backspaces input = %q, want %q", line, "  ")
	}
	if got := m.takeInput(); got != "" {
		t.Fatalf("whitespace-only input submitted %q, want empty", got)
	}

	m.askQuestion(questionPromptEntry{
		id: "q1", kind: contracts.QuestionChoice, question: "Proceed?", options: []string{"yes", "no"},
	})
	for _, r := range "ok" {
		m.typeRune(r)
	}
	got := tuiFrameLines(t, m, 60, 8)
	found := map[string]bool{
		"? Proceed? (answer with a number)": false,
		"  1) yes  2) no":                   false,
		"? ok":                              false,
	}
	for _, line := range got {
		for want := range found {
			if strings.Contains(tuiPlain(line), want) {
				found[want] = true
			}
		}
	}
	for want, seen := range found {
		if !seen {
			t.Fatalf("question mode missing line %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
	if got := m.takeInput(); got != "ok" {
		t.Fatalf("takeInput = %q, want %q", got, "ok")
	}
	if len(m.input) != 0 {
		t.Fatal("takeInput did not clear the editor")
	}
	m.closeQuestion()
	if frame := tuiPlain(render(m, 60, 8)); strings.Contains(frame, "? Proceed?") {
		t.Fatalf("closed question still rendered:\n%s", frame)
	}
}

func TestTUIScreenScrollPinning(t *testing.T) {
	now := tuiTestClock()
	at := func(sec int) time.Time { return time.Date(2024, 1, 1, 10, 0, sec, 0, time.Local) }
	six := make([]contracts.LogEntry, 6)
	for i := range six {
		six[i] = tuiEntry(at(i), "lead", fmt.Sprintf("line %d", i))
	}
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", six, nil)

	// An 8-row terminal leaves 3 rows of log; following shows the newest lines.
	follow := tuiFrameLines(t, m, 60, 8)
	if plain := tuiPane(tuiPlain(follow[2])); plain != "10:00:03 lead text line 3" {
		t.Fatalf("follow pane top = %q", plain)
	}
	if plain := tuiPane(tuiPlain(follow[4])); plain != "10:00:05 lead text line 5" {
		t.Fatalf("follow pane bottom = %q", plain)
	}

	// Scroll up two: the window now ends at line 3.
	m.scrollUp(2)
	scrolled := tuiFrameLines(t, m, 60, 8)
	if plain := tuiPane(tuiPlain(scrolled[4])); plain != "10:00:03 lead text line 3" {
		t.Fatalf("scrolled pane bottom = %q", plain)
	}

	// New output while scrolled must not move the viewport: pinning.
	m.appendLog(tuiEntry(at(6), "lead", "line 6"), tuiEntry(at(7), "lead", "line 7"))
	pinned := tuiFrameLines(t, m, 60, 8)
	if plain := tuiPane(tuiPlain(pinned[4])); plain != "10:00:03 lead text line 3" {
		t.Fatalf("pinned pane bottom moved to %q", plain)
	}

	// Paging back down to the bottom resumes following the tail.
	m.scrollDown(4) // past the offset; clamps to zero
	if !m.following() {
		t.Fatalf("scroll = %d, want 0 (following)", m.scroll)
	}
	resumed := tuiFrameLines(t, m, 60, 8)
	if plain := tuiPane(tuiPlain(resumed[4])); plain != "10:00:07 lead text line 7" {
		t.Fatalf("resumed pane bottom = %q", plain)
	}
	m.appendLog(tuiEntry(at(8), "lead", "line 8"))
	live := tuiFrameLines(t, m, 60, 8)
	if plain := tuiPane(tuiPlain(live[4])); plain != "10:00:08 lead text line 8" {
		t.Fatalf("follow pane after append = %q", plain)
	}
}

func TestTUIScreenScrollClampAndIndicator(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{
		tuiEntry(tuiTestClock(), "lead", "only line"),
	}, nil)
	m.scrollUp(100)
	if m.scroll != 0 { // max scroll on a one-line buffer is zero
		t.Fatalf("scroll = %d, want 0", m.scroll)
	}
	m.appendLog(tuiEntry(tuiTestClock(), "lead", "second"), tuiEntry(tuiTestClock(), "lead", "third"))
	m.scrollUp(100)
	if m.scroll != 2 { // len-1
		t.Fatalf("scroll = %d, want 2", m.scroll)
	}
	frame := tuiPlain(render(m, 60, 8))
	if !strings.Contains(frame, "(scrolled 2 up)") {
		t.Fatalf("no scroll indicator in the input line:\n%s", frame)
	}
	// Scrolled to the top, the page shows the oldest line first.
	if !strings.Contains(frame, "text only line") {
		t.Fatalf("oldest line not visible at max scroll:\n%s", frame)
	}
}

func TestTUIScreenLogCap(t *testing.T) {
	now := tuiTestClock()
	total := tuiLogCap + 7
	entries := make([]contracts.LogEntry, total)
	for i := range entries {
		entries[i] = tuiEntry(tuiTestClock(), "lead", fmt.Sprintf("entry %03d", i))
	}
	// Backlog arrives first, live output after — the oldest lines fall off.
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", entries[:3], nil)
	m.appendLog(entries[3:]...)

	if len(m.log) != tuiLogCap {
		t.Fatalf("buffer holds %d lines, want the cap %d", len(m.log), tuiLogCap)
	}
	if m.log[0].text != "10:30:00 lead text entry 007" {
		t.Fatalf("oldest kept line = %q, want entry 007", m.log[0].text)
	}
	if m.log[tuiLogCap-1].text != "10:30:00 lead text entry 406" {
		t.Fatalf("newest line = %q, want entry 406", m.log[tuiLogCap-1].text)
	}
}

func TestTUIScreenTruncateAndWrap(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1234567890abc", Title: strings.Repeat("w", 40)}, "demo", []contracts.LogEntry{
		tuiEntry(now, "lead", strings.Repeat("y", 50)),
	}, nil)

	// 20 columns: every row is exactly 20 wide, the long title is cut, and the
	// 69-rune log line wraps at the box's 16-column content width.
	got := tuiFrameLines(t, m, 20, 12)
	for i, line := range got {
		if w := len([]rune(tuiPlain(line))); w != 20 {
			t.Fatalf("row %d is %d wide, want 20: %q", i, w, tuiPlain(line))
		}
	}
	line := tuiLogLine{text: "10:30:00 lead text " + strings.Repeat("y", 50), kind: "text", stamp: 9}
	rows := line.rows(16)
	if len(rows) != 5 {
		t.Fatalf("expected the 69-rune line to wrap to 5 rows (14 cells beside the gutter), got %d", len(rows))
	}
	var joined strings.Builder
	for _, row := range rows {
		if w := row.width(); w > 16 {
			t.Fatalf("wrapped row exceeds the pane width: %d", w)
		}
		joined.WriteString(strings.TrimPrefix(row.plain(), "  ")) // the gutter
	}
	if joined.String() != line.text {
		t.Fatalf("wrapped rows lost text: %q", joined.String())
	}

	// Frame level: the wrapped rows must reach the rendered box. The width
	// loop above only pins the box outline; this proves each wrapped chunk of
	// the stored entry really renders, at the box's 16-column content width.
	chunks := tuiWrap(line.text, 16)
	if len(chunks) != len(rows) {
		t.Fatalf("tuiWrap and rows disagree on the chunk count: %d vs %d", len(chunks), len(rows))
	}
	for _, chunk := range chunks {
		found := false
		for _, row := range got {
			if pane := tuiPane(tuiPlain(row)); pane == chunk {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("wrapped chunk %q missing from the rendered box:\n%s", chunk, tuiPlain(strings.Join(got, "\n")))
		}
	}
}

func TestTUIScreenBacklogRendersThroughWriteLogEntry(t *testing.T) {
	entry := contracts.LogEntry{AgentID: "a", Kind: contracts.LogText, Text: "hello", At: tuiTestClock()}
	var want strings.Builder
	writeLogEntry(&want, entry)

	m := newTuiModel(tuiTestClock())
	m.openThread(threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{entry}, nil)
	frame := tuiPlain(render(m, 60, 8))
	if !strings.Contains(frame, strings.TrimRight(want.String(), "\n")) {
		t.Fatalf("log pane line differs from writeLogEntry:\n%s", frame)
	}
}

func TestTUIScreenCloseThreadResets(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.setList([]psRow{{thread: threadListRow{ID: "th_1"}, project: "demo"}})
	m.openThread(threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{
		tuiEntry(now, "lead", "one"),
		tuiEntry(now, "lead", "two"),
		tuiEntry(now, "lead", "three"),
	}, nil)
	m.scrollUp(1)
	m.typeRune('x')
	m.askQuestion(questionPromptEntry{id: "q1"})

	m.closeThread()
	if m.screen != tuiScreenList {
		t.Fatal("closeThread did not return to the list screen")
	}
	if len(m.log) != 0 || m.scroll != 0 || m.pending != nil || len(m.input) != 0 {
		t.Fatalf("thread state survived close: log=%d scroll=%d pending=%v input=%q",
			len(m.log), m.scroll, m.pending != nil, string(m.input))
	}
	if frame := tuiPlain(render(m, 60, 8)); !strings.Contains(frame, "Threads") {
		t.Fatalf("list not rendered after close:\n%s", frame)
	}
}

// A typed line wider than the screen keeps its tail: the user types at the
// caret side, so the head is cut to an ellipsis — the same rule the dialog
// prompt follows.
func TestTUIScreenInputKeepsTheTailVisible(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil, nil)
	for _, r := range strings.Repeat("word ", 20) + "END" {
		m.typeRune(r)
	}
	got := tuiFrameLines(t, m, 30, 8)
	input := strings.TrimPrefix(tuiPlain(got[len(got)-2]), "┃ ")
	if !strings.HasPrefix(input, "…") || !strings.HasSuffix(strings.TrimRight(input, " "), "END▌") {
		t.Fatalf("input row = %q, want an ellipsis head and the typed tail", input)
	}
	if w := stringCells(tuiPlain(got[len(got)-2])); w > 30 {
		t.Fatalf("input row is %d cells wide, over 30", w)
	}
	// The scrolled note stays visible even on a full line: the cut takes the
	// typed head, never the note at the caret-side end.
	m.appendLog(tuiEntry(now, "lead", "one"), tuiEntry(now, "lead", "two"))
	m.scrollUp(1)
	input = tuiPlain(strings.Split(render(m, 30, 8), "\n")[len(got)-2])
	if !strings.Contains(input, "END") || !strings.Contains(input, "(scrolled 1 up)") {
		t.Fatalf("scrolled note lost on a full input row: %q", input)
	}
}

// Agent output is untrusted: ESC and other control bytes must never reach
// the terminal raw, where they would move the cursor or clear the frame. Tab
// stays, since logs indent with it.
func TestTUIScreenStripsControlBytesFromLogText(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{
		tuiEntry(now, "lead", "a\x1b[2Jb\rc\x07"),
	}, nil)
	m.appendLog(tuiEntry(now, "lead", "x\ty"))
	frame := render(m, 60, 8)
	if strings.Contains(frame, "\x1b") {
		t.Fatalf("an ESC byte reached the frame:\n%q", frame)
	}
	plain := tuiPlain(frame)
	if !strings.Contains(plain, "a[2Jbc") {
		t.Fatalf("the log line lost its non-control runes:\n%s", plain)
	}
	if !strings.Contains(plain, "x\ty") {
		t.Fatalf("tab was stripped from the log line:\n%q", plain)
	}
}

// A stored line wraps once per width: the wrapped rows are memoized on the
// line, so a repaint — or the pane the same line feeds — reuses them instead
// of re-splitting up to the log cap every frame.
func TestTUILogLineWrapsOncePerWidth(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{
		tuiEntry(now, "lead", strings.Repeat("z", 60)),
	}, nil)
	render(m, 60, 8)
	m.log[0].wrapped[0] = tuiText("CACHED", tuiInk{})
	if got := m.log[0].rows(58)[0].plain(); got != "CACHED" {
		t.Fatalf("rows at the same width re-wrapped instead of reusing the cache: %q", got)
	}
	after := m.log[0].rows(20)
	if len(after) == 0 || after[0].plain() == "CACHED" {
		t.Fatal("a new width did not re-wrap the line")
	}
	if m.log[0].wrapW != 20 {
		t.Fatalf("cache width = %d, want 20", m.log[0].wrapW)
	}
}

// paneRows is pure: rendering settles an out-of-range offset in view without
// writing it back, and two renders of the same model paint the same bytes.
func TestTUIPaneRenderIsPureAndSettlesTop(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil, nil)
	m.openPane("agent log", tuiLogLines(
		tuiEntry(now, "lead", "one"),
		tuiEntry(now, "lead", "two"),
	), true)
	first := render(m, 60, 8)
	if second := render(m, 60, 8); second != first {
		t.Fatalf("two renders of one pane differ:\n%s\n---\n%s", first, second)
	}

	// A top scrolled far past the end settles at the newest row in view, but
	// the pane's own offset is never rewritten by the render.
	m.pane.follow = false
	m.pane.top = 1 << 20
	clamped := render(m, 60, 8)
	if m.pane.top != 1<<20 {
		t.Fatal("paneRows wrote the clamped offset back into the pane")
	}
	if clamped != first {
		t.Fatalf("a pane clamped in view shows different rows than the follow view:\n%s\n---\n%s", clamped, first)
	}
}
