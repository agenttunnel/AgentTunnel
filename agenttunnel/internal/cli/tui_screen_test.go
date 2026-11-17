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
// the two-cell gutter (blank, or a kind's bar) stripped, trailing fill
// dropped, indentation kept.
func tuiPane(plain string) string {
	plain = strings.TrimPrefix(plain, "  ")
	if rest, ok := strings.CutPrefix(plain, "┃ "); ok {
		plain = rest
	} else {
		plain = strings.TrimPrefix(plain, "  ")
	}
	return strings.TrimRight(plain, " ")
}

// tuiFrameLines renders and returns one plain line per screen row.
func tuiFrameLines(t *testing.T, m *tuiModel, cols, rows int) []string {
	t.Helper()
	return strings.Split(render(m, cols, rows), "\n")
}

// tuiOpenWith opens a thread the way the session does: the view first, then
// the backlog folded into its transcript.
func tuiOpenWith(m *tuiModel, row threadListRow, project string, backlog []contracts.LogEntry, statuses map[string]contracts.AgentStatus) {
	m.openThread(row, project, statuses)
	m.appendLog(backlog...)
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
			want: []string{"press n to start a thread"},
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

// tuiPaneRows is the transcript part of a thread frame, one string per row
// with the margin and gutter stripped and blank rows dropped.
func tuiPaneRows(frame []string, from, to int) []string {
	var out []string
	for _, line := range frame[from:to] {
		if row := tuiPane(tuiPlain(line)); row != "" {
			out = append(out, row)
		}
	}
	return out
}

func TestTUIScreenThreadRender(t *testing.T) {
	now := tuiTestClock()
	at := func(sec int) time.Time { return time.Date(2024, 1, 1, 10, 0, sec, 0, time.Local) }
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{
		ID: "th_thread000001", Title: "job", RunningAgents: 1, Agents: 2,
	}, "demo", []contracts.LogEntry{
		tuiEntry(at(0), "lead", "hello world\n"),
		{AgentID: "lead", Kind: contracts.LogText, Text: "line1\nline2", At: at(1)},
	}, map[string]contracts.AgentStatus{
		"worker-1": contracts.AgentSucceeded,
		"lead":     contracts.AgentRunning,
	})
	for _, r := range "hi there" {
		m.typeRune(r)
	}

	// 12 rows: header, title bar, the speaker row and three text rows, a blank,
	// the status line, input (pad, text, pad), footer.
	got := tuiFrameLines(t, m, 60, 12)
	if len(got) != 12 {
		t.Fatalf("frame has %d lines, want 12:\n%s", len(got), strings.Join(got, "\n"))
	}
	if bar := tuiPlain(got[1]); !strings.Contains(bar, "job  demo  th_thread000") || !strings.Contains(bar, "running  1/2 agents") {
		t.Fatalf("title bar = %q", bar)
	}
	// The speaker is named once; both chunks read as one block under it, with
	// no timestamp or kind column in front of each row.
	if head := strings.TrimSpace(tuiPlain(got[2])); head != "lead  10:00" {
		t.Fatalf("speaker row = %q, want %q", head, "lead  10:00")
	}
	want := []string{"hello world", "line1", "line2"}
	for i, text := range want {
		if pane := tuiPane(tuiPlain(got[3+i])); pane != text {
			t.Fatalf("transcript row %d = %q, want %q", i, pane, text)
		}
	}
	status := strings.TrimSpace(tuiPlain(got[7]))
	if !strings.Contains(status, "working") || !strings.HasSuffix(status, "agents lead:running  worker-1:succeeded") {
		t.Fatalf("status line = %q, want the working mark and the roster", status)
	}
	if input := strings.TrimRight(tuiPlain(got[9]), " "); input != "┃ hi there▌" {
		t.Fatalf("input line = %q, want %q", input, "┃ hi there▌")
	}
	if footer := tuiPlain(got[11]); !strings.Contains(footer, "enter send") || !strings.Contains(footer, "esc back") {
		t.Fatalf("footer = %q, want the thread key hints", footer)
	}
}

// Streamed chunks from one agent grow one block; another kind or another
// speaker starts the next, and only a change of speaker gets a name row.
func TestTUITranscriptMergesChunksAndNamesSpeakers(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	m.appendLog(
		tuiEntry(now, "lead", "I'll split "),
		tuiEntry(now, "lead", "this in two.\n"),
		contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "$ go test ./...", At: now},
		contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "$ go vet ./...", At: now},
		tuiEntry(now, "worker", "on it"),
	)
	if len(m.log) != 4 {
		t.Fatalf("transcript holds %d blocks, want 4 (merged text, two tools, worker)", len(m.log))
	}
	if m.log[0].text != "I'll split this in two.\n" {
		t.Fatalf("merged block = %q", m.log[0].text)
	}
	heads := []bool{m.log[0].head, m.log[1].head, m.log[2].head, m.log[3].head}
	if want := []bool{true, false, false, true}; fmt.Sprint(heads) != fmt.Sprint(want) {
		t.Fatalf("name rows = %v, want %v", heads, want)
	}
	// 16 rows leave ten for the transcript, rows 2 to 11.
	rows := tuiPaneRows(tuiFrameLines(t, m, 60, 16), 2, 11)
	want := []string{"lead  10:30", "I'll split this in two.", "$ go test ./...", "$ go vet ./...", "worker  10:30", "on it"}
	// The name rows sit at the margin, two cells left of the text.
	for i, row := range rows {
		rows[i] = strings.TrimSpace(row)
	}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Fatalf("transcript rows = %q, want %q", rows, want)
	}
}

// The user's own messages are part of the transcript: a prompt and a steer
// read in line under "you", each once however many times it is delivered,
// while an orchestrator message is skipped — its words arrive as output.
func TestTUITranscriptShowsTheUsersMessagesOnce(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	m.appendMessage("m1", "user", "fix the parser", now)
	m.appendMessage("m1", "user", "fix the parser", now) // the backlog and the live frame
	m.appendMessage("m2", "orchestrator", "done", now)
	m.appendMessage("m3", "steering", "also the lexer", now)
	if len(m.log) != 2 {
		t.Fatalf("transcript holds %d blocks, want the prompt and the steer", len(m.log))
	}
	frame := tuiPlain(render(m, 60, 14))
	for _, want := range []string{"you  10:30", "┃ fix the parser", "you · steer  10:30", "┃ also the lexer"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "done") {
		t.Fatalf("an orchestrator message was shown twice over:\n%s", frame)
	}
}

// A reopened thread interleaves what the user sent with the output by time.
// Of the messages older than the output tail only the newest is kept.
func TestTUITranscriptBacklogInterleavesByTime(t *testing.T) {
	at := func(sec int) time.Time { return time.Date(2024, 1, 1, 10, 0, sec, 0, time.Local) }
	m := newTuiModel(tuiTestClock())
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	m.appendBacklog(
		[]contracts.LogEntry{tuiEntry(at(10), "lead", "first answer"), tuiEntry(at(30), "lead", "second answer")},
		[]tuiMessageRow{
			{ID: "m0", Author: "user", Text: "ancient", At: at(1)},
			{ID: "m1", Author: "user", Text: "first ask", At: at(5)},
			{ID: "m2", Author: "user", Text: "second ask", At: at(20)},
			{ID: "m3", Author: "user", Text: "third ask", At: at(40)},
		},
	)
	var order []string
	for _, b := range m.log {
		order = append(order, b.text)
	}
	want := []string{"first ask", "first answer", "second ask", "second answer", "third ask"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("backlog order = %q, want %q", order, want)
	}
}

func TestTUIScreenInputAndQuestion(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", nil, nil)

	for _, r := range "  yes " {
		m.typeRune(r)
	}
	if got := tuiPlain(render(m, 60, 8)); !strings.Contains(got, "┃   yes ") {
		t.Fatalf("typed input not shown:\n%s", got)
	}
	for i := 0; i < 4; i++ {
		m.backspace()
	}
	if line := m.input.string(); line != "  " {
		t.Fatalf("after backspaces input = %q, want %q", line, "  ")
	}
	if got := m.takeInput(); got != "" {
		t.Fatalf("whitespace-only input submitted %q, want empty", got)
	}

	m.askQuestion(questionPromptEntry{
		id: "q1", kind: contracts.QuestionMulti, question: "Proceed?", options: []string{"yes", "no"},
	})
	for _, r := range "ok" {
		m.typeRune(r)
	}
	got := tuiFrameLines(t, m, 60, 8)
	found := map[string]bool{
		"? Proceed? (answer with numbers separated by commas)": false,
		"  1) yes  2) no": false,
		"? ok":            false,
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
	if !m.input.empty() {
		t.Fatal("takeInput did not clear the editor")
	}
	m.closeQuestion()
	if frame := tuiPlain(render(m, 60, 8)); strings.Contains(frame, "? Proceed?") {
		t.Fatalf("closed question still rendered:\n%s", frame)
	}
}

// A single-choice question lists its options with one highlighted. The arrows
// move the highlight while nothing is typed, and an empty Enter answers with
// it; once the user types, the arrows are the composer's again.
func TestTUIScreenChoiceQuestionPicksWithArrows(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", nil, nil)
	m.askQuestion(questionPromptEntry{
		id: "q1", kind: contracts.QuestionChoice, question: "which database?", options: []string{"postgres", "sqlite"},
	})
	frame := tuiPlain(render(m, 60, 14))
	for _, want := range []string{"? which database?", "▸ 1) postgres", "  2) sqlite"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("choice frame missing %q:\n%s", want, frame)
		}
	}
	if got := m.pendingChoice(); got != "1" {
		t.Fatalf("pendingChoice = %q, want the first option", got)
	}
	if !m.moveChoice(1) || !m.moveChoice(1) { // the second press stops at the end
		t.Fatal("the arrows did not move the highlighted option")
	}
	if got := m.pendingChoice(); got != "2" {
		t.Fatalf("pendingChoice after down = %q, want 2", got)
	}
	if frame := tuiPlain(render(m, 60, 14)); !strings.Contains(frame, "▸ 2) sqlite") {
		t.Fatalf("highlight did not move:\n%s", frame)
	}
	m.typeRune('1')
	if m.moveChoice(-1) || m.pendingChoice() != "" {
		t.Fatal("with text typed the arrows and an empty Enter must not pick an option")
	}
}

func TestTUIScreenScrollPinning(t *testing.T) {
	now := tuiTestClock()
	lines := make([]contracts.LogEntry, 8)
	for i := range lines {
		lines[i] = contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: fmt.Sprintf("line %d", i), At: now}
	}
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", lines, nil)

	// An 8-row terminal leaves 3 rows of transcript; following shows the newest.
	follow := tuiFrameLines(t, m, 60, 8)
	if plain := tuiPane(tuiPlain(follow[2])); plain != "line 5" {
		t.Fatalf("follow pane top = %q", plain)
	}
	if plain := tuiPane(tuiPlain(follow[4])); plain != "line 7" {
		t.Fatalf("follow pane bottom = %q", plain)
	}

	// Scroll up two rows: the window now ends at line 5.
	m.scrollUp(2)
	scrolled := tuiFrameLines(t, m, 60, 8)
	if plain := tuiPane(tuiPlain(scrolled[4])); plain != "line 5" {
		t.Fatalf("scrolled pane bottom = %q", plain)
	}
	if status := tuiPlain(scrolled[5]); !strings.Contains(status, "↑ 2 rows above the latest") {
		t.Fatalf("status line = %q, want the scrolled note", status)
	}

	// New output while scrolled must not move the viewport: pinning. A long
	// entry that wraps over several rows counts as the rows it takes.
	m.appendLog(
		contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "line 8", At: now},
		contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: strings.Repeat("w", 120), At: now},
	)
	pinned := tuiFrameLines(t, m, 60, 8)
	if plain := tuiPane(tuiPlain(pinned[4])); plain != "line 5" {
		t.Fatalf("pinned pane bottom moved to %q", plain)
	}

	// Paging back down to the bottom resumes following the tail.
	m.scrollDown(100)
	if !m.following() {
		t.Fatalf("scroll = %d, want 0 (following)", m.scroll)
	}
	m.appendLog(contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "line 9", At: now})
	live := tuiFrameLines(t, m, 60, 8)
	if plain := tuiPane(tuiPlain(live[4])); plain != "line 9" {
		t.Fatalf("follow pane after append = %q", plain)
	}
}

// Scrolling stops where the oldest row reaches the top of the pane, so the
// view never runs off into blank space above the transcript.
func TestTUIScreenScrollClampsAtTheOldestRow(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{
		{AgentID: "lead", Kind: contracts.LogTool, Text: "only line", At: now},
	}, nil)
	render(m, 60, 8)
	m.scrollUp(100)
	if m.scroll != 0 { // everything already fits the pane
		t.Fatalf("scroll = %d, want 0", m.scroll)
	}
	for i := 0; i < 6; i++ {
		m.appendLog(contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: fmt.Sprintf("more %d", i), At: now})
	}
	render(m, 60, 8)
	m.scrollUp(100)
	// Eight rows (the name row and seven lines) over a three-row pane.
	if m.scroll != 5 {
		t.Fatalf("scroll = %d, want 5", m.scroll)
	}
	frame := tuiFrameLines(t, m, 60, 8)
	if head := strings.TrimSpace(tuiPlain(frame[2])); !strings.HasPrefix(head, "lead") {
		t.Fatalf("top of the pane at max scroll = %q, want the oldest row", head)
	}
	if plain := tuiPane(tuiPlain(frame[3])); plain != "only line" {
		t.Fatalf("second row at max scroll = %q", plain)
	}
}

func TestTUIScreenLogCap(t *testing.T) {
	now := tuiTestClock()
	total := tuiLogCap + 7
	entries := make([]contracts.LogEntry, total)
	for i := range entries {
		entries[i] = contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: fmt.Sprintf("entry %03d", i), At: now}
	}
	// Backlog arrives first, live output after — the oldest blocks fall off.
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", entries[:3], nil)
	m.appendLog(entries[3:]...)

	if len(m.log) != tuiLogCap {
		t.Fatalf("buffer holds %d blocks, want the cap %d", len(m.log), tuiLogCap)
	}
	if m.log[0].text != "entry 007" {
		t.Fatalf("oldest kept block = %q, want entry 007", m.log[0].text)
	}
	if m.log[tuiLogCap-1].text != "entry 406" {
		t.Fatalf("newest block = %q, want entry 406", m.log[tuiLogCap-1].text)
	}
}

func TestTUIScreenTruncateAndWrap(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	command := "$ " + strings.Repeat("y", 50)
	tuiOpenWith(m, threadListRow{ID: "th_1234567890abc", Title: strings.Repeat("w", 40)}, "demo", []contracts.LogEntry{
		{AgentID: "lead", Kind: contracts.LogTool, Text: command, At: now},
		tuiEntry(now, "lead", "prose that is long enough to wrap at a word"),
	}, nil)

	// 20 columns: every row is exactly 20 wide and the long title is cut.
	got := tuiFrameLines(t, m, 20, 16)
	for i, line := range got {
		if w := stringCells(tuiPlain(line)); w != 20 {
			t.Fatalf("row %d is %d wide, want 20: %q", i, w, tuiPlain(line))
		}
	}
	// A command keeps every character: it wraps at the cell, 14 cells beside
	// the gutter at the pane's 16-column content width.
	rows := m.log[0].rows(16)
	if len(rows) != 4 {
		t.Fatalf("expected the 52-rune command to wrap to 4 rows, got %d", len(rows))
	}
	var joined strings.Builder
	for _, row := range rows {
		if w := row.width(); w > 16 {
			t.Fatalf("wrapped row exceeds the pane width: %d", w)
		}
		joined.WriteString(strings.TrimPrefix(row.plain(), "┃ "))
	}
	if joined.String() != command {
		t.Fatalf("wrapped rows lost text: %q", joined.String())
	}
	// Prose breaks at spaces, never inside a word.
	for _, row := range m.log[1].rows(16) {
		text := strings.TrimSpace(row.plain())
		for _, word := range strings.Fields(text) {
			if !strings.Contains(" prose that is long enough to wrap at a word ", " "+word+" ") {
				t.Fatalf("prose row %q split a word", text)
			}
		}
	}
	// Frame level: each wrapped chunk of the command reaches the screen.
	for _, chunk := range tuiWrap(command, 14) {
		found := false
		for _, row := range got {
			if strings.Contains(tuiPlain(row), "┃ "+chunk) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("wrapped chunk %q missing from the frame:\n%s", chunk, tuiPlain(strings.Join(got, "\n")))
		}
	}
}

func TestTUIScreenCloseThreadResets(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.setList([]psRow{{thread: threadListRow{ID: "th_1"}, project: "demo"}})
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{
		tuiEntry(now, "lead", "one"),
		tuiEntry(now, "worker", "two"),
		tuiEntry(now, "lead", "three"),
	}, nil)
	m.scrollUp(1)
	m.typeRune('x')
	m.askQuestion(questionPromptEntry{id: "q1"})

	m.closeThread()
	if m.screen != tuiScreenList {
		t.Fatal("closeThread did not return to the list screen")
	}
	if len(m.log) != 0 || m.scroll != 0 || m.pending != nil || !m.input.empty() {
		t.Fatalf("thread state survived close: log=%d scroll=%d pending=%v input=%q",
			len(m.log), m.scroll, m.pending != nil, m.input.string())
	}
	if frame := tuiPlain(render(m, 60, 8)); !strings.Contains(frame, "Threads") {
		t.Fatalf("list not rendered after close:\n%s", frame)
	}
}

// What was typed in a thread is that thread's draft: leaving keeps it,
// coming back restores it, and sending it clears it.
func TestTUIScreenDraftStaysWithItsThread(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	for _, r := range "half a thought" {
		m.typeRune(r)
	}
	m.closeThread()
	m.openThread(threadListRow{ID: "th_2"}, "demo", nil)
	if !m.input.empty() {
		t.Fatalf("another thread opened with th_1's draft: %q", m.input.string())
	}
	m.closeThread()
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	if got := m.input.string(); got != "half a thought" {
		t.Fatalf("draft = %q, want it back", got)
	}
	if sent := m.takeInput(); sent != "half a thought" {
		t.Fatalf("takeInput = %q", sent)
	}
	m.closeThread()
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	if !m.input.empty() {
		t.Fatalf("a sent draft came back: %q", m.input.string())
	}
}

// The composer grows with its text: a prompt wider than the screen wraps onto
// further rows instead of sliding out of view, and a typed line break starts
// a new row. The caret follows the cursor.
func TestTUIScreenInputWrapsAndGrows(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", nil, nil)
	for _, r := range strings.Repeat("word ", 9) + "END" {
		m.typeRune(r)
	}
	got := tuiFrameLines(t, m, 30, 14)
	var input []string
	for _, line := range got {
		if plain := tuiPlain(line); strings.HasPrefix(plain, "┃ ") && strings.TrimSpace(plain) != "┃" {
			input = append(input, strings.TrimRight(strings.TrimPrefix(plain, "┃ "), " "))
		}
	}
	if len(input) != 2 || !strings.HasPrefix(input[0], "word word") || !strings.HasSuffix(input[1], "END▌") {
		t.Fatalf("input rows = %q, want the text wrapped over two rows with the caret after END", input)
	}
	for i, line := range got {
		if w := stringCells(tuiPlain(line)); w != 30 {
			t.Fatalf("row %d is %d cells wide, want 30: %q", i, w, tuiPlain(line))
		}
	}

	m.input.clear()
	for _, r := range "first" {
		m.typeRune(r)
	}
	m.typeRune('\n')
	for _, r := range "second" {
		m.typeRune(r)
	}
	frame := tuiPlain(render(m, 30, 14))
	if !strings.Contains(frame, "┃ first") || !strings.Contains(frame, "┃ second▌") {
		t.Fatalf("a typed line break did not start a new row:\n%s", frame)
	}
	// With the cursor moved off the end the bar caret is gone: the cell under
	// the cursor is drawn inverted instead, and no text shifts.
	m.input.home()
	frame = tuiPlain(render(m, 30, 14))
	if strings.Contains(frame, "▌") || !strings.Contains(frame, "┃ second") {
		t.Fatalf("mid-text cursor still drew the end caret, or moved text:\n%s", frame)
	}
}

// Agent output is untrusted: ESC and other control bytes must never reach
// the terminal raw, where they would move the cursor or clear the frame. A
// tab becomes spaces, so the cells counted match the cells drawn.
func TestTUIScreenStripsControlBytesFromLogText(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{
		tuiEntry(now, "lead", "a\x1b[2Jb\rc\x07\n"),
		{AgentID: "lead", Kind: contracts.LogTool, Text: "x\ty\x1b[0m", At: now},
	}, nil)
	m.appendMessage("m1", "user", "u\x1b[31mser", now)
	m.askQuestion(questionPromptEntry{id: "q", kind: contracts.QuestionChoice, question: "q\x1b[2J?", options: []string{"o\x1bk"}})
	frame := render(m, 60, 16)
	if strings.Contains(frame, "\x1b") || strings.Contains(frame, "\t") || strings.Contains(frame, "\x07") {
		t.Fatalf("a control byte reached the frame:\n%q", frame)
	}
	plain := tuiPlain(frame)
	for _, want := range []string{"a[2Jbc", "x    y[0m", "u[31mser", "q[2J?", "1) ok"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("frame lost the printable runes of %q:\n%s", want, plain)
		}
	}
}

// A block renders once per width: the rows are memoized on the block, so a
// repaint — or the pane the same block feeds — reuses them instead of
// re-rendering up to the cap every frame. A chunk that grows the block drops
// the memo.
func TestTUIBlockRendersOncePerWidth(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", []contracts.LogEntry{
		tuiEntry(now, "lead", strings.Repeat("z", 60)),
	}, nil)
	render(m, 60, 8)
	m.log[0].wrapped[0] = tuiText("CACHED", tuiInk{})
	if got := m.log[0].rows(58)[0].plain(); got != "CACHED" {
		t.Fatalf("rows at the same width re-rendered instead of reusing the cache: %q", got)
	}
	after := m.log[0].rows(20)
	if len(after) == 0 || after[0].plain() == "CACHED" {
		t.Fatal("a new width did not re-render the block")
	}
	if m.log[0].wrapW != 20 {
		t.Fatalf("cache width = %d, want 20", m.log[0].wrapW)
	}
	m.log[0].wrapped[0] = tuiText("CACHED", tuiInk{})
	m.appendLog(tuiEntry(now, "lead", " more"))
	if got := m.log[0].rows(20); got[0].plain() == "CACHED" || !strings.Contains(m.log[0].text, "more") {
		t.Fatal("a chunk that grew the block left the stale rows in place")
	}
}

// paneRows is pure: rendering settles an out-of-range offset in view without
// writing it back, and two renders of the same model paint the same bytes.
func TestTUIPaneRenderIsPureAndSettlesTop(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_1"}, "demo", nil, nil)
	m.openPane("agent log", tuiLogBlocks(
		contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "one", At: now},
		contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "two", At: now},
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

// The working mark shows only while an agent is running or queued, and its
// clock counts from the earliest start still in flight.
func TestTUIScreenWorkingMarkFollowsTheRoster(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_1", Title: "job"}, "demo", nil)
	m.setRoster(tuiRoster{
		statuses: map[string]contracts.AgentStatus{"lead": contracts.AgentRunning, "worker": contracts.AgentPending},
		started:  map[string]time.Time{"lead": now.Add(-95 * time.Second), "worker": now.Add(-5 * time.Second)},
	})
	if frame := tuiPlain(render(m, 80, 10)); !strings.Contains(frame, "working 1m35s") {
		t.Fatalf("no working mark with its clock:\n%s", frame)
	}
	if title := m.windowTitle(); title != "● job · AgentTunnel" {
		t.Fatalf("window title = %q", title)
	}
	m.observeAgent("lead", contracts.AgentSucceeded)
	m.observeAgent("worker", contracts.AgentCancelled)
	if frame := tuiPlain(render(m, 80, 10)); strings.Contains(frame, "working") {
		t.Fatalf("the working mark outlived the work:\n%s", frame)
	}
	if title := m.windowTitle(); title != "job · AgentTunnel" {
		t.Fatalf("window title after the turn = %q", title)
	}
}
