package cli

import (
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiDetail is a full-body text view over one thread: an agent's complete logs,
// its mailbox, or its task list. It replaces the log pane until closed, and
// closes with the thread.
type tuiDetail struct {
	title string
	lines []tuiLogLine
	// top is the first display row shown. follow pins the view to the newest
	// rows, which suits logs; a reader who scrolls up stops following.
	top    int
	follow bool

	// rows caches the wrapped lines for the width they were built at, so a
	// repaint does not rewrap a long agent log every frame.
	rows   []tuiRow
	cacheW int
	cached bool
}

// rowsFor wraps the pane's lines to width, reusing the cache when it fits.
func (p *tuiDetail) rowsFor(width int) []tuiRow {
	if p.cached && p.cacheW == width {
		return p.rows
	}
	rows := []tuiRow{}
	for _, l := range p.lines {
		rows = append(rows, l.rows(width)...)
	}
	p.rows, p.cacheW, p.cached = rows, width, true
	return rows
}

// openPane shows lines as a full-body view. follow starts it at the newest rows.
func (m *tuiModel) openPane(title string, lines []tuiLogLine, follow bool) {
	m.pane = &tuiDetail{title: title, lines: lines, follow: follow}
}

// paneRows draws the open pane: a title row, then as many wrapped rows as fit.
// The offset is clamped here because only the render knows the wrapped length;
// the key handler moves it freely and the render settles it.
func (m *tuiModel) paneRows(cols, rows int) []tuiRow {
	p := m.pane
	all := p.rowsFor(cols)
	page := max(rows-1, 0)
	maxTop := max(len(all)-page, 0)
	if p.follow {
		p.top = maxTop
	}
	p.top = min(max(p.top, 0), maxTop)
	end := min(p.top+page, len(all))
	out := append([]tuiRow{tuiText(p.title, tuiInk{fg: tuiColAccent, bold: true})}, all[p.top:end]...)
	return fitRows(out, rows)
}

// tuiPaneKey scrolls the pane. Esc and q close it; any other key is consumed so
// it cannot reach the thread's input while the pane has the screen.
func tuiPaneKey(m *tuiModel, k tuiKey, rows int) {
	p := m.pane
	page := tuiPage(rows)
	switch k.Kind {
	case keyEsc:
		m.pane = nil
	case keyUp:
		p.follow, p.top = false, p.top-1
	case keyDown:
		p.follow, p.top = false, p.top+1
	case keyPgUp:
		p.follow, p.top = false, p.top-page
	case keyPgDn:
		p.follow, p.top = false, p.top+page
	case keyRune:
		if k.Rune == 'q' {
			m.pane = nil
		}
	}
}

// mailboxLines renders a thread's peer mailbox, oldest first.
func mailboxLines(box []tuiMailboxRow) []tuiLogLine {
	if len(box) == 0 {
		return []tuiLogLine{{text: "no peer messages", kind: "system"}}
	}
	var lines []tuiLogLine
	for _, msg := range box {
		head := fmt.Sprintf("%s %s → %s", msg.At.Local().Format("15:04:05"), msg.From, msg.To)
		if msg.Kind != "" {
			head += " (" + msg.Kind + ")"
		}
		lines = append(lines, tuiLogLine{text: head, kind: "status"})
		for _, body := range strings.Split(strings.TrimRight(msg.Body, "\n"), "\n") {
			lines = append(lines, tuiLogLine{text: "  " + body, kind: string(contracts.LogText)})
		}
	}
	return lines
}

// taskLines renders a thread's task list in plan order.
func taskLines(tasks []tuiTaskRow) []tuiLogLine {
	if len(tasks) == 0 {
		return []tuiLogLine{{text: "no tasks planned", kind: "system"}}
	}
	lines := make([]tuiLogLine, 0, len(tasks))
	for _, task := range tasks {
		title := task.Title
		if title == "" {
			title = task.Name
		}
		lines = append(lines, tuiLogLine{text: fmt.Sprintf("[%s] %s", task.Status, title), kind: "status"})
	}
	return lines
}
