package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ANSI styling for the TUI. The renderer only wraps whole display lines in
// these codes, so truncation and wrapping can stay plain-text operations.
const (
	tuiReset   = "\x1b[0m"
	tuiBold    = "\x1b[1m"
	tuiDim     = "\x1b[2m"
	tuiReverse = "\x1b[7m"
)

// tuiScreen is which pane the keyboard drives.
type tuiScreen int

const (
	tuiScreenList tuiScreen = iota
	tuiScreenThread
)

// tuiLogCap bounds the thread view's log buffer at the daemon's own
// thread.logs tail size, so a long-lived TUI grows no unbounded slice.
const tuiLogCap = 400

// tuiModel is the whole TUI state. It holds no I/O and no goroutines: the
// event loop feeds it rows, log lines, roster snapshots, and keystrokes, then
// re-renders. Reused shapes: list rows are ps's []psRow, log lines reuse
// writeLogEntry's column format verbatim, and pending questions are attach's
// questionPromptEntry.
type tuiModel struct {
	screen tuiScreen

	// List screen: nested ps rows (nestPsRows already ordered them) with a
	// cursor. now stamps the UPDATED column.
	rows   []psRow
	cursor int
	now    time.Time

	// Thread screen.
	thread  threadListRow
	project string
	// log holds pre-rendered, writeLogEntry-shaped plain lines.
	log []string
	// scroll is how many stored log lines the viewport's window stops short
	// of the end. Zero means tail-follow: new output stays on screen.
	scroll int
	// roster is the thread's agents.list snapshot; the roster line renders it.
	roster map[string]contracts.AgentStatus
	// pending is the open question that owns the next submitted line.
	pending *questionPromptEntry

	// input is the single-line editor for the thread screen.
	input []rune
}

func newTuiModel(now time.Time) *tuiModel {
	return &tuiModel{now: now, roster: map[string]contracts.AgentStatus{}}
}

// setList replaces the thread list, keeping the cursor on a valid row.
func (m *tuiModel) setList(rows []psRow) {
	m.rows = rows
	if m.cursor >= len(rows) {
		m.cursor = len(rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// moveCursor moves the list selection, stopping at the ends.
func (m *tuiModel) moveCursor(delta int) {
	if len(m.rows) == 0 {
		m.cursor = 0
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
}

// selected is the row Enter would open.
func (m *tuiModel) selected() (psRow, bool) {
	if len(m.rows) == 0 {
		return psRow{}, false
	}
	return m.rows[m.cursor], true
}

// openThread switches to the thread screen with a fresh view: backlog fills
// the log buffer, statuses seed the roster, and the view starts tail-followed.
func (m *tuiModel) openThread(row threadListRow, project string, backlog []contracts.LogEntry, statuses map[string]contracts.AgentStatus) {
	m.screen = tuiScreenThread
	m.thread = row
	m.project = project
	m.log = nil
	m.scroll = 0
	m.pending = nil
	m.input = nil
	m.roster = make(map[string]contracts.AgentStatus, len(statuses))
	for id, status := range statuses {
		m.roster[id] = status
	}
	m.appendLog(backlog...)
}

// closeThread returns to the list screen and drops the thread view's state,
// so reopening a thread starts clean.
func (m *tuiModel) closeThread() {
	m.screen = tuiScreenList
	m.thread = threadListRow{}
	m.project = ""
	m.log = nil
	m.scroll = 0
	m.pending = nil
	m.input = nil
	m.roster = map[string]contracts.AgentStatus{}
}

// appendLog renders entries through writeLogEntry so the TUI's log pane and
// `agenttunnel logs` print the same columns.
func (m *tuiModel) appendLog(entries ...contracts.LogEntry) {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		var b strings.Builder
		writeLogEntry(&b, entry)
		lines = append(lines, strings.Split(strings.TrimRight(b.String(), "\n"), "\n")...)
	}
	m.appendLogLines(lines...)
}

// appendLogLines appends pre-rendered lines (status changes, dispatches) and
// keeps the buffer capped: the oldest lines fall off. A scrolled-up viewport
// stays pinned to its content — the offset grows with the appends — while a
// following one (scroll zero) rides the tail by definition.
func (m *tuiModel) appendLogLines(lines ...string) {
	if len(lines) == 0 {
		return
	}
	m.log = append(m.log, lines...)
	if m.scroll > 0 {
		m.scroll += len(lines)
	}
	if over := len(m.log) - tuiLogCap; over > 0 {
		m.log = m.log[over:]
	}
	m.clampScroll()
}

// scrollUp pages toward older output; leaving the very bottom stops following.
func (m *tuiModel) scrollUp(n int) {
	m.scroll += n
	m.clampScroll()
}

// scrollDown pages toward newer output; reaching the bottom resumes following.
func (m *tuiModel) scrollDown(n int) {
	m.scroll -= n
	m.clampScroll()
}

// following reports whether new output stays on screen.
func (m *tuiModel) following() bool {
	return m.scroll == 0
}

func (m *tuiModel) clampScroll() {
	max := len(m.log) - 1
	if max < 0 {
		max = 0
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	if m.scroll > max {
		m.scroll = max
	}
}

// setRoster replaces the thread's agent statuses.
func (m *tuiModel) setRoster(statuses map[string]contracts.AgentStatus) {
	m.roster = make(map[string]contracts.AgentStatus, len(statuses))
	for id, status := range statuses {
		m.roster[id] = status
	}
}

// observeAgent records one agent status from a live event.
func (m *tuiModel) observeAgent(id string, status contracts.AgentStatus) {
	if m.roster == nil {
		m.roster = map[string]contracts.AgentStatus{}
	}
	m.roster[id] = status
}

// askQuestion opens the pending question that owns the next submitted line.
func (m *tuiModel) askQuestion(q questionPromptEntry) {
	m.pending = &q
}

// closeQuestion clears a pending question (answered elsewhere or withdrawn).
func (m *tuiModel) closeQuestion() {
	m.pending = nil
}

// typeRune edits the input line.
func (m *tuiModel) typeRune(r rune) {
	m.input = append(m.input, r)
}

// backspace deletes the last typed rune.
func (m *tuiModel) backspace() {
	if len(m.input) > 0 {
		m.input = m.input[:len(m.input)-1]
	}
}

// takeInput returns the typed line and clears the editor. The line is what
// handleAttachLine-shaped routing parses: an answer when a question is
// pending, a steer or slash command otherwise.
func (m *tuiModel) takeInput() string {
	line := strings.TrimSpace(string(m.input))
	m.input = nil
	return line
}

// render draws the whole screen as one frame of styled lines. It is pure:
// same model and terminal size, same bytes. Lines are plain text wrapped in
// whole-line ANSI codes; truncation and wrapping run on the plain text first.
func render(m *tuiModel, cols, rows int) string {
	if cols < 1 || rows < 1 {
		return ""
	}
	var lines []string
	if m.screen == tuiScreenThread {
		lines = renderTuiThread(m, cols, rows)
	} else {
		lines = renderTuiList(m, cols, rows)
	}
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	return strings.Join(lines, "\n")
}

// tuiStyle wraps one display line in an ANSI code unless it is empty.
func tuiStyle(code, line string) string {
	if line == "" {
		return line
	}
	return code + line + tuiReset
}

// renderTuiList draws the thread list: a header, the ps rows with the cursor
// highlighted, and a key-hint footer.
func renderTuiList(m *tuiModel, cols, rows int) []string {
	out := []string{tuiStyle(tuiBold, tuiTruncate("Threads", cols))}
	height := rows - 2 // header + footer
	if height < 1 {
		return out
	}
	visible, base := tuiListWindow(m.rows, m.cursor, height)
	if len(visible) == 0 {
		return append(out, "no threads yet; start one with `agenttunnel run <template>`",
			tuiStyle(tuiDim, tuiTruncate("q quit", cols)))
	}
	for i, row := range visible {
		line := tuiTruncate(tuiThreadListLine(row, m.now), cols-2)
		if base+i == m.cursor {
			out = append(out, tuiStyle(tuiReverse, "> "+line))
			continue
		}
		out = append(out, "  "+line)
	}
	footer := fmt.Sprintf("%d threads · ↑/↓ select · enter open · r refresh · q quit", len(m.rows))
	return append(out, tuiStyle(tuiDim, tuiTruncate(footer, cols)))
}

// tuiThreadListLine is one row of the TUI list, in ps's column order without
// the table alignment: ID, indented title (nestPsRows depth), project, status,
// agent count, age.
func tuiThreadListLine(row psRow, now time.Time) string {
	title := row.thread.Title
	if title == "" {
		title = "(untitled)"
	}
	if row.depth > 0 {
		title = strings.Repeat("  ", row.depth-1) + "└ " + title
	}
	return fmt.Sprintf("%s  %s  %s  %s  %s  %s",
		shortThreadID(row.thread.ID),
		title,
		row.project,
		row.status,
		psAgentCount(row.thread.RunningAgents, row.thread.Agents),
		relativeAgo(row.thread.UpdatedAt, now))
}

// tuiListWindow picks which slice of a long list stays visible, keeping the
// cursor on screen and roughly centered.
func tuiListWindow(rows []psRow, cursor, height int) ([]psRow, int) {
	if height < 1 || len(rows) <= height {
		return rows, 0
	}
	top := cursor - height/2
	if top > len(rows)-height {
		top = len(rows) - height
	}
	if top < cursor-height+1 {
		top = cursor - height + 1
	}
	if top < 0 {
		top = 0
	}
	return rows[top : top+height], top
}

// renderTuiThread draws the thread screen: header, the log pane, the pending
// question, the roster line, and the input line pinned last.
func renderTuiThread(m *tuiModel, cols, rows int) []string {
	title := m.thread.Title
	if title == "" {
		title = "(untitled)"
	}
	header := fmt.Sprintf("%s %q · %s · %s (%d/%d agents)",
		shortThreadID(m.thread.ID), title, m.project, threadRowStatus(m.thread),
		m.thread.RunningAgents, m.thread.Agents)
	out := []string{tuiStyle(tuiBold, tuiTruncate(header, cols))}

	chrome := 3 // header, roster, input
	var question []string
	if m.pending != nil {
		question = tuiQuestionLines(*m.pending, cols)
		chrome += len(question)
	}
	if height := rows - chrome; height > 0 {
		for _, line := range m.logPane(cols, height) {
			out = append(out, tuiTruncateToWidth(line, cols))
		}
	}
	out = append(out, question...)
	out = append(out, tuiStyle(tuiDim, tuiTruncate(tuiRosterLine(m.roster), cols)))

	prompt := "> "
	if m.pending != nil {
		prompt = "? "
	}
	input := prompt + string(m.input)
	if !m.following() {
		input = fmt.Sprintf("%s  (scrolled %d up)", input, m.scroll)
	}
	out = append(out, tuiTruncate(input, cols))
	return out
}

// logPane picks the displayed log lines. Stored lines wrap to the terminal
// width; the window holds `height` display lines and stops `scroll` stored
// lines short of the end, so a scrolled view stays pinned to its content. A
// page with less content than room pads with blank lines below it.
func (m *tuiModel) logPane(cols, height int) []string {
	bottom := len(m.log) - m.scroll
	if bottom < 0 {
		bottom = 0
	}
	var visible []string
	// Walk backwards from the window's last stored line, collecting wrapped
	// display lines until the page is full.
	for i := bottom - 1; i >= 0 && len(visible) < height; i-- {
		wrapped := tuiWrap(m.log[i], cols)
		if missing := height - len(visible); len(wrapped) > missing {
			wrapped = wrapped[len(wrapped)-missing:]
		}
		visible = append(wrapped, visible...)
	}
	for len(visible) < height {
		visible = append(visible, "")
	}
	return visible
}

// tuiQuestionLines renders a pending question and, on choice questions, its
// numbered options — the same hints printQuestion gives.
func tuiQuestionLines(q questionPromptEntry, cols int) []string {
	hint := ""
	switch q.kind {
	case contracts.QuestionChoice:
		hint = " (answer with a number)"
	case contracts.QuestionMulti:
		hint = " (answer with numbers separated by commas)"
	}
	lines := []string{tuiStyle(tuiBold, tuiTruncate("? "+questionText(q)+hint, cols))}
	if len(q.options) > 0 {
		options := make([]string, len(q.options))
		for i, option := range q.options {
			options[i] = fmt.Sprintf("%d) %s", i+1, option)
		}
		lines = append(lines, tuiTruncate("  "+strings.Join(options, "  "), cols))
	}
	return lines
}

// tuiRosterLine is the one-line roster: agent:status pairs sorted by agent id
// so the line is stable between renders of the same roster.
func tuiRosterLine(statuses map[string]contracts.AgentStatus) string {
	if len(statuses) == 0 {
		return "agents (none)"
	}
	ids := make([]string, 0, len(statuses))
	for id := range statuses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	pairs := make([]string, len(ids))
	for i, id := range ids {
		pairs[i] = id + ":" + string(statuses[id])
	}
	return "agents " + strings.Join(pairs, "  ")
}

// tuiTruncate cuts a plain line to width runes, marking a cut with an
// ellipsis. Width counts runes, not display cells: no dependency, and the
// lines this renders came from the ASCII-first log format anyway.
func tuiTruncate(s string, width int) string {
	r := []rune(s)
	if width < 1 {
		return ""
	}
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

// tuiTruncateToWidth cuts a plain line to width runes without an ellipsis,
// for log pane rows where the wrap already phrases continuation.
func tuiTruncateToWidth(s string, width int) string {
	r := []rune(s)
	if width < 1 {
		return ""
	}
	if len(r) <= width {
		return s
	}
	return string(r[:width])
}

// tuiWrap hard-wraps a plain line at width runes. Entry-internal continuation
// lines (already indented by writeLogEntry to the prefix column) wrap flat;
// only a line longer than the pane width wraps at all.
func tuiWrap(s string, width int) []string {
	if width < 1 {
		return []string{""}
	}
	r := []rune(s)
	if len(r) <= width {
		return []string{s}
	}
	var out []string
	for len(r) > width {
		out = append(out, string(r[:width]))
		r = r[width:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}
