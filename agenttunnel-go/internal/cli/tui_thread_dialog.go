package cli

import (
	"context"
	"strings"
)

// tuiJob is daemon work that runs off the session loop. It returns the fold
// that applies its outcome to the model, and the loop runs that fold. A job
// never reads the model: it captures the values it needs when it is queued.
// Returning nil folds nothing.
type tuiJob func(ctx context.Context, c cliCaller) func(*tuiModel)

// startJob queues a job for the session loop to launch.
func (m *tuiModel) startJob(job tuiJob) {
	m.jobs = append(m.jobs, job)
}

// takeJobs hands the queued jobs to the loop and clears the queue.
func (m *tuiModel) takeJobs() []tuiJob {
	jobs := m.jobs
	m.jobs = nil
	return jobs
}

// runTuiJob runs one job and sends its fold to the loop. A session that ends
// first drops the outcome instead of blocking.
func runTuiJob(ctx context.Context, c cliCaller, job tuiJob, out chan<- func(*tuiModel)) {
	apply := job(ctx, c)
	if apply == nil {
		return
	}
	select {
	case out <- apply:
	case <-ctx.Done():
	}
}

// tuiNote reports a job's outcome. On its thread the lines are logged there;
// anywhere else the first line becomes the list's flash.
func tuiNote(threadID string, lines ...string) func(*tuiModel) {
	return func(m *tuiModel) {
		if threadID != "" && m.onThread(threadID) {
			m.appendLogLines(lines...)
			return
		}
		if len(lines) > 0 {
			m.flash = lines[0]
		}
	}
}

// tuiPickItem is one picker row: the label shown and the value handed back.
type tuiPickItem struct {
	label string
	value string
}

// tuiDialog is a modal the thread menus open. With items it is a picker; without
// them it is a text prompt. Submit runs after the dialog closes, so a submit may
// open the next dialog in a chain.
type tuiDialog struct {
	title  string
	items  []tuiPickItem
	cursor int
	input  []rune
	submit func(m *tuiModel, value string)
}

// openPrompt shows a text prompt, prefilled with initial.
func (m *tuiModel) openPrompt(title, initial string, submit func(m *tuiModel, value string)) {
	m.dialog = &tuiDialog{title: title, input: []rune(initial), submit: submit}
}

// openPicker shows a list to choose from. An empty list is a flash, not a dialog.
func (m *tuiModel) openPicker(title string, items []tuiPickItem, submit func(m *tuiModel, value string)) {
	if len(items) == 0 {
		m.flash = "nothing to choose from"
		return
	}
	m.dialog = &tuiDialog{title: title, items: items, submit: submit}
}

// tuiDialogKey edits the open dialog: a prompt takes text, a picker takes the
// arrows. Enter submits the typed text or the highlighted value; Esc cancels.
func tuiDialogKey(m *tuiModel, k tuiKey) {
	d := m.dialog
	picker := d.items != nil
	switch k.Kind {
	case keyEsc:
		m.dialog = nil
	case keyEnter:
		m.dialog = nil
		value := strings.TrimSpace(string(d.input))
		if picker {
			value = d.items[d.cursor].value
		}
		d.submit(m, value)
	case keyUp:
		if picker {
			d.cursor = max(d.cursor-1, 0)
		}
	case keyDown:
		if picker {
			d.cursor = min(d.cursor+1, len(d.items)-1)
		}
	case keyBackspace:
		if !picker && len(d.input) > 0 {
			d.input = d.input[:len(d.input)-1]
		}
	case keyRune:
		if !picker {
			d.input = append(d.input, k.Rune)
		}
	}
}

// dialogBox draws the open dialog centered over a cols x rows body. It returns
// nil when the terminal is too small to hold one.
func (m *tuiModel) dialogBox(cols, rows int) ([]tuiRow, int) {
	d := m.dialog
	width := min(60, cols-4)
	if width < 20 || rows < 4 {
		return nil, 0
	}
	contentW := width - 4
	accent := tuiInk{fg: tuiColAccent, bold: true}
	var inner []tuiRow
	if d.items == nil {
		// The prompt keeps the typed tail visible: an ellipsis stands for the cut head.
		text := []rune(string(d.input))
		if avail := contentW - 2; len(text) > avail {
			text = append([]rune("…"), text[len(text)-(avail-1):]...)
		}
		inner = []tuiRow{tuiText("> ", accent).join(tuiText(string(text), tuiInk{fg: tuiColText})).fit(contentW)}
	} else {
		maxItems := rows - 3
		start := 0
		if d.cursor >= maxItems {
			start = d.cursor - maxItems + 1
		}
		for i := start; i < len(d.items) && len(inner) < maxItems; i++ {
			selected := i == d.cursor
			marker, ink := "  ", tuiInk{fg: tuiColDim}
			if selected {
				marker, ink = "› ", tuiInk{fg: tuiColText, bold: true}
			}
			row := tuiText(marker, accent).join(tuiText(d.items[i].label, ink)).fit(contentW).padTo(contentW, ink)
			if selected {
				row = row.withBG(tuiColSelect)
			}
			inner = append(inner, row)
		}
	}
	title := tuiText(d.title, accent)
	return tuiBox(width, len(inner)+2, title, nil, inner), width
}
