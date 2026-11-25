package cli

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The Files tab: the focused thread's worktree as a tree with git status marks,
// a fuzzy name search, and a read-only preview of a file. Everything reads through
// the daemon, so a client never reaches the disk for a path it was not sent.

const tuiViewFiles = "files"

// tuiFileEntry mirrors files.Entry.
type tuiFileEntry struct {
	Name     string         `json:"name"`
	Path     string         `json:"path"`
	Dir      bool           `json:"dir"`
	Size     int64          `json:"size"`
	Children []tuiFileEntry `json:"children"`
}

// tuiFileMatch mirrors files.Match.
type tuiFileMatch struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

// tuiFileRead mirrors files.ReadResult.
type tuiFileRead struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Text      string `json:"text"`
}

// tuiFileVisible is one tree row: an entry and how deep it sits.
type tuiFileVisible struct {
	entry tuiFileEntry
	depth int
}

type tuiFilesState struct {
	tuiBase
	threadID string
	root     []tuiFileEntry
	marks    map[string]string
	expanded map[string]bool

	// search is the match list when a query has run; nil means the tree.
	search   []tuiFileMatch
	query    string
	searched bool
	// preview is the open file, or nil. scroll is its line offset.
	preview *tuiFileRead
	scroll  int
	// gen numbers the preview and search requests: each one, and each way of
	// closing what they open, moves it on, and a result lands only if its
	// request is still the latest. A reply that arrives after Esc, or after a
	// newer search, is dropped instead of reopening what the user closed.
	gen int
}

func tuiFilesOf(m *tuiModel) *tuiFilesState {
	return tuiStateOf[tuiFilesState](m, tuiViewFiles)
}

// fetchJob reads the focused thread's tree and git status in the background.
// The fold drops the result if the focus moved to another thread meanwhile.
func (st *tuiFilesState) fetchJob() tuiJob {
	id := st.threadID
	base := &st.tuiBase
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		if id == "" {
			return func(m *tuiModel) {
				st := tuiFilesOf(m)
				if st.threadID != "" {
					return // the user focused a thread meanwhile
				}
				st.root, st.marks = nil, nil
			}
		}
		var tree struct {
			Entries []tuiFileEntry    `json:"entries"`
			Status  map[string]string `json:"status"`
		}
		if err := c.Call(ctx, "files.tree", map[string]string{"threadId": id}, &tree); err != nil {
			return tuiViewErrJob(base, err)
		}
		return func(m *tuiModel) {
			st := tuiFilesOf(m)
			if st.threadID != id {
				return // the user focused another thread meanwhile
			}
			st.root, st.marks = tree.Entries, tree.Status
		}
	}
}

// visible flattens the expanded tree into the rows the cursor walks.
func (st *tuiFilesState) visible() []tuiFileVisible {
	var out []tuiFileVisible
	var walk func(entries []tuiFileEntry, depth int)
	walk = func(entries []tuiFileEntry, depth int) {
		for _, e := range entries {
			out = append(out, tuiFileVisible{entry: e, depth: depth})
			if e.Dir && st.expanded[e.Path] {
				walk(e.Children, depth+1)
			}
		}
	}
	walk(st.root, 0)
	return out
}

// listLen is the number of rows the cursor walks: search matches, or the tree.
func (st *tuiFilesState) listLen() int {
	if st.search != nil {
		return len(st.search)
	}
	return len(st.visible())
}

// open loads a file for the preview pane in the background. Binary and oversized
// files come back flagged by the daemon, so the preview shows that instead of bytes.
func (st *tuiFilesState) open(m *tuiModel, path string) {
	id := st.threadID
	base := &st.tuiBase
	st.gen++
	gen := st.gen
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var read tuiFileRead
		if err := c.Call(ctx, "files.read", map[string]string{"threadId": id, "path": path}, &read); err != nil {
			return tuiViewErrJob(base, err)
		}
		return func(m *tuiModel) {
			st := tuiFilesOf(m)
			if st.threadID != id || st.gen != gen {
				return // another thread, or a newer request or a close, meanwhile
			}
			st.preview, st.scroll = &read, 0
		}
	})
}

// runSearch replaces the tree with the matches for query, in the background.
func (st *tuiFilesState) runSearch(m *tuiModel, query string) {
	id := st.threadID
	base := &st.tuiBase
	st.gen++
	gen := st.gen
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var reply struct {
			Matches []tuiFileMatch `json:"matches"`
		}
		if err := c.Call(ctx, "files.search", map[string]any{"threadId": id, "query": query, "limit": 200}, &reply); err != nil {
			return tuiViewErrJob(base, err)
		}
		matches := reply.Matches
		if matches == nil {
			matches = []tuiFileMatch{}
		}
		return func(m *tuiModel) {
			st := tuiFilesOf(m)
			if st.threadID != id || st.gen != gen {
				return // another thread, or a newer request or a close, meanwhile
			}
			st.query, st.search, st.searched, st.cursor = query, matches, true, 0
		}
	})
}

func (st *tuiFilesState) render(cols, rows int) []tuiRow {
	status := st.status()
	if st.ed.on {
		status = st.ed.row()
	}
	if st.threadID == "" {
		return tuiViewFrame(cols, rows, "Files", []tuiRow{tuiFaint("select a thread in the list to browse its files")}, -1, status)
	}
	if st.preview != nil {
		return st.renderPreview(cols, rows, status)
	}
	var lines []tuiRow
	if st.search != nil {
		lines = st.searchRows()
	} else {
		lines = st.treeRows()
	}
	title := "Files"
	if st.searched {
		title = fmt.Sprintf("Files · search %q", st.query)
	}
	return tuiViewFrame(cols, rows, title, lines, st.cursor, status)
}

func (st *tuiFilesState) treeRows() []tuiRow {
	vis := st.visible()
	if len(vis) == 0 {
		return []tuiRow{tuiFaint("no files in this worktree")}
	}
	out := make([]tuiRow, 0, len(vis))
	for _, v := range vis {
		marker := "  "
		if v.entry.Dir {
			marker = "▸ "
			if st.expanded[v.entry.Path] {
				marker = "▾ "
			}
		}
		mark := tuiText(" ", tuiInk{})
		if code, ok := st.marks[v.entry.Path]; ok {
			mark = tuiText(fileStatusGlyph(code), fileStatusInk(code))
		}
		name := tuiInk{fg: tuiColText}
		if v.entry.Dir {
			name = tuiInk{fg: tuiColAccent2}
		}
		out = append(out, mark.join(tuiText(" "+strings.Repeat("  ", v.depth)+marker, tuiInk{fg: tuiColDim}),
			tuiText(v.entry.Name, name)))
	}
	return out
}

func (st *tuiFilesState) searchRows() []tuiRow {
	if len(st.search) == 0 {
		return []tuiRow{tuiFaint("no matches for " + st.query)}
	}
	out := make([]tuiRow, 0, len(st.search))
	for _, m := range st.search {
		ink := tuiInk{fg: tuiColText}
		if m.Dir {
			ink = tuiInk{fg: tuiColAccent2}
		}
		out = append(out, tuiText("  "+m.Path, ink))
	}
	return out
}

// renderPreview shows the open file numbered, from the scroll offset down.
func (st *tuiFilesState) renderPreview(cols, rows int, status tuiRow) []tuiRow {
	p := st.preview
	var lines []tuiRow
	switch {
	case p.Binary:
		lines = []tuiRow{tuiFaint(fmt.Sprintf("binary file · %d bytes · not shown", p.Size))}
	default:
		for i, line := range strings.Split(p.Text, "\n") {
			lines = append(lines, tuiText(fmt.Sprintf("%5d  ", i+1), tuiInk{fg: tuiColFaint}).
				join(tuiText(line, tuiInk{fg: tuiColText})))
		}
		if p.Truncated {
			lines = append(lines, tuiFaint("… truncated"))
		}
	}
	off := min(max(st.scroll, 0), max(len(lines)-1, 0))
	return tuiViewFrame(cols, rows, "Files · "+p.Path, lines[off:], -1, status)
}

func fileStatusGlyph(code string) string {
	switch code {
	case "added":
		return "A"
	case "deleted":
		return "D"
	case "untracked":
		return "?"
	case "renamed":
		return "R"
	case "unmerged":
		return "U"
	}
	return "M"
}

func fileStatusInk(code string) tuiInk {
	switch code {
	case "added", "untracked":
		return tuiInk{fg: tuiColOK}
	case "deleted", "unmerged":
		return tuiInk{fg: tuiColErr}
	}
	return tuiInk{fg: tuiColWarn}
}

func (st *tuiFilesState) key(m *tuiModel, k tuiKey) bool {
	st.touch(k)
	if st.ed.on {
		if st.ed.key(k) {
			query := st.ed.value()
			st.ed.text = nil
			if query == "" {
				st.search, st.searched, st.query = nil, false, ""
				st.gen++
			} else {
				st.runSearch(m, query)
			}
		}
		return true
	}
	if st.preview != nil {
		if step := tuiNav(k, 20); step != 0 {
			st.scroll = max(st.scroll+step, 0)
			return true
		}
		if k.Kind == keyEsc || k.Kind == keyEnter || (k.Kind == keyRune && k.Rune == 'h') {
			st.preview = nil
			st.gen++
			return true
		}
		return false
	}
	if k.Kind == keyEsc && st.searched {
		st.search, st.searched, st.query, st.cursor = nil, false, "", 0
		st.gen++
		return true
	}
	if step := tuiNav(k, 10); step != 0 {
		st.cursor = tuiMoveCursor(st.cursor, step, st.listLen())
		return true
	}
	switch {
	case k.Kind == keyRune && k.Rune == '/':
		st.ed.open("search files", st.query)
	case k.Kind == keyEnter || (k.Kind == keyRune && k.Rune == 'l'):
		st.activate(m)
	case k.Kind == keyRune && k.Rune == 'h':
		st.collapse()
	case k.Kind == keyRune && k.Rune == 'r':
		tuiReloadJob(m, &st.tuiBase, st.fetchJob())
	default:
		return false
	}
	return true
}

// activate opens the row under the cursor: a folder toggles, a file previews.
func (st *tuiFilesState) activate(m *tuiModel) {
	if st.search != nil {
		if st.cursor < len(st.search) && !st.search[st.cursor].Dir {
			st.open(m, st.search[st.cursor].Path)
		}
		return
	}
	vis := st.visible()
	if st.cursor >= len(vis) {
		return
	}
	e := vis[st.cursor].entry
	if !e.Dir {
		st.open(m, e.Path)
		return
	}
	if st.expanded == nil {
		st.expanded = map[string]bool{}
	}
	st.expanded[e.Path] = !st.expanded[e.Path]
}

// collapse closes the folder under the cursor.
func (st *tuiFilesState) collapse() {
	vis := st.visible()
	if st.search != nil || st.cursor >= len(vis) {
		return
	}
	if e := vis[st.cursor].entry; e.Dir && st.expanded[e.Path] {
		st.expanded[e.Path] = false
	}
}

// focus points the view at the focused thread; a different thread reloads.
func (st *tuiFilesState) focus(m *tuiModel) {
	id := ""
	if row, ok := m.focusRow(); ok {
		id = row.thread.ID
	}
	if id == st.threadID {
		return
	}
	st.threadID = id
	st.root, st.marks, st.expanded = nil, nil, nil
	st.search, st.query, st.searched = nil, "", false
	st.preview, st.scroll, st.cursor = nil, 0, 0
	st.mu.Lock()
	st.gate.force = true
	st.mu.Unlock()
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewFiles,
		Title: "Files",
		Order: 20,
		Load: func(ctx context.Context, c cliCaller, m *tuiModel) {
			st := tuiFilesOf(m)
			st.bind(ctx, c)
			st.focus(m)
			// The tree is cheap to read but walks the worktree, so it refreshes on
			// open and on r rather than on every poll tick.
			tuiLoadJob(m, &st.tuiBase, time.Hour, st.fetchJob())
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiFilesOf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiFilesOf(m).key(m, k)
		},
		Hints: []tuiHint{
			{"↑/↓", "move"}, {"enter", "open"}, {"h", "collapse"}, {"/", "search"},
			{"r", "reload"}, {"←/→", "tabs"}, {"esc", "threads"},
		},
	})
}
