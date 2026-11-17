package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The thread view is a transcript: a block per run of output from one speaker,
// a name row where the speaker changes, and the user's own messages in line.
// Streamed chunks grow the last block instead of stacking as rows.

// tuiLogCap bounds the transcript at the daemon's own thread.logs tail size,
// so a long-lived TUI grows no unbounded slice.
const tuiLogCap = 400

// Block kinds beyond the daemon's log kinds.
const (
	tuiKindUser   = "user"     // a message the user sent
	tuiKindSteer  = "steering" // a message the user sent into a running turn
	tuiKindShell  = "shell"    // a shell command the user ran in the thread
	tuiKindSystem = "system"   // the TUI's own notes: help, command results
	tuiKindStatus = "status"   // agent lifecycle and dispatch
)

// tuiSpeakerYou is the speaker of the user's own blocks.
const tuiSpeakerYou = "you"

// tuiBlock is one stored unit of a transcript: text of one kind from one
// speaker. head marks a change of speaker, where a name row leads the block;
// gap sets it off from the block before with a blank row.
type tuiBlock struct {
	agent string
	kind  string
	text  string
	at    time.Time
	head  bool
	gap   bool
	// wrapW/wrapped memoize rows(width): a block changes only when a chunk
	// grows it, so a repaint reuses the rendered rows instead of re-rendering
	// up to tuiLogCap blocks every frame.
	wrapW   int
	wrapped []tuiRow
}

// tuiStreams reports whether chunks of kind continue one another: harnesses
// stream prose and reasoning in pieces that carry their own line breaks.
func tuiStreams(kind string) bool {
	return kind == string(contracts.LogText) || kind == string(contracts.LogReasoning)
}

// tuiAppendBlock adds one piece of output to blocks. A streamed chunk from the
// speaker and kind of the last block grows it; anything else starts a block,
// headed when the speaker changed or the user sent it.
func tuiAppendBlock(blocks []tuiBlock, agent, kind, text string, at time.Time) []tuiBlock {
	if n := len(blocks); n > 0 {
		last := &blocks[n-1]
		if tuiStreams(kind) && last.kind == kind && last.agent == agent {
			last.text += text
			last.wrapped = nil
			return blocks
		}
	}
	// Each thing the user sent is its own utterance, named even when it
	// follows another of theirs.
	sent := kind == tuiKindUser || kind == tuiKindSteer || kind == tuiKindShell
	head := agent != "" && (sent || len(blocks) == 0 || blocks[len(blocks)-1].agent != agent)
	return append(blocks, tuiBlock{agent: agent, kind: kind, text: text, at: at, head: head, gap: head && len(blocks) > 0})
}

// tuiAppendNote adds a headless note. One that follows a speaker's output is
// set off by a blank row; notes in a run stay together.
func tuiAppendNote(blocks []tuiBlock, kind, text string) []tuiBlock {
	note := tuiNoteBlock(kind, text)
	note.gap = len(blocks) > 0 && blocks[len(blocks)-1].agent != ""
	return append(blocks, note)
}

// tuiLogBlocks folds log entries into blocks, for a pane that shows one
// agent's whole log.
func tuiLogBlocks(entries ...contracts.LogEntry) []tuiBlock {
	var blocks []tuiBlock
	for _, e := range entries {
		blocks = tuiAppendBlock(blocks, string(e.AgentID), tuiEntryKind(e), e.Text, e.At)
	}
	return blocks
}

func tuiEntryKind(e contracts.LogEntry) string {
	if e.Kind == "" {
		return string(contracts.LogText)
	}
	return string(e.Kind)
}

// tuiNoteBlock is a headless block of plain lines: the TUI's own notes, and
// the rows of panes built from lines (mailbox, tasks).
func tuiNoteBlock(kind, text string) tuiBlock {
	return tuiBlock{kind: kind, text: text}
}

// rows renders the block's body to width: two cells of gutter, then the text
// the way its kind reads. Prose renders as markdown and reflows at words;
// commands, errors and raw output keep their exact characters and wrap at the
// cell. Text is stripped of control bytes first: agent output is not trusted
// to be printable, and a raw ESC or CSI would break the frame geometry.
func (b *tuiBlock) rows(width int) []tuiRow {
	if b.wrapped != nil && b.wrapW == width {
		return b.wrapped
	}
	body := max(width-2, 1)
	ink := tuiKindInk(b.kind)
	var content []tuiRow
	switch b.kind {
	case string(contracts.LogText):
		content = tuiMarkdownRows(b.text, body, ink)
	case string(contracts.LogReasoning), tuiKindUser, tuiKindSteer, tuiKindSystem, tuiKindStatus:
		for _, line := range tuiPlainLines(b.text) {
			content = append(content, tuiWrapRow(tuiText(line, ink), body, 0)...)
		}
	default:
		for _, line := range tuiPlainLines(b.text) {
			for _, part := range tuiWrap(line, body) {
				content = append(content, tuiText(part, ink))
			}
		}
	}
	gutter := tuiGutter(b.kind)
	out := make([]tuiRow, len(content))
	for i, row := range content {
		out[i] = gutter.join(row).fit(width)
	}
	b.wrapped, b.wrapW = out, width
	return out
}

// tuiPlainLines splits text into printable lines: surrounding blank lines
// dropped, tabs expanded so a cell count matches what the terminal draws, and
// control bytes stripped.
func tuiPlainLines(text string) []string {
	text = strings.Trim(text, "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = tuiSanitize(strings.ReplaceAll(line, "\t", "    "))
	}
	return lines
}

// tuiGutter is the two cells left of a row: a colored bar for the kinds that
// deserve one, blank for plain output.
func tuiGutter(kind string) tuiRow {
	switch kind {
	case string(contracts.LogTool), tuiKindShell:
		return tuiText("┃ ", tuiInk{fg: tuiColAccent2})
	case string(contracts.LogError):
		return tuiText("┃ ", tuiInk{fg: tuiColErr})
	case string(contracts.LogReasoning):
		return tuiText("┃ ", tuiInk{fg: tuiColAgent})
	case tuiKindUser, tuiKindSteer:
		return tuiText("┃ ", tuiInk{fg: tuiColAccent})
	}
	return tuiText("  ", tuiInk{})
}

// --- The open thread's transcript ---

// appendLog folds agent output into the transcript.
func (m *tuiModel) appendLog(entries ...contracts.LogEntry) {
	m.grow(func() {
		for _, e := range entries {
			m.log = tuiAppendBlock(m.log, string(e.AgentID), tuiEntryKind(e), e.Text, e.At)
		}
	})
}

// appendMessage adds a message the user sent: a prompt, a steer, or a shell
// command. Other authors are skipped, since their words already arrive as
// agent output. A message seen before (the backlog and the live stream can
// both carry it) is added once.
func (m *tuiModel) appendMessage(id, author, text string, at time.Time) {
	kind, speaker := "", tuiSpeakerYou
	switch author {
	case "user":
		kind = tuiKindUser
	case "steering":
		kind = tuiKindSteer
	case "shell":
		kind, speaker = tuiKindShell, "shell"
	default:
		return
	}
	if id != "" {
		if m.shown[id] {
			return
		}
		if m.shown == nil {
			m.shown = map[string]bool{}
		}
		m.shown[id] = true
	}
	m.grow(func() {
		m.log = tuiAppendBlock(m.log, speaker, kind, text, at)
	})
}

// appendLogLines appends the TUI's own notes (help, command results).
func (m *tuiModel) appendLogLines(lines ...string) {
	m.appendNote(tuiKindSystem, lines...)
}

// appendStatusLines appends agent lifecycle and dispatch notes.
func (m *tuiModel) appendStatusLines(lines ...string) {
	m.appendNote(tuiKindStatus, lines...)
}

func (m *tuiModel) appendNote(kind string, lines ...string) {
	if len(lines) == 0 {
		return
	}
	m.grow(func() {
		m.log = tuiAppendNote(m.log, kind, strings.Join(lines, "\n"))
	})
}

// grow runs an append and keeps the view steady around it. A reader who
// scrolled up stays on the rows they were reading — the offset grows by the
// rows that arrived — while a following view (scroll zero) rides the tail.
// The oldest blocks fall off past the cap.
func (m *tuiModel) grow(appendBlocks func()) {
	before := 0
	if m.scroll > 0 {
		before = m.logRows()
	}
	appendBlocks()
	if m.scroll > 0 {
		m.scroll += m.logRows() - before
	}
	if over := len(m.log) - tuiLogCap; over > 0 {
		m.log = m.log[over:]
	}
	m.clampScroll()
}

// tuiLogWidthDefault stands in for the transcript width before the first
// paint has recorded the real one.
const tuiLogWidthDefault = 78

// logWidth is the width the transcript was last painted at.
func (m *tuiModel) logWidth() int {
	if m.logW > 0 {
		return m.logW
	}
	return tuiLogWidthDefault
}

// blockRows is block i as painted: a blank row where the block is set off
// from the one before, the speaker's name where the speaker changes, then the
// body.
func (m *tuiModel) blockRows(i, width int) []tuiRow {
	b := &m.log[i]
	body := b.rows(width)
	if !b.head && !b.gap {
		return body
	}
	out := make([]tuiRow, 0, len(body)+2)
	if b.gap {
		out = append(out, nil)
	}
	if b.head {
		out = append(out, m.speakerRow(b).fit(width))
	}
	return append(out, body...)
}

// blockHeight is len(blockRows(i, width)) without building the name row.
func (m *tuiModel) blockHeight(i, width int) int {
	b := &m.log[i]
	n := len(b.rows(width))
	if b.gap {
		n++
	}
	if b.head {
		n++
	}
	return n
}

// logRows is the transcript's height in display rows at the painted width.
func (m *tuiModel) logRows() int {
	width, n := m.logWidth(), 0
	for i := range m.log {
		n += m.blockHeight(i, width)
	}
	return n
}

// speakerRow names who a block is from: the agent in its harness tint with
// the harness beside it, or the user, and when it started.
func (m *tuiModel) speakerRow(b *tuiBlock) tuiRow {
	ink := tuiHarnessInk(m.harness[b.agent])
	if b.agent == tuiSpeakerYou {
		ink = tuiInk{fg: tuiColAccent}
	}
	ink.bold = true
	row := tuiText(b.agent, ink)
	if b.kind == tuiKindSteer {
		row = row.join(tuiText(" · steer", tuiInk{fg: tuiColFaint}))
	}
	if h := m.harness[b.agent]; h != "" {
		row = row.join(tuiText(" · "+h, tuiInk{fg: tuiColFaint}))
	}
	if !b.at.IsZero() {
		row = row.join(tuiText("  "+b.at.Local().Format("15:04"), tuiInk{fg: tuiColFaint}))
	}
	return row
}

// logPane picks the displayed transcript rows: the window holds `height`
// display rows and stops `scroll` rows short of the end, so a scrolled view
// stays pinned to its content. A page with less content than room pads with
// blank rows below it.
func (m *tuiModel) logPane(cols, height int) []tuiRow {
	if height <= 0 {
		return nil
	}
	// Walk backwards from the newest block, skipping the scrolled-past rows,
	// collecting whole blocks until the page is full.
	var parts [][]tuiRow
	skip, have := m.scroll, 0
	for i := len(m.log) - 1; i >= 0 && have < height; i-- {
		rows := m.blockRows(i, cols)
		if skip >= len(rows) {
			skip -= len(rows)
			continue
		}
		rows = rows[:len(rows)-skip]
		skip = 0
		if missing := height - have; len(rows) > missing {
			rows = rows[len(rows)-missing:]
		}
		parts = append(parts, rows)
		have += len(rows)
	}
	visible := make([]tuiRow, 0, height)
	for i := len(parts) - 1; i >= 0; i-- {
		visible = append(visible, parts[i]...)
	}
	return fitRows(visible, height)
}

// scrollUp moves toward older output; leaving the very bottom stops following.
func (m *tuiModel) scrollUp(n int) {
	m.scroll += n
	m.clampScroll()
}

// scrollDown moves toward newer output; reaching the bottom resumes following.
func (m *tuiModel) scrollDown(n int) {
	m.scroll -= n
	m.clampScroll()
}

// following reports whether new output stays on screen.
func (m *tuiModel) following() bool {
	return m.scroll == 0
}

// clampScroll keeps the offset between the tail and the point where the
// oldest row sits at the top of the pane.
func (m *tuiModel) clampScroll() {
	if m.scroll <= 0 {
		m.scroll = 0
		return
	}
	limit := max(m.logRows()-max(m.logH, 1), 0)
	m.scroll = min(m.scroll, limit)
}

// lastReply is the newest passage of agent prose in the transcript, the text
// /copy takes, or "" when no agent has said anything yet.
func (m *tuiModel) lastReply() string {
	for i := len(m.log) - 1; i >= 0; i-- {
		if b := m.log[i]; b.kind == string(contracts.LogText) && b.agent != "" {
			return strings.TrimSpace(b.text)
		}
	}
	return ""
}

// --- Backlog ---

// tuiBacklog interleaves a thread's agent output with the messages the user
// sent, by time, so a reopened thread reads as the conversation it was. The
// output is a tail; of the messages older than it only the newest is kept,
// as the prompt the visible work answers.
func (m *tuiModel) appendBacklog(entries []contracts.LogEntry, messages []tuiMessageRow) {
	messages = append([]tuiMessageRow(nil), messages...)
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].At.Before(messages[j].At) })
	if len(entries) > 0 && !entries[0].At.IsZero() {
		first := 0
		for first < len(messages) && messages[first].At.Before(entries[0].At) {
			first++
		}
		if first > 1 {
			messages = messages[first-1:]
		}
	}
	next := 0
	for _, e := range entries {
		for next < len(messages) && !e.At.IsZero() && !messages[next].At.After(e.At) {
			msg := messages[next]
			m.appendMessage(msg.ID, msg.Author, msg.Text, msg.At)
			next++
		}
		m.appendLog(e)
	}
	for ; next < len(messages); next++ {
		msg := messages[next]
		m.appendMessage(msg.ID, msg.Author, msg.Text, msg.At)
	}
}

// --- The line under the transcript ---

// tuiSpinner is the working indicator's frames, one per tenth of a second.
var tuiSpinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// working reports whether any agent of the open thread is running or queued.
// The indicator and its clock show only while this is true, so they never
// outlive the work.
func (m *tuiModel) working() bool {
	for _, status := range m.roster {
		if status == contracts.AgentRunning || status == contracts.AgentPending {
			return true
		}
	}
	return false
}

// workingSince is when the earliest still-active agent started, or zero when
// no start time is known.
func (m *tuiModel) workingSince() time.Time {
	var since time.Time
	for id, status := range m.roster {
		if status != contracts.AgentRunning && status != contracts.AgentPending {
			continue
		}
		if at := m.started[id]; !at.IsZero() && (since.IsZero() || at.Before(since)) {
			since = at
		}
	}
	return since
}

// tuiElapsed words a duration the way a stopwatch reads: 42s, 3m07s, 1h02m.
func tuiElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
}

// statusRow is the line between the transcript and the prompt: a spinner and
// clock while agents work, the roster, and — when the reader scrolled up —
// how far the view sits above the newest output.
func (m *tuiModel) statusRow(width int) tuiRow {
	var row tuiRow
	if m.working() {
		frame := tuiSpinner[int(m.now.UnixMilli()/100)%len(tuiSpinner)]
		row = tuiText(string(frame)+" working", tuiInk{fg: tuiColWarn, bold: true})
		if since := m.workingSince(); !since.IsZero() {
			row = row.join(tuiText(" "+tuiElapsed(m.now.Sub(since)), tuiInk{fg: tuiColDim}))
		}
		row = row.join(tuiText("  ", tuiInk{}))
	}
	row = row.join(m.rosterRow())
	if m.following() {
		return row.fit(width)
	}
	note := tuiText(fmt.Sprintf("↑ %d rows above the latest · end to follow", m.scroll), tuiInk{fg: tuiColAccent})
	if width <= note.width()+2 {
		return note.fit(width)
	}
	left := row.fit(width - note.width() - 2)
	return left.padTo(width-note.width(), tuiInk{}).join(note)
}

// rosterRow is the agents and their statuses, sorted by agent id so the line
// is stable between renders. Names take their harness tint.
func (m *tuiModel) rosterRow() tuiRow {
	if len(m.roster) == 0 {
		return tuiText("agents (none)", tuiInk{fg: tuiColDim})
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
		row = row.join(
			tuiText(id, tuiHarnessInk(m.harness[id])),
			tuiText(":", tuiInk{fg: tuiColFaint}),
			tuiText(status, tuiStatusInk(status)),
		)
	}
	return row
}
