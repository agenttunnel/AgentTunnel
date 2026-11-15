package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiScreen is which pane of the thread list the keyboard drives.
type tuiScreen int

const (
	tuiScreenList tuiScreen = iota
	tuiScreenThread
)

// tuiLogCap bounds the thread view's log buffer at the daemon's own
// thread.logs tail size, so a long-lived TUI grows no unbounded slice.
const tuiLogCap = 400

// tuiInfo is the header's connection line: the daemon's host and the build.
type tuiInfo struct {
	daemon  string
	version string
}

// tuiLogLine is one stored display line. kind picks its color; stamp is the
// rune width of its leading timestamp, which renders faint.
type tuiLogLine struct {
	text  string
	kind  string
	stamp int
	// wrapW/wrapped memoize rows(width): a stored line never changes, so a
	// repaint reuses the wrapped rows instead of re-splitting up to the
	// tuiLogCap lines every frame.
	wrapW   int
	wrapped []tuiRow
}

// rows wraps the line to width and styles each wrapped row. Hard wrapping
// makes row i start at cell offset i*width, which is how the stamp boundary
// is found; the stamp prefix itself is always narrow. Text is stripped of
// control bytes first: agent output is not trusted to be printable, and a
// raw ESC or CSI would break the frame geometry.
func (l *tuiLogLine) rows(width int) []tuiRow {
	if l.wrapped != nil && l.wrapW == width {
		return l.wrapped
	}
	text := tuiSanitize(l.text)
	gutter := tuiGutter(l.kind)
	body := max(width-2, 1)
	wrapped := tuiWrap(text, body)
	out := make([]tuiRow, len(wrapped))
	off := 0
	for i, part := range wrapped {
		out[i] = gutter.join(styleLogRow(part, off, l.stamp, tuiKindInk(l.kind))).fit(width)
		off += stringCells(part)
	}
	l.wrapped, l.wrapW = out, width
	return out
}

// tuiGutter is the two cells left of a log row: a colored bar for the kinds
// that deserve one (tools, errors, reasoning), blank for plain output.
func tuiGutter(kind string) tuiRow {
	switch kind {
	case "tool":
		return tuiText("┃ ", tuiInk{fg: tuiColAccent2})
	case "error":
		return tuiText("┃ ", tuiInk{fg: tuiColErr})
	case "reasoning":
		return tuiText("┃ ", tuiInk{fg: tuiColAgent})
	}
	return tuiText("  ", tuiInk{})
}

// styleLogRow paints the stamp part of a wrapped row faint and the rest in ink.
func styleLogRow(text string, offset, stamp int, ink tuiInk) tuiRow {
	runes := []rune(text)
	n := stamp - offset
	if n <= 0 || len(runes) == 0 {
		return tuiText(text, ink)
	}
	n = min(n, len(runes))
	return tuiText(string(runes[:n]), tuiInk{fg: tuiColFaint}).join(tuiText(string(runes[n:]), ink))
}

// tuiModel is the whole TUI state. It holds no I/O and no goroutines: the
// event loop feeds it rows, log lines, roster snapshots, and keystrokes, then
// re-renders. Reused shapes: list rows are ps's []psRow, log lines reuse
// writeLogEntry's column format verbatim, and pending questions are attach's
// questionPromptEntry.
type tuiModel struct {
	theme tuiTheme
	info  tuiInfo

	// view is the active registered tab; "" is the thread list.
	view string
	// viewState holds each registered view's own state, keyed by view ID.
	viewState map[string]any
	// palette is the open command palette, or nil.
	palette *tuiPalette
	// dialog is the open prompt or picker a thread command asked for, or nil.
	dialog *tuiDialog
	// quit, refresh are requests the session loop acts on after a key.
	quit, refresh bool
	// openID asks the loop to open a thread by id, once the list has it.
	openID string
	// jobs are daemon calls queued for the loop to launch in the background.
	jobs []tuiJob
	// catalog is the harness catalog, fetched once a runtime menu needs it.
	catalog *tuiCatalog
	// flash is a one-line note shown on the thread list; any key clears it.
	flash string

	screen tuiScreen

	// List screen: nested ps rows (nestPsRows already ordered them) with a
	// cursor. now stamps the UPDATED column. seen is each thread's message
	// count at its last read: a count above it, on an idle thread, is unread.
	// A -1 entry means the thread was just closed and reseeds on the next list.
	rows   []psRow
	cursor int
	now    time.Time
	seen   map[string]int

	// Thread screen.
	thread  threadListRow
	project string
	// log is the pane's buffer, oldest first.
	log []tuiLogLine
	// scroll is how many stored log lines the viewport's window stops short
	// of the end. Zero means tail-follow: new output stays on screen.
	scroll int
	// roster is the thread's agents.list snapshot; harness maps each agent to
	// its harness, for the tint.
	roster  map[string]contracts.AgentStatus
	harness map[string]string
	// pending is the open question that owns the next submitted line.
	pending *questionPromptEntry
	// pane replaces the log pane while open: agent logs, mailbox, or tasks.
	pane *tuiDetail

	// input is the single-line editor for the thread screen.
	input []rune
}

func newTuiModel(now time.Time) *tuiModel {
	return &tuiModel{
		now:       now,
		seen:      map[string]int{},
		roster:    map[string]contracts.AgentStatus{},
		harness:   map[string]string{},
		viewState: map[string]any{},
	}
}

// setList replaces the thread list, keeping the cursor on a valid row. A
// thread seen for the first time is marked read, so history never lights up
// as unread; the open thread is always read.
func (m *tuiModel) setList(rows []psRow) {
	m.rows = rows
	if m.seen == nil {
		m.seen = map[string]int{}
	}
	for _, row := range rows {
		id := row.thread.ID
		seen, ok := m.seen[id]
		if !ok || seen < 0 || (m.screen == tuiScreenThread && id == m.thread.ID) {
			m.seen[id] = row.thread.Messages
		}
	}
	if m.cursor >= len(rows) {
		m.cursor = len(rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// isUnread reports whether a thread has messages the viewer has not read: it
// is idle, not open, and its count grew past the last one seen.
func (m *tuiModel) isUnread(th threadListRow) bool {
	if th.RunningAgents > 0 || (m.screen == tuiScreenThread && th.ID == m.thread.ID) {
		return false
	}
	seen, ok := m.seen[th.ID]
	return ok && seen >= 0 && th.Messages > seen
}

// unreadCount is how many listed threads are unread.
func (m *tuiModel) unreadCount() int {
	n := 0
	for _, row := range m.rows {
		if m.isUnread(row.thread) {
			n++
		}
	}
	return n
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
	m.harness = map[string]string{}
	m.roster = make(map[string]contracts.AgentStatus, len(statuses))
	for id, status := range statuses {
		m.roster[id] = status
	}
	m.appendLog(backlog...)
}

// closeThread returns to the list screen and drops the thread view's state,
// so reopening a thread starts clean. The thread counts as read as of the
// next list refresh.
func (m *tuiModel) closeThread() {
	if m.thread.ID != "" && m.seen != nil {
		m.seen[m.thread.ID] = -1
	}
	m.screen = tuiScreenList
	m.thread = threadListRow{}
	m.project = ""
	m.log = nil
	m.scroll = 0
	m.pending = nil
	m.pane = nil
	m.input = nil
	m.roster = map[string]contracts.AgentStatus{}
	m.harness = map[string]string{}
}

// appendLog renders entries through writeLogEntry so the TUI's log pane and
// `agenttunnel logs` print the same columns, then colors each line by kind.
func (m *tuiModel) appendLog(entries ...contracts.LogEntry) {
	m.push(tuiLogLines(entries...)...)
}

// tuiLogLines renders log entries as stored display lines, in the same column
// format writeLogEntry prints.
func tuiLogLines(entries ...contracts.LogEntry) []tuiLogLine {
	var lines []tuiLogLine
	for _, entry := range entries {
		kind := string(entry.Kind)
		if kind == "" {
			kind = string(contracts.LogText)
		}
		var b strings.Builder
		writeLogEntry(&b, entry)
		for i, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
			stamp := 0
			if i == 0 {
				stamp = stampWidth(line)
			}
			lines = append(lines, tuiLogLine{text: line, kind: kind, stamp: stamp})
		}
	}
	return lines
}

// appendLogLines appends plain system lines (help, command errors).
func (m *tuiModel) appendLogLines(lines ...string) {
	m.appendKind("system", lines...)
}

// appendStatusLines appends agent lifecycle and dispatch lines.
func (m *tuiModel) appendStatusLines(lines ...string) {
	m.appendKind("status", lines...)
}

func (m *tuiModel) appendKind(kind string, lines ...string) {
	stored := make([]tuiLogLine, len(lines))
	for i, line := range lines {
		stored[i] = tuiLogLine{text: line, kind: kind}
	}
	m.push(stored...)
}

// stampWidth is the rune width of a line's leading word, the timestamp
// writeLogEntry and writeStatusLine lead with.
func stampWidth(line string) int {
	if i := strings.IndexByte(line, ' '); i > 0 {
		return i + 1
	}
	return 0
}

// push appends stored lines and keeps the buffer capped: the oldest lines
// fall off. A scrolled-up viewport stays pinned to its content — the offset
// grows with the appends — while a following one (scroll zero) rides the tail.
func (m *tuiModel) push(lines ...tuiLogLine) {
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

// setRoster replaces the thread's agent statuses and harnesses.
func (m *tuiModel) setRoster(statuses map[string]contracts.AgentStatus, harness map[string]string) {
	m.roster = make(map[string]contracts.AgentStatus, len(statuses))
	for id, status := range statuses {
		m.roster[id] = status
	}
	m.harness = make(map[string]string, len(harness))
	for id, h := range harness {
		m.harness[id] = h
	}
}

// observeAgent records one agent status from a live event.
func (m *tuiModel) observeAgent(id string, status contracts.AgentStatus) {
	if m.roster == nil {
		m.roster = map[string]contracts.AgentStatus{}
	}
	m.roster[id] = status
}

// observeHarness records the harness an agent was dispatched on.
func (m *tuiModel) observeHarness(id, harness string) {
	if harness == "" {
		return
	}
	if m.harness == nil {
		m.harness = map[string]string{}
	}
	m.harness[id] = harness
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

// activeView is the registered view on screen, if one is.
func (m *tuiModel) activeView() (tuiView, bool) {
	if m.view == tuiThreadsViewID {
		return tuiView{}, false
	}
	return tuiViewByID(m.view)
}

// render draws the whole screen as one frame of styled lines: a header bar,
// the body, and a footer of key hints. It is pure: same model and terminal
// size, same bytes. Widths are computed on plain runes before styling.
func render(m *tuiModel, cols, rows int) string {
	if cols < 1 || rows < 1 {
		return ""
	}
	var lines []tuiRow
	bodyRows := rows
	lines = append(lines, m.headerRow(cols))
	bodyRows--
	hasFooter := rows >= 2
	if hasFooter {
		bodyRows--
	}
	body := fitRows(m.bodyRows(cols, bodyRows), bodyRows)
	if m.palette != nil {
		if box, width := m.paletteBox(cols, bodyRows); box != nil {
			body = tuiPaintModal(body, cols, box, width)
		}
	} else if m.dialog != nil {
		if box, width := m.dialogBox(cols, bodyRows); box != nil {
			body = tuiPaintModal(body, cols, box, width)
		}
	}
	lines = append(lines, body...)
	if hasFooter {
		lines = append(lines, tuiFooter(cols, m.hints()))
	}
	out := make([]string, len(lines))
	for i, row := range lines {
		out[i] = row.render(m.theme)
	}
	return strings.Join(out, "\n")
}

// fitRows makes a body exactly n rows: extra rows are dropped from the end,
// missing ones padded with blanks.
func fitRows(rows []tuiRow, n int) []tuiRow {
	if n < 0 {
		n = 0
	}
	if len(rows) > n {
		return rows[:n]
	}
	for len(rows) < n {
		rows = append(rows, nil)
	}
	return rows
}

// bodyRows picks the body for the active tab.
func (m *tuiModel) bodyRows(cols, rows int) []tuiRow {
	if v, ok := m.activeView(); ok {
		return v.Render(m, cols, rows)
	}
	if m.screen == tuiScreenThread {
		if m.pane != nil {
			return m.paneRows(cols, rows)
		}
		return m.renderThread(cols, rows)
	}
	return m.renderList(cols, rows)
}

// headerRow is the top bar: the AgentTunnel wordmark, the tabs with the
// active one lit, and the daemon and build on the right. The daemon label
// is reserved first; the tabs share whatever is left, windowed around the
// active one with ellipsis markers when they cannot all fit. A bar too
// narrow for everything gives up the build first, then the wordmark — the
// host itself stays on screen at any width that can hold it.
func (m *tuiModel) headerRow(cols int) tuiRow {
	bar := tuiInk{bg: tuiColBar}
	mark := tuiText(" ◆ ", tuiInk{fg: tuiColAccent, bg: tuiColBar, bold: true}).join(
		tuiText("Agent", tuiInk{fg: tuiColFaint, bg: tuiColBar}),
		tuiText("Tunnel ", tuiInk{fg: tuiColText, bg: tuiColBar, bold: true}))
	labels := []tuiRow{m.daemonRow(true)}
	if m.info.version != "" {
		labels = append(labels, m.daemonRow(false))
	}
	for _, right := range labels {
		if row := m.headerTabs(mark, right, cols); row != nil {
			return row
		}
	}
	// Too narrow for the wordmark beside the bare host: keep the host on the
	// right edge and cut the wordmark to whatever room is left.
	right := labels[len(labels)-1]
	if cols <= right.width() {
		return right.fit(cols).padTo(cols, bar)
	}
	cut := cols - right.width()
	return mark.fit(cut).padTo(cut, bar).join(right)
}

// daemonRow is the header's right-hand label: the connection dot, the daemon
// host, and the build — the build only when withVersion is set.
func (m *tuiModel) daemonRow(withVersion bool) tuiRow {
	info := m.info.daemon
	if withVersion && m.info.version != "" {
		info += "  v" + m.info.version
	}
	return tuiText("● ", tuiInk{fg: tuiColOK, bg: tuiColBar}).
		join(tuiText(info+" ", tuiInk{fg: tuiColFaint, bg: tuiColBar}))
}

// headerTabs builds one header attempt for the given right-hand label: the
// wordmark, the windowed tab bar, and the label right-aligned. It returns nil
// when the row cannot hold all three with at least one blank before the label.
func (m *tuiModel) headerTabs(mark, right tuiRow, cols int) tuiRow {
	bar := tuiInk{bg: tuiColBar}
	ids, leftCut, rightCut := tuiWindowTabs(tuiViewOrder(), m.view, cols-mark.width()-right.width()-1)
	row := mark
	if leftCut {
		row = row.join(tuiText("…", tuiInk{fg: tuiColFaint, bg: tuiColBar}), tuiText(" ", bar))
	}
	for _, id := range ids {
		ink := tuiInk{fg: tuiColDim, bg: tuiColBar}
		if id == m.view {
			ink = tuiInk{fg: tuiColInk, bg: tuiColSelect, bold: true}
		}
		row = row.join(tuiText(" "+tuiTabTitle(id)+" ", ink), tuiText(" ", bar))
	}
	if rightCut {
		row = row.join(tuiText("…", tuiInk{fg: tuiColFaint, bg: tuiColBar}), tuiText(" ", bar))
	}
	gap := cols - row.width() - right.width()
	if gap < 1 {
		return nil
	}
	return row.join(tuiText(strings.Repeat(" ", gap), bar), right)
}

// tuiTabCells is one tab's width in the header: a space of padding each side
// of the title plus the separator space after it.
func tuiTabCells(id string) int {
	return stringCells(tuiTabTitle(id)) + 3
}

// tuiWindowTabs picks the tabs the header shows when they cannot all fit:
// a window around the active one, with an ellipsis standing for each hidden
// run. Two cells per possible ellipsis are reserved before fitting tabs, so
// the markers never steal the daemon label's space.
func tuiWindowTabs(order []string, active string, budget int) (ids []string, leftCut, rightCut bool) {
	widths := make([]int, len(order))
	total := 0
	for i, id := range order {
		widths[i] = tuiTabCells(id)
		total += widths[i]
	}
	if total <= budget {
		return order, false, false
	}
	idx := 0
	for i, id := range order {
		if id == active {
			idx = i
			break
		}
	}
	room := budget - 4 // the two possible ellipsis markers
	if room < widths[idx] {
		if budget >= widths[idx] {
			return order[idx : idx+1], idx > 0, idx < len(order)-1
		}
		return nil, false, false
	}
	lo, hi, used := idx, idx+1, widths[idx]
	for {
		grew := false
		if lo > 0 && used+widths[lo-1] <= room {
			lo--
			used += widths[lo]
			grew = true
		}
		if hi < len(order) && used+widths[hi] <= room {
			used += widths[hi]
			hi++
			grew = true
		}
		if !grew {
			break
		}
	}
	return order[lo:hi], lo > 0, hi < len(order)
}

// hints are the footer's key hints for the current surface.
func (m *tuiModel) hints() []tuiHint {
	if m.palette != nil {
		return []tuiHint{{"enter", "run"}, {"↑/↓", "move"}, {"esc", "close"}}
	}
	if m.dialog != nil {
		if m.dialog.items != nil {
			return []tuiHint{{"enter", "choose"}, {"↑/↓", "move"}, {"esc", "cancel"}}
		}
		return []tuiHint{{"enter", "confirm"}, {"esc", "cancel"}}
	}
	if m.pane != nil && m.screen == tuiScreenThread {
		return []tuiHint{{"pgup/pgdn", "scroll"}, {"esc", "close"}}
	}
	if v, ok := m.activeView(); ok {
		if len(v.Hints) > 0 {
			return v.Hints
		}
		return []tuiHint{{"tab", "views"}, {"esc", "threads"}, {"ctrl+k", "commands"}, {"q", "quit"}}
	}
	if m.screen == tuiScreenThread {
		return []tuiHint{{"enter", "send"}, {"esc", "back"}, {"pgup/pgdn", "scroll"}, {"ctrl+k", "commands"}, {"ctrl+c", "quit"}}
	}
	return []tuiHint{{"↑/↓", "select"}, {"enter", "open"}, {"tab", "views"}, {"r", "refresh"}, {"ctrl+k", "commands"}, {"q", "quit"}}
}

// renderList draws the thread list the way opencode's session picker looks: a
// quiet heading, then one row per thread with the selected one washed in the
// primary color. No frame; the rows own the width.
func (m *tuiModel) renderList(cols, rows int) []tuiRow {
	if rows < 1 || cols < 4 {
		return fitRows(nil, rows)
	}
	innerW := cols - 4
	heading := tuiText("Threads", tuiInk{fg: tuiColText, bold: true}).
		join(tuiText(fmt.Sprintf("  %d", len(m.rows)), tuiInk{fg: tuiColFaint}))
	if m.flash != "" {
		heading = heading.join(tuiText("  "+m.flash, tuiInk{fg: tuiColAccent, bold: true}))
	} else if n := m.unreadCount(); n > 0 {
		heading = heading.join(tuiText(fmt.Sprintf("  %d unread", n), tuiInk{fg: tuiColAccent, bold: true}))
	}
	var out []tuiRow
	if rows >= 6 { // a short terminal spends every row on threads
		out = []tuiRow{nil, tuiText("  ", tuiInk{}).join(heading).fit(cols)}
	}
	switch {
	case len(m.rows) == 0:
		out = append(out, tuiText("  no threads yet; start one with `agenttunnel run <template>`", tuiInk{fg: tuiColFaint}).fit(cols))
	case rows-len(out) >= 1:
		visible, base := tuiListWindow(m.rows, m.cursor, rows-len(out))
		for i, row := range visible {
			line := m.listRow(row, innerW, base+i == m.cursor)
			out = append(out, tuiText("  ", tuiInk{}).join(line))
		}
	}
	return fitRows(out, rows)
}

// listRow is one thread: a cursor, a status dot, the ps columns in order, and
// an "unread" word when the thread has news. A selected row washes its width.
func (m *tuiModel) listRow(row psRow, width int, selected bool) tuiRow {
	status := row.status
	if status == "" {
		status = threadRowStatus(row.thread)
	}
	title := row.thread.Title
	if title == "" {
		title = "(untitled)"
	}
	if row.depth > 0 {
		title = strings.Repeat("  ", row.depth-1) + "└ " + title
	}
	marker, titleInk := "  ", tuiInk{fg: tuiColText}
	if selected {
		marker, titleInk.bold = "▌ ", true
	}
	countInk := tuiInk{fg: tuiColFaint}
	if row.thread.RunningAgents > 0 {
		countInk = tuiInk{fg: tuiColAccent}
	}
	line := tuiText(marker, tuiInk{fg: tuiColAccent, bold: true}).join(
		tuiText("● ", tuiStatusInk(status)),
		tuiText(shortThreadID(row.thread.ID), tuiInk{fg: tuiColDim}),
		tuiText("  ", tuiInk{}),
		tuiText(title, titleInk),
		tuiText("  ", tuiInk{}),
		tuiText(row.project, tuiInk{fg: tuiColDim}),
		tuiText("  ", tuiInk{}),
		tuiText(status, tuiStatusInk(status)),
		tuiText("  ", tuiInk{}),
		tuiText(psAgentCount(row.thread.RunningAgents, row.thread.Agents), countInk),
		tuiText("  ", tuiInk{}),
		tuiText(relativeAgo(row.thread.UpdatedAt, m.now), tuiInk{fg: tuiColFaint}),
	)
	if m.isUnread(row.thread) {
		line = line.join(tuiText("  unread", tuiInk{fg: tuiColAccent, bold: true}))
	}
	line = line.fit(width).padTo(width, tuiInk{})
	if selected {
		line = line.selected()
	}
	return line
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

// renderThread draws the thread view the opencode way: a panel title bar, the
// conversation with quiet gutters instead of a frame, any pending question and
// the roster, and a panel prompt pinned at the bottom.
func (m *tuiModel) renderThread(cols, rows int) []tuiRow {
	input := m.inputBox(cols, rows)
	innerW := max(cols-2, 1)
	innerH := rows - len(input)
	if rows >= 6 {
		innerH-- // the title bar
	}
	if innerH < 0 {
		innerH = 0
	}

	var tail []tuiRow
	if m.pending != nil {
		tail = questionRows(*m.pending, innerW)
	}
	tail = append(tail, m.rosterRow(innerW))
	if len(tail) > innerH {
		tail = tail[len(tail)-innerH:]
	}
	inner := append(m.logPane(innerW, innerH-len(tail)), tail...)
	margin := tuiText("  ", tuiInk{})
	for i := range inner {
		inner[i] = margin.join(inner[i]).fit(cols).padTo(cols, tuiInk{})
	}

	var body []tuiRow
	if rows >= 6 {
		body = append(body, m.threadBar(cols))
	}
	body = append(body, fitRows(inner, innerH)...)
	return append(body, input...)
}

// threadBar is the panel row over the conversation: the title, the project,
// and the status at the right edge.
func (m *tuiModel) threadBar(cols int) tuiRow {
	bar := tuiInk{bg: tuiColBar}
	right := m.threadStatus().withBG(tuiColBar).join(tuiText(" ", bar))
	left := tuiText(" ", bar).join(m.threadTitle().withBG(tuiColBar))
	if cols <= right.width()+2 {
		return left.fit(cols).padTo(cols, bar)
	}
	left = left.fit(cols - right.width() - 1)
	return left.padTo(cols-right.width(), bar).join(right)
}

// threadTitle is the bar's left label: the title, then the project and id.
func (m *tuiModel) threadTitle() tuiRow {
	title := m.thread.Title
	if title == "" {
		title = "(untitled)"
	}
	return tuiText(title, tuiInk{fg: tuiColText, bold: true}).join(
		tuiText("  "+m.project, tuiInk{fg: tuiColDim}),
		tuiText("  "+shortThreadID(m.thread.ID), tuiInk{fg: tuiColFaint}),
	)
}

// threadStatus is the bar's right label: the status word and agent count.
func (m *tuiModel) threadStatus() tuiRow {
	status := threadRowStatus(m.thread)
	return tuiText("● ", tuiStatusInk(status)).join(
		tuiText(status, tuiStatusInk(status)),
		tuiText(fmt.Sprintf("  %d/%d agents", m.thread.RunningAgents, m.thread.Agents), tuiInk{fg: tuiColDim}),
	)
}

// inputBox is the prompt: a panel block with a colored left gutter, a padding
// row above and below, and a caret after the typed text. A question owns the
// prompt in the warning color. Too short a screen gets the bare line.
func (m *tuiModel) inputBox(cols, rows int) []tuiRow {
	gutterInk, prompt := tuiInk{fg: tuiColAccent, bg: tuiColBar}, ""
	if m.pending != nil {
		gutterInk, prompt = tuiInk{fg: tuiColWarn, bg: tuiColBar}, "? "
	}
	bar := tuiInk{bg: tuiColBar}
	// The caret trails the typed text, ahead of the scroll note.
	text := tuiText(prompt, tuiInk{fg: tuiColWarn, bg: tuiColBar, bold: true}).
		join(tuiText(string(m.input), tuiInk{fg: tuiColText, bg: tuiColBar}),
			tuiText("▌", tuiInk{fg: tuiColAccent, bg: tuiColBar}))
	if !m.following() {
		text = text.join(tuiText(fmt.Sprintf("  (scrolled %d up)", m.scroll), tuiInk{fg: tuiColFaint, bg: tuiColBar}))
	}
	// tail keeps the caret side on screen when the line is long.
	line := func(content tuiRow) tuiRow {
		return tuiText("┃ ", gutterInk).join(content.tail(cols-2)).padTo(cols, bar)
	}
	if rows < 8 {
		return []tuiRow{line(text)}
	}
	return []tuiRow{line(nil), line(text), line(nil)}
}

// logPane picks the displayed log rows. Stored lines wrap to the pane width;
// the window holds `height` display rows and stops `scroll` stored lines short
// of the end, so a scrolled view stays pinned to its content. A page with less
// content than room pads with blank rows below it.
func (m *tuiModel) logPane(cols, height int) []tuiRow {
	if height <= 0 {
		return nil
	}
	bottom := len(m.log) - m.scroll
	if bottom < 0 {
		bottom = 0
	}
	var visible []tuiRow
	// Walk backwards from the window's last stored line, collecting wrapped
	// rows until the page is full.
	for i := bottom - 1; i >= 0 && len(visible) < height; i-- {
		wrapped := m.log[i].rows(cols)
		if missing := height - len(visible); len(wrapped) > missing {
			wrapped = wrapped[len(wrapped)-missing:]
		}
		visible = append(append([]tuiRow{}, wrapped...), visible...)
	}
	return fitRows(visible, height)
}

// questionRows renders a pending question and, on choice questions, its
// numbered options — the same hints printQuestion gives.
func questionRows(q questionPromptEntry, width int) []tuiRow {
	hint := ""
	switch q.kind {
	case contracts.QuestionChoice:
		hint = " (answer with a number)"
	case contracts.QuestionMulti:
		hint = " (answer with numbers separated by commas)"
	}
	rows := []tuiRow{tuiText("? "+questionText(q)+hint, tuiInk{fg: tuiColWarn, bold: true}).fit(width)}
	if len(q.options) > 0 {
		options := make([]string, len(q.options))
		for i, option := range q.options {
			options[i] = fmt.Sprintf("%d) %s", i+1, option)
		}
		rows = append(rows, tuiText("  "+strings.Join(options, "  "), tuiInk{fg: tuiColText}).fit(width))
	}
	return rows
}

// rosterRow is the one-line roster: agent:status pairs sorted by agent id so
// the line is stable between renders. Names take their harness tint.
func (m *tuiModel) rosterRow(width int) tuiRow {
	if len(m.roster) == 0 {
		return tuiText("agents (none)", tuiInk{fg: tuiColDim}).fit(width)
	}
	ids := make([]string, 0, len(m.roster))
	for id := range m.roster {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	row := tuiText("agents ", tuiInk{fg: tuiColDim})
	for i, id := range ids {
		if i > 0 {
			row = row.join(tuiText("  ", tuiInk{}))
		}
		status := string(m.roster[id])
		nameInk := tuiHarnessInk(m.harness[id])
		row = row.join(
			tuiText(id, nameInk),
			tuiText(":", tuiInk{fg: tuiColFaint}),
			tuiText(status, tuiStatusInk(status)),
		)
	}
	return row.fit(width)
}

// tuiWrap hard-wraps a plain line at width cells; a wide rune that would
// straddle the boundary starts the next row. Entry-internal continuation
// lines (already indented by writeLogEntry to the prefix column) wrap flat;
// only a line wider than the pane width wraps at all.
func tuiWrap(s string, width int) []string {
	if width < 1 {
		return []string{""}
	}
	if stringCells(s) <= width {
		return []string{s}
	}
	r := []rune(s)
	var out []string
	start, cells := 0, 0
	for i, rn := range r {
		w := runeCells(rn)
		if cells+w > width {
			out = append(out, string(r[start:i]))
			start, cells = i, 0
		}
		cells += w
	}
	out = append(out, string(r[start:]))
	return out
}

// tuiSanitize strips control characters the TUI never prints: C0 and C1
// codes and DEL, except tab. ESC or CSI bytes in agent output would move the
// cursor or clear the frame, so no stored text renders raw.
func tuiSanitize(s string) string {
	clean := true
	for _, r := range s {
		if tuiControlRune(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !tuiControlRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tuiControlRune reports whether r is a control rune: the C0 range, DEL, or
// the C1 range — except tab, which logs use for indentation.
func tuiControlRune(r rune) bool {
	if r == '\t' {
		return false
	}
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}
