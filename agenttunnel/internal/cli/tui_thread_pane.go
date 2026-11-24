package cli

import (
	"fmt"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiDetail is a full-body text view over one thread: an agent's complete logs,
// its mailbox, or its task list. It replaces the log pane until closed, and
// closes with the thread.
type tuiDetail struct {
	title string
	lines []tuiBlock
	// top is the first display row shown. follow pins the view to the newest
	// rows, which suits logs; a reader who scrolls up stops following. The
	// rendered rows are memoized per block (tuiBlock cache).
	top    int
	follow bool
}

// rowsFor renders the pane's blocks to width. Each block memoizes its own
// rows, so an unchanged pane repaints for free.
func (p *tuiDetail) rowsFor(width int) []tuiRow {
	var rows []tuiRow
	for i := range p.lines {
		rows = append(rows, p.lines[i].rows(width)...)
	}
	return rows
}

// openPane shows lines as a full-body view. follow starts it at the newest rows.
func (m *tuiModel) openPane(title string, lines []tuiBlock, follow bool) {
	m.pane = &tuiDetail{title: title, lines: lines, follow: follow}
}

// paneRows draws the open pane: a title row, then as many wrapped rows as fit.
// It is pure: the offset is settled here because only the render knows the
// wrapped length, but it is never written back — the key handler moves top
// freely and each render clamps it into view.
func (m *tuiModel) paneRows(cols, rows int) []tuiRow {
	p := m.pane
	all := p.rowsFor(cols)
	page := max(rows-1, 0)
	maxTop := max(len(all)-page, 0)
	top := p.top
	if p.follow {
		top = maxTop
	}
	top = min(max(top, 0), maxTop)
	end := min(top+page, len(all))
	out := append([]tuiRow{tuiText(p.title, tuiInk{fg: tuiColAccent, bold: true})}, all[top:end]...)
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
	case keyLeft:
		m.cycleView(-1)
	case keyRight:
		m.cycleView(1)
	case keyUp:
		p.follow, p.top = false, p.top-1
	case keyDown:
		p.follow, p.top = false, p.top+1
	case keyPgUp:
		p.follow, p.top = false, p.top-page
	case keyPgDn:
		p.follow, p.top = false, p.top+page
	case keyWheelUp:
		p.follow, p.top = false, p.top-tuiWheelRows
	case keyWheelDown:
		p.follow, p.top = false, p.top+tuiWheelRows
	case keyRune:
		if k.Rune == 'q' {
			m.pane = nil
		}
	}
}

// mailboxLines renders a thread's peer mailbox, oldest first.
func mailboxLines(box []tuiMailboxRow) []tuiBlock {
	if len(box) == 0 {
		return []tuiBlock{tuiNoteBlock(tuiKindSystem, "no peer messages")}
	}
	var lines []tuiBlock
	for _, msg := range box {
		head := fmt.Sprintf("%s %s → %s", msg.At.Local().Format("15:04:05"), msg.From, msg.To)
		if msg.Kind != "" {
			head += " (" + msg.Kind + ")"
		}
		lines = append(lines, tuiNoteBlock(tuiKindStatus, head),
			tuiNoteBlock(string(contracts.LogText), msg.Body))
	}
	return lines
}

// taskLines renders a thread's task list in plan order.
func taskLines(tasks []tuiTaskRow) []tuiBlock {
	if len(tasks) == 0 {
		return []tuiBlock{tuiNoteBlock(tuiKindSystem, "no tasks planned")}
	}
	lines := make([]tuiBlock, 0, len(tasks))
	for _, task := range tasks {
		title := task.Title
		if title == "" {
			title = task.Name
		}
		lines = append(lines, tuiNoteBlock(tuiKindStatus, fmt.Sprintf("[%s] %s", task.Status, title)))
	}
	return lines
}
