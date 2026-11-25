package cli

import (
	"fmt"
	"strconv"
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

// tuiInfo is the header's connection line: the daemon's host and the build.
// offline is set while the session is redialing a dropped connection.
type tuiInfo struct {
	daemon  string
	version string
	offline bool
}

// tuiModel is the whole TUI state. It holds no I/O and no goroutines: the
// event loop feeds it rows, transcript blocks, roster snapshots, and
// keystrokes, then re-renders. Reused shapes: list rows are ps's []psRow and
// pending questions are attach's questionPromptEntry.
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
	// copyText is text a command asked the loop to put on the clipboard, and
	// edit a request to write the prompt in $EDITOR; the terminal does both.
	copyText string
	edit     bool
	// openID asks the loop to open a thread by id, once the list has it.
	openID string
	// jobs are daemon calls queued for the loop to launch in the background.
	jobs []tuiJob
	// catalog is the harness catalog, fetched once a runtime menu needs it.
	catalog *tuiCatalog
	// flash is a one-line note shown on the thread list; any key clears it.
	flash string

	screen tuiScreen

	// cwd is the directory the TUI was started in, offered as the folder of a
	// new project. fresh is the thread the TUI opened there on its own: one
	// left with nothing done in it is discarded, so starting the TUI never
	// leaves an empty thread behind.
	cwd   string
	fresh string
	// savedTheme is the desktop theme the user picked, which lends its accent.
	savedTheme string

	// filter narrows the thread list to the rows matching what was typed after
	// "/"; nil shows every row.
	filter *tuiListFilter

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
	// log is the transcript, oldest block first. logCap is how many blocks it
	// may hold: tuiLogCap, plus whatever older output the reader loaded.
	log    []tuiBlock
	logCap int
	// growing marks output appended while scrolled that the view has not
	// measured yet; growFrom is the transcript's height before the first of
	// it. See grow and settleGrowth.
	growing  bool
	growFrom int
	// messages is what the user sent in this thread, oldest first, for placing
	// in older output as it loads; older is the paging state.
	messages []tuiMessageRow
	older    tuiOlder
	// shown holds the ids of the user messages already in the transcript, so
	// one the backlog and the live stream both carry appears once.
	shown map[string]bool
	// scroll is how many display rows the viewport's window stops short of the
	// end. Zero means tail-follow: new output stays on screen.
	scroll int
	// logW and logH are the transcript's last painted width and height. The
	// render records them so scrolling moves in the rows the reader sees.
	logW, logH int
	// roster is the thread's agents.list snapshot; harness maps each agent to
	// its harness, for the tint, and started to when it began, for the clock.
	roster  map[string]contracts.AgentStatus
	harness map[string]string
	started map[string]time.Time
	// parent and role place each agent in the sidebar's tree.
	parent map[string]string
	role   map[string]string
	// pending is the open question that owns the next submitted line; choice
	// is the option of a choice question the arrows have highlighted.
	pending *questionPromptEntry
	choice  int
	// pane replaces the log pane while open: agent logs, mailbox, or tasks.
	pane *tuiDetail

	// input is the composer: a multi-line editor with the sent prompts kept
	// for recall. drafts holds what was typed in each thread left mid-thought.
	input  tuiInput
	drafts map[string]string
	// slashSel is the highlighted slash hint; reset whenever the input changes.
	slashSel int
	// mention is the open "@" file search; attached holds, per thread, the
	// paths picked from it that the draft may still name.
	mention  tuiMention
	attached map[string]map[string]bool
	// side is the thread screen's right pane data.
	side tuiSide
}

func newTuiModel(now time.Time) *tuiModel {
	return &tuiModel{
		now:       now,
		seen:      map[string]int{},
		roster:    map[string]contracts.AgentStatus{},
		harness:   map[string]string{},
		started:   map[string]time.Time{},
		drafts:    map[string]string{},
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
	m.moveCursor(0)
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

// moveCursor moves the list selection over the rows shown, stopping at the
// ends. A zero delta just brings the cursor back in range.
func (m *tuiModel) moveCursor(delta int) {
	m.cursor = min(max(m.cursor+delta, 0), max(len(m.listRows())-1, 0))
}

// selected is the row Enter would open.
func (m *tuiModel) selected() (psRow, bool) {
	rows := m.listRows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return psRow{}, false
	}
	return rows[m.cursor], true
}

// openThread switches to the thread screen with a fresh view: the roster is
// seeded, the view starts tail-followed, and a draft left in this thread
// comes back to the composer. The caller fills the transcript.
func (m *tuiModel) openThread(row threadListRow, project string, statuses map[string]contracts.AgentStatus) {
	m.screen = tuiScreenThread
	m.thread = row
	m.project = project
	m.resetTranscript()
	m.pending = nil
	m.pane = nil
	m.harness = map[string]string{}
	m.started = map[string]time.Time{}
	m.roster = make(map[string]contracts.AgentStatus, len(statuses))
	for id, status := range statuses {
		m.roster[id] = status
	}
	m.input.set(m.drafts[row.ID])
	m.slashSel = 0
	m.mention = tuiMention{}
}

// repaintAll drops every block's remembered rows, for a palette change: the
// rows carry their colors, so they must be rendered again.
func (m *tuiModel) repaintAll() {
	for i := range m.log {
		m.log[i].wrapped = nil
	}
	if m.pane != nil {
		for i := range m.pane.lines {
			m.pane.lines[i].wrapped = nil
		}
	}
}

// resetTranscript empties the transcript and everything that describes it.
func (m *tuiModel) resetTranscript() {
	m.log, m.logCap, m.shown, m.messages = nil, 0, nil, nil
	m.older = tuiOlder{}
	m.scroll = 0
}

// closeThread returns to the list screen and drops the thread view's state,
// so reopening a thread starts clean. What was typed stays with the thread.
// The thread counts as read as of the next list refresh.
func (m *tuiModel) closeThread() {
	if m.thread.ID != "" {
		if m.seen != nil {
			m.seen[m.thread.ID] = -1
		}
		if m.drafts == nil {
			m.drafts = map[string]string{}
		}
		if draft := m.input.string(); strings.TrimSpace(draft) != "" {
			m.drafts[m.thread.ID] = draft
		} else {
			delete(m.drafts, m.thread.ID)
			if m.thread.ID == m.fresh {
				m.startJob(tuiDiscardJob(m.fresh))
			}
		}
		if m.thread.ID == m.fresh {
			m.fresh = ""
		}
	}
	m.screen = tuiScreenList
	m.thread = threadListRow{}
	m.project = ""
	m.resetTranscript()
	m.pending = nil
	m.pane = nil
	m.input.clear()
	m.slashSel = 0
	m.mention = tuiMention{}
	m.roster = map[string]contracts.AgentStatus{}
	m.harness = map[string]string{}
	m.started = map[string]time.Time{}
}

// setRoster replaces what is known about the thread's agents with a fresh
// agents.list read.
func (m *tuiModel) setRoster(r tuiRoster) {
	m.roster = make(map[string]contracts.AgentStatus, len(r.statuses))
	for id, status := range r.statuses {
		m.roster[id] = status
	}
	m.harness = map[string]string{}
	for id, h := range r.harness {
		m.harness[id] = h
	}
	m.started = map[string]time.Time{}
	for id, at := range r.started {
		m.started[id] = at
	}
	m.parent, m.role = r.parent, r.role
}

// observeAgent records one agent status from a live event. An agent seen to
// start running has its clock started here, unless the roster already knew.
func (m *tuiModel) observeAgent(id string, status contracts.AgentStatus) {
	if m.roster == nil {
		m.roster = map[string]contracts.AgentStatus{}
	}
	if status == contracts.AgentRunning && m.roster[id] != contracts.AgentRunning {
		m.observeStart(id, m.now)
	}
	m.roster[id] = status
}

// observeStart records when an agent began, keeping the earliest time known.
func (m *tuiModel) observeStart(id string, at time.Time) {
	if at.IsZero() {
		return
	}
	if m.started == nil {
		m.started = map[string]time.Time{}
	}
	if known, ok := m.started[id]; !ok || at.Before(known) {
		m.started[id] = at
	}
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
	m.choice = 0
}

// choosing reports whether the arrows pick an option: a single-choice
// question is open and nothing is typed, so the keys cannot be meant for text.
func (m *tuiModel) choosing() bool {
	return m.pending != nil && m.pending.kind == contracts.QuestionChoice &&
		len(m.pending.options) > 0 && m.input.empty()
}

// moveChoice moves the highlighted option by d, stopping at the ends, and
// reports whether an option list was there to take the key.
func (m *tuiModel) moveChoice(d int) bool {
	if !m.choosing() {
		return false
	}
	m.choice = min(max(m.choice+d, 0), len(m.pending.options)-1)
	return true
}

// pendingChoice is the answer an empty Enter gives: the highlighted option's
// number, the form answerQuestion parses, or "" when there is nothing to pick.
func (m *tuiModel) pendingChoice() string {
	if !m.choosing() {
		return ""
	}
	return strconv.Itoa(m.choice + 1)
}

// closeQuestion clears a pending question (answered elsewhere or withdrawn).
func (m *tuiModel) closeQuestion() {
	m.pending = nil
}

// typeRune types one rune into the composer.
func (m *tuiModel) typeRune(r rune) {
	m.input.insert(r)
	m.slashSel = 0
}

// backspace deletes the rune before the cursor.
func (m *tuiModel) backspace() {
	m.input.backspace()
	m.slashSel = 0
}

// takeInput returns the typed text and clears the editor, remembering it for
// recall. The text is what handleAttachLine-shaped routing parses: an answer
// when a question is pending, a steer or slash command otherwise.
func (m *tuiModel) takeInput() string {
	m.slashSel = 0
	delete(m.drafts, m.thread.ID)
	return m.input.take()
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
		lines = append(lines, tuiFooter(cols, m.hints(cols, bodyRows)))
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
// host, and the build — the build only when withVersion is set. While the
// connection is down the dot turns and the label says so.
func (m *tuiModel) daemonRow(withVersion bool) tuiRow {
	info := m.info.daemon
	if m.info.offline {
		return tuiText("○ ", tuiInk{fg: tuiColWarn, bg: tuiColBar}).
			join(tuiText(info+" · reconnecting… ", tuiInk{fg: tuiColWarn, bg: tuiColBar}))
	}
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
	ids, leftCut, rightCut := tuiWindowTabs(tuiViewOrder(), m.view, cols-mark.width()-right.width()-1, m.tabBadge)
	row := mark
	if leftCut {
		row = row.join(tuiText("…", tuiInk{fg: tuiColFaint, bg: tuiColBar}), tuiText(" ", bar))
	}
	for _, id := range ids {
		ink := tuiInk{fg: tuiColDim, bg: tuiColBar}
		if id == m.view {
			ink = tuiInk{fg: tuiColInk, bg: tuiColSelect, bold: true}
		}
		row = row.join(tuiText(" "+tuiTabTitle(id)+" ", ink))
		if badge := m.tabBadge(id); badge != "" {
			row = row.join(tuiText(badge+" ", tuiInk{fg: tuiColAccent, bg: tuiColBar, bold: true}))
		}
		row = row.join(tuiText(" ", bar))
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
func tuiTabCells(id, badge string) int {
	cells := stringCells(tuiTabTitle(id)) + 3
	if badge != "" {
		cells += stringCells(badge) + 1
	}
	return cells
}

// tabBadge is the small count after a tab's title: the Threads tab says how
// many threads are working, or how many are waiting to be read, so the bar
// answers "is anything happening" from any tab.
func (m *tuiModel) tabBadge(id string) string {
	if id != tuiThreadsViewID {
		return ""
	}
	running := 0
	for _, row := range m.rows {
		if row.thread.RunningAgents > 0 {
			running++
		}
	}
	if running > 0 {
		return fmt.Sprintf("●%d", running)
	}
	if n := m.unreadCount(); n > 0 {
		return fmt.Sprintf("%d new", n)
	}
	return ""
}

// tuiWindowTabs picks the tabs the header shows when they cannot all fit:
// a window around the active one, with an ellipsis standing for each hidden
// run. Two cells per possible ellipsis are reserved before fitting tabs, so
// the markers never steal the daemon label's space.
func tuiWindowTabs(order []string, active string, budget int, badge func(string) string) (ids []string, leftCut, rightCut bool) {
	widths := make([]int, len(order))
	total := 0
	for i, id := range order {
		widths[i] = tuiTabCells(id, badge(id))
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

// hints are the footer's key hints for the current surface. The sidebar's
// toggle is named only while the sidebar is hidden, since that is when the
// user needs to be told it exists.
func (m *tuiModel) hints(cols, rows int) []tuiHint {
	if m.palette != nil {
		return []tuiHint{{"enter", "run"}, {"↑/↓", "move"}, {"esc", "close"}}
	}
	if m.dialog != nil {
		if m.dialog.items != nil {
			return []tuiHint{{"enter", "choose"}, {"↑/↓", "move"}, {"esc", "cancel"}}
		}
		return []tuiHint{{"enter", "confirm"}, {"esc", "cancel"}}
	}
	if v, ok := m.activeView(); ok {
		if len(v.Hints) > 0 {
			return v.Hints
		}
		return []tuiHint{{"←/→", "tabs"}, {"esc", "threads"}, {"ctrl+k", "commands"}, {"q", "quit"}}
	}
	if m.pane != nil && m.screen == tuiScreenThread {
		return []tuiHint{{"pgup/pgdn", "scroll"}, {"←/→", "tabs"}, {"esc", "close"}}
	}
	if m.screen == tuiScreenThread {
		hints := []tuiHint{{"enter", "send"}, {"alt+enter", "newline"}, {"tab", "views"}, {"esc", "back"}}
		if m.input.empty() {
			hints[2] = tuiHint{"←/→", "tabs"}
		}
		if m.sideFits(rows) && !m.sideShown(cols, rows) {
			hints = append(hints, tuiHint{"ctrl+b", "sidebar"})
		}
		return append(hints, tuiHint{"ctrl+k", "commands"}, tuiHint{"↑", "history"}, tuiHint{"pgup/pgdn", "scroll"})
	}
	if m.filtering() {
		return []tuiHint{{"enter", "keep filter"}, {"esc", "clear"}}
	}
	hints := []tuiHint{{"↑/↓", "select"}, {"enter", "open"}, {"n", "new"}, {"s", "new solo"}, {"/", "filter"}, {"←/→", "tabs"}}
	if len(m.rows) > 0 && m.sideFits(rows) && cols >= tuiPreviewMinCols {
		hints = append(hints, tuiHint{"ctrl+b", "preview"})
	}
	return append(hints, tuiHint{"ctrl+k", "commands"}, tuiHint{"q", "quit"})
}

// renderList draws the thread list the way opencode's session picker looks: a
// quiet heading, then one row per thread with the selected one washed in the
// primary color. No frame; the rows own the width.
func (m *tuiModel) renderList(cols, rows int) []tuiRow {
	m.side.paintCols, m.side.paintRows = cols, rows
	sel, ok := m.selected()
	if !ok || !m.previewShown(cols, rows) {
		return m.renderListRows(cols, rows)
	}
	// The selected thread's preview takes the sidebar's place beside the list.
	mainCols := cols - tuiSideWidth
	side := append([]tuiRow{nil}, m.previewRows(sel, tuiSideWidth-2)...)
	return tuiBeside(m.renderListRows(mainCols, rows), mainCols, side, tuiSideWidth-2)
}

// renderListRows is the list itself, cols wide.
func (m *tuiModel) renderListRows(cols, rows int) []tuiRow {
	if rows < 1 || cols < 4 {
		return fitRows(nil, rows)
	}
	innerW := cols - 4
	list := m.listRows()
	heading := tuiText("Threads", tuiInk{fg: tuiColText, bold: true}).
		join(tuiText(fmt.Sprintf("  %d", len(list)), tuiInk{fg: tuiColFaint}))
	if f := m.filter; f != nil {
		heading = heading.join(tuiText("  /"+string(f.query), tuiInk{fg: tuiColAccent2, bold: true}))
		if f.typing {
			heading = heading.join(tuiText("▌", tuiInk{fg: tuiColAccent}))
		}
	}
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
		out = append(out, m.homeRows(cols, rows-len(out), "press n to start a thread, or s for a solo one")...)
	case len(list) == 0:
		out = append(out, tuiText("  no thread matches the filter · esc clears it", tuiInk{fg: tuiColFaint}).fit(cols))
	case rows-len(out) >= 1:
		visible, base := tuiListWindow(list, m.cursor, rows-len(out))
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
		tuiSoloBadge(row.thread),
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
	m.side.paintCols, m.side.paintRows = cols, rows
	if !m.sideShown(cols, rows) {
		return m.renderThreadMain(cols, rows)
	}
	if cols < tuiSideMinCols {
		return m.sidePane(cols, rows)
	}
	mainCols := cols - tuiSideWidth
	return m.withSidebar(m.renderThreadMain(mainCols, rows), mainCols, rows)
}

func (m *tuiModel) renderThreadMain(cols, rows int) []tuiRow {
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
		tail = m.questionRows(innerW)
	}
	tail = append(tail, m.statusRow(innerW))
	tail = append(tail, m.slashRows(innerW)...)
	tail = append(tail, m.mentionRows(innerW)...)
	if len(tail) > innerH {
		tail = tail[len(tail)-innerH:]
	}
	// The painted geometry is what scrolling measures in: rows of this width,
	// a page of this height.
	m.logW, m.logH = innerW, max(innerH-len(tail), 0)
	var pane []tuiRow
	if len(m.log) == 0 && m.pending == nil && m.scroll == 0 {
		pane = m.homeRows(innerW, m.logH, "type a message and press enter to start")
	} else {
		pane = m.logPane(innerW, m.logH)
	}
	inner := append(pane, tail...)
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

// homeRows is the empty-state home, opencode style: the wordmark centered a
// little above the middle of the pane, with one quiet tip under it.
func (m *tuiModel) homeRows(width, height int, tip string) []tuiRow {
	if height < 1 {
		return nil
	}
	lines := []tuiRow{
		tuiText("◆ ", tuiInk{fg: tuiColAccent, bold: true}).join(
			tuiText("Agent", tuiInk{fg: tuiColFaint, bold: true}),
			tuiText("Tunnel", tuiInk{fg: tuiColText, bold: true})),
		tuiText("bring your own subscription", tuiInk{fg: tuiColFaint}),
		nil,
		tuiText(tip, tuiInk{fg: tuiColDim}),
	}
	if height < len(lines) {
		lines = lines[len(lines)-1:]
	}
	top := max((height-len(lines))/2-1, 0)
	out := make([]tuiRow, 0, height)
	for i := 0; i < top; i++ {
		out = append(out, nil)
	}
	for _, l := range lines {
		pad := max((width-l.width())/2, 0)
		out = append(out, tuiText(strings.Repeat(" ", pad), tuiInk{}).join(l).fit(width))
	}
	return fitRows(out, height)
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
// Once the roster is known it is the source: the list row the thread was
// opened from is a snapshot, and would keep saying "running" after the turn
// had ended.
func (m *tuiModel) threadStatus() tuiRow {
	row := m.thread
	if len(m.roster) > 0 {
		row.Agents, row.RunningAgents = len(m.roster), 0
		for _, status := range m.roster {
			if status == contracts.AgentRunning || status == contracts.AgentPending {
				row.RunningAgents++
			}
		}
	}
	status := threadRowStatus(row)
	return tuiSoloBadge(row).join(tuiText("● ", tuiStatusInk(status))).join(
		tuiText(status, tuiStatusInk(status)),
		tuiText(fmt.Sprintf("  %d/%d agents", row.RunningAgents, row.Agents), tuiInk{fg: tuiColDim}),
	)
}

// tuiInputMaxRows is how tall the composer's text may grow before it scrolls.
const tuiInputMaxRows = 6

// inputBox is the prompt: a panel block with a colored left gutter, a padding
// row above and below, and the typed text wrapped over as many rows as it
// needs, up to tuiInputMaxRows or a third of the screen. The caret is a bar
// after the last rune, or the cell under the cursor inverted. A question owns
// the prompt in the warning color. Too short a screen gets the bare line.
func (m *tuiModel) inputBox(cols, rows int) []tuiRow {
	gutterInk, prompt := tuiInk{fg: tuiColAccent, bg: tuiColBar}, ""
	if m.pending != nil {
		gutterInk, prompt = tuiInk{fg: tuiColWarn, bg: tuiColBar}, "? "
	}
	bar := tuiInk{bg: tuiColBar}
	text := tuiInk{fg: tuiColText, bg: tuiColBar}
	// One cell is kept free at the right edge for the caret past the text.
	width := max(cols-2-stringCells(prompt)-1, 1)
	lines, curRow, curCol := m.input.view(width, max(min(tuiInputMaxRows, rows/3), 1))
	out := make([]tuiRow, 0, len(lines)+2)
	if rows >= 8 {
		out = append(out, tuiText("┃ ", gutterInk).padTo(cols, bar))
	}
	for i, line := range lines {
		row := tuiText(line, text)
		if i == curRow {
			row = tuiCaretRow(row, curCol, text)
		}
		lead := tuiText(prompt, tuiInk{fg: tuiColWarn, bg: tuiColBar, bold: true})
		if i > 0 {
			lead = tuiText(strings.Repeat(" ", stringCells(prompt)), bar)
		}
		out = append(out, tuiText("┃ ", gutterInk).join(lead, row).fit(cols).padTo(cols, bar))
	}
	if rows >= 8 {
		out = append(out, tuiText("┃ ", gutterInk).padTo(cols, bar))
	}
	return out
}

// tuiCaretRow draws the caret on a composer row at cell col: a bar after the
// text when the cursor is past its end, otherwise the cell under it inverted.
func tuiCaretRow(row tuiRow, col int, ink tuiInk) tuiRow {
	if col >= row.width() {
		return row.join(tuiText("▌", tuiInk{fg: tuiColAccent, bg: ink.bg}))
	}
	under := row.cut(col, col+1)
	end := col + max(under.width(), 1)
	caret := tuiText(under.plain(), tuiInk{fg: tuiColInk, bg: tuiColAccent})
	return row.cut(0, col).join(caret, row.cut(end, row.width()))
}

// questionRows renders the pending question. A single choice lists its options
// one per row with the highlighted one marked: the arrows move it and Enter
// answers with it, or a typed number answers directly. Other kinds keep the
// hint printQuestion gives.
func (m *tuiModel) questionRows(width int) []tuiRow {
	q := *m.pending
	warn := tuiInk{fg: tuiColWarn, bold: true}
	if q.kind == contracts.QuestionChoice && len(q.options) > 0 {
		rows := tuiWrapRow(tuiText("? "+tuiSanitize(questionText(q)), warn), width, 2)
		for i, option := range q.options {
			mark, ink := "  ", tuiInk{fg: tuiColText}
			if i == m.choice && m.choosing() {
				mark, ink = "▸ ", tuiInk{fg: tuiColAccent, bold: true}
			}
			label := fmt.Sprintf("%d) %s", i+1, tuiSanitize(option))
			rows = append(rows, tuiText(mark, tuiInk{fg: tuiColAccent}).join(tuiText(label, ink)).fit(width))
		}
		return append(rows, tuiText("  ↑/↓ choose · enter answer · or type a number", tuiInk{fg: tuiColFaint}).fit(width))
	}
	hint := ""
	if q.kind == contracts.QuestionMulti {
		hint = " (answer with numbers separated by commas)"
	}
	rows := tuiWrapRow(tuiText("? "+tuiSanitize(questionText(q))+hint, warn), width, 2)
	if len(q.options) > 0 {
		options := make([]string, len(q.options))
		for i, option := range q.options {
			options[i] = fmt.Sprintf("%d) %s", i+1, tuiSanitize(option))
		}
		rows = append(rows, tuiText("  "+strings.Join(options, "  "), tuiInk{fg: tuiColText}).fit(width))
	}
	return rows
}

// tuiWrap hard-wraps a plain line at width cells; a wide rune that would
// straddle the boundary starts the next row. It is for text whose exact
// characters matter (commands, raw output); prose reflows through tuiWrapRow.
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
