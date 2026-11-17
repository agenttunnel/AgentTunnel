package cli

import (
	"context"
	"sort"
	"strings"
)

// tuiThreadsViewID names the built-in thread list (and the thread view it
// opens). Registered views may not use it.
const tuiThreadsViewID = ""

// tuiView is an extra top-level tab registered from its own file, so settings,
// usage, diffs and the like plug in without touching the session loop. A view
// keeps its state in m.viewState under its ID.
type tuiView struct {
	// ID is the stable key the tab bar and the palette switch to.
	ID    string
	Title string
	// Order places the tab in the bar: lower comes first, after the thread
	// list, so the tab order is deliberate instead of file-init luck.
	Order int
	// Load refreshes the view's data over the daemon. The session calls it on
	// open and on every poll tick while the view is active. It runs on the
	// session loop, so it must stay cache-only: anything slow (a forge or
	// network call) queues a background job instead.
	Load func(ctx context.Context, c cliCaller, m *tuiModel)
	// Render paints the body: any number of rows, which render fits to the
	// body height.
	Render func(m *tuiModel, cols, rows int) []tuiRow
	// Key handles keys the global layer does not own. It reports whether it
	// consumed the key; Esc, Tab, ctrl+k and q fall through to the global layer
	// when it does not.
	Key func(m *tuiModel, k tuiKey) bool
	// Hints are the footer's key hints while the view is active.
	Hints []tuiHint
	// Actions are palette commands this view contributes.
	Actions []tuiAction
}

// tuiAction is one command-palette entry. Run changes the model; work that
// needs the daemon goes through the model's flags (refresh, quit), which the
// session loop acts on.
type tuiAction struct {
	Title string
	// Hint is the key that does the same thing directly, shown dim.
	Hint string
	// Show gates the entry on the current screen; nil means always.
	Show func(m *tuiModel) bool
	Run  func(m *tuiModel)
}

var tuiViewRegistry []tuiView

// registerTuiView adds a view to the tab bar. An empty or duplicate ID is a
// programming error and panics, the same rule Register follows.
func registerTuiView(v tuiView) {
	if v.ID == tuiThreadsViewID {
		panic("cli: tui view needs a non-empty ID other than the threads list")
	}
	if v.Render == nil {
		panic("cli: tui view " + v.ID + " has no Render")
	}
	for _, existing := range tuiViewRegistry {
		if existing.ID == v.ID {
			panic("cli: duplicate tui view " + v.ID)
		}
	}
	tuiViewRegistry = append(tuiViewRegistry, v)
}

// tuiViewByID finds a registered view.
func tuiViewByID(id string) (tuiView, bool) {
	for _, v := range tuiViewRegistry {
		if v.ID == id {
			return v, true
		}
	}
	return tuiView{}, false
}

// tuiViewOrder is the tab order: the thread list first, then registered views
// by their Order field, so the bar reads deliberately (Threads, Master, Files,
// Review, PR, Usage, Settings, Remote, …) instead of by file-init luck.
func tuiViewOrder() []string {
	views := make([]tuiView, len(tuiViewRegistry))
	copy(views, tuiViewRegistry)
	sort.SliceStable(views, func(i, j int) bool { return views[i].Order < views[j].Order })
	order := []string{tuiThreadsViewID}
	for _, v := range views {
		order = append(order, v.ID)
	}
	return order
}

// takeOpenID is the loop's deferred-open handoff: a background job asked to
// open a thread by id, usually a freshly created draft. A tab that became
// active while the job ran never hides the opener — the request switches
// back to the thread surface first.
func (m *tuiModel) takeOpenID() string {
	id := m.openID
	m.openID = ""
	if id != "" && m.view != tuiThreadsViewID {
		m.switchView(tuiThreadsViewID)
	}
	return id
}

// tabTitle is the label a tab shows for view id.
func tuiTabTitle(id string) string {
	if id == tuiThreadsViewID {
		return "Threads"
	}
	if v, ok := tuiViewByID(id); ok {
		return v.Title
	}
	return id
}

// switchView makes id the active tab. An open thread stays open underneath:
// its files, diff and request are tabs of the conversation, and Esc from any
// of them comes back to it with the transcript and the draft as they were.
func (m *tuiModel) switchView(id string) {
	if _, ok := tuiViewByID(id); !ok && id != tuiThreadsViewID {
		return
	}
	m.view = id
}

// cycleView moves to the next (delta 1) or previous (delta -1) tab.
func (m *tuiModel) cycleView(delta int) {
	order := tuiViewOrder()
	idx := 0
	for i, id := range order {
		if id == m.view {
			idx = i
		}
	}
	next := (idx + delta + len(order)) % len(order)
	m.switchView(order[next])
}

// tuiPalette is the open command palette: a typed filter over the actions and
// the highlighted match.
type tuiPalette struct {
	query  []rune
	cursor int
}

// openPalette shows the command palette over the current screen.
func (m *tuiModel) openPalette() {
	m.palette = &tuiPalette{}
}

// allActions is every palette command that applies to the current screen: the
// built-ins, a "show" entry per registered view, each view's own actions, and
// the thread commands.
func (m *tuiModel) allActions() []tuiAction {
	actions := []tuiAction{
		{Title: "Threads: show list", Run: func(m *tuiModel) {
			m.switchView(tuiThreadsViewID)
			if m.screen == tuiScreenThread {
				m.closeThread()
			}
		}},
		{Title: "Threads: refresh", Hint: "r", Run: func(m *tuiModel) { m.refresh = true }},
		{Title: "Quit", Hint: "q", Run: func(m *tuiModel) { m.quit = true }},
	}
	for _, id := range tuiViewOrder()[1:] {
		v, ok := tuiViewByID(id)
		if !ok {
			continue
		}
		actions = append(actions, tuiAction{
			Title: "Show " + v.Title,
			Run:   func(m *tuiModel) { m.switchView(id) },
		})
	}
	for _, id := range tuiViewOrder()[1:] {
		if v, ok := tuiViewByID(id); ok {
			actions = append(actions, v.Actions...)
		}
	}
	actions = append(actions, tuiPaletteActions()...)
	var shown []tuiAction
	for _, a := range actions {
		if a.Show == nil || a.Show(m) {
			shown = append(shown, a)
		}
	}
	return shown
}

// paletteMatches is the actions the typed query fuzzy-matches, best match
// first; an empty query lists every action in palette order.
func (m *tuiModel) paletteMatches() []tuiAction {
	if m.palette == nil {
		return nil
	}
	actions := m.allActions()
	titles := make([]string, len(actions))
	for i, a := range actions {
		titles[i] = a.Title + " " + a.Hint
	}
	order := tuiFuzzyOrder(string(m.palette.query), titles)
	out := make([]tuiAction, len(order))
	for i, index := range order {
		out[i] = actions[index]
	}
	return out
}

// tuiPaletteKey edits the palette: type to filter, arrows to move, Enter runs
// the highlighted action and closes the palette, Esc closes it without running.
func tuiPaletteKey(m *tuiModel, k tuiKey) {
	p := m.palette
	matches := m.paletteMatches()
	switch k.Kind {
	case keyEsc:
		m.palette = nil
	case keyEnter:
		if len(matches) == 0 {
			return
		}
		run := matches[min(p.cursor, len(matches)-1)]
		m.palette = nil
		run.Run(m)
	case keyUp:
		p.cursor = max(p.cursor-1, 0)
	case keyDown:
		p.cursor = min(p.cursor+1, max(len(matches)-1, 0))
	case keyBackspace:
		if len(p.query) > 0 {
			p.query = p.query[:len(p.query)-1]
		}
		p.cursor = 0
	case keyRune:
		p.query = append(p.query, k.Rune)
		p.cursor = 0
	}
}

// paletteBox draws the palette panel for a cols x rows body. It returns nil
// when the terminal is too narrow to hold one.
func (m *tuiModel) paletteBox(cols, rows int) ([]tuiRow, int) {
	width := min(60, cols-4)
	if width < 20 || rows < 4 {
		return nil, 0
	}
	contentW := width - 4
	matches := m.paletteMatches()
	maxItems := rows - 3 // box top and bottom, plus the query line
	p := m.palette
	p.cursor = min(p.cursor, max(len(matches)-1, 0))

	query := tuiText("> ", tuiInk{fg: tuiColAccent, bold: true}).join(tuiText(string(p.query), tuiInk{fg: tuiColText}))
	inner := []tuiRow{query}
	start := 0
	if p.cursor >= maxItems {
		start = p.cursor - maxItems + 1
	}
	for i := start; i < len(matches) && len(inner)-1 < maxItems; i++ {
		a := matches[i]
		selected := i == p.cursor
		marker, ink := "  ", tuiInk{fg: tuiColDim}
		if selected {
			marker, ink = "› ", tuiInk{fg: tuiColText, bold: true}
		}
		left := tuiText(marker, tuiInk{fg: tuiColAccent, bold: true}).join(tuiText(a.Title, ink))
		hint := tuiText(a.Hint, tuiInk{fg: tuiColFaint})
		gap := max(contentW-left.width()-hint.width(), 1)
		row := left.join(tuiText(strings.Repeat(" ", gap), ink), hint).fit(contentW).padTo(contentW, ink)
		if selected {
			row = row.selected()
		}
		inner = append(inner, row)
	}
	if len(matches) == 0 {
		inner = append(inner, tuiText("no matching commands", tuiInk{fg: tuiColFaint}))
	}
	title := tuiText("Commands", tuiInk{fg: tuiColAccent, bold: true})
	return tuiBox(width, len(inner)+2, title, nil, inner), width
}

// tuiPaintModal lays a box over body, centered, replacing only the columns it
// covers so the screen shows on either side of it.
func tuiPaintModal(body []tuiRow, cols int, box []tuiRow, width int) []tuiRow {
	if len(box) == 0 || len(box) > len(body) {
		return body
	}
	top := (len(body) - len(box)) / 2
	left := (cols - width) / 2
	for i, line := range box {
		row := body[top+i].padTo(cols, tuiInk{})
		before := row.cut(0, left).padTo(left, tuiInk{})
		after := row.cut(left+width, cols+width)
		body[top+i] = before.join(line, after)
	}
	return body
}
