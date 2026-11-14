package cli

import (
	"context"
	"fmt"
	"time"
)

// The Worktrees tab: the focused project's per-thread checkouts, with each one's
// branch, divergence from its base, and dirty state. Removing and pruning change
// files on disk, so both wait for a second press.

const tuiViewWorktrees = "worktrees"

// tuiWorktreeWire mirrors transport.WorktreeView.
type tuiWorktreeWire struct {
	Path      string   `json:"path"`
	Branch    string   `json:"branch"`
	Base      string   `json:"base"`
	Exists    bool     `json:"exists"`
	Prunable  bool     `json:"prunable"`
	Dirty     bool     `json:"dirty"`
	Ahead     int      `json:"ahead"`
	Behind    int      `json:"behind"`
	ThreadIDs []string `json:"threadIds"`
}

type tuiWorktreesState struct {
	tuiBase
	projectID string
	project   string
	rows      []tuiWorktreeWire
}

func tuiWorktreesOf(m *tuiModel) *tuiWorktreesState {
	return tuiStateOf[tuiWorktreesState](m, tuiViewWorktrees)
}

func (st *tuiWorktreesState) fetch() error {
	if st.projectID == "" {
		st.rows = nil
		return nil
	}
	var rows []tuiWorktreeWire
	if err := st.call("worktree.list", map[string]string{"projectId": st.projectID}, &rows); err != nil {
		return err
	}
	st.rows = rows
	st.cursor = tuiMoveCursor(st.cursor, 0, len(rows))
	return nil
}

func (st *tuiWorktreesState) selected() (tuiWorktreeWire, bool) {
	if st.cursor < 0 || st.cursor >= len(st.rows) {
		return tuiWorktreeWire{}, false
	}
	return st.rows[st.cursor], true
}

func (st *tuiWorktreesState) render(cols, rows int) []tuiRow {
	status := st.status()
	if st.ed.on {
		status = st.ed.row()
	}
	if st.projectID == "" {
		return tuiViewFrame(cols, rows, "Worktrees", []tuiRow{tuiFaint("select a thread in the list to see its project's worktrees")}, -1, status)
	}
	if len(st.rows) == 0 {
		return tuiViewFrame(cols, rows, "Worktrees · "+st.project, []tuiRow{tuiFaint("no worktrees for this project yet")}, -1, status)
	}
	out := make([]tuiRow, 0, len(st.rows))
	for _, w := range st.rows {
		out = append(out, worktreeRow(w))
	}
	return tuiViewFrame(cols, rows, "Worktrees · "+st.project, out, st.cursor, status)
}

// worktreeRow is one checkout: its state mark, branch, divergence, and threads.
func worktreeRow(w tuiWorktreeWire) tuiRow {
	mark := tuiText("● ", tuiInk{fg: tuiColOK})
	switch {
	case w.Prunable || !w.Exists:
		mark = tuiText("● ", tuiInk{fg: tuiColErr})
	case w.Dirty:
		mark = tuiText("● ", tuiInk{fg: tuiColWarn})
	}
	row := mark.join(tuiText(usageDash(w.Branch), tuiInk{fg: tuiColText, bold: true}))
	row = row.join(tuiText("  base "+usageDash(w.Base), tuiInk{fg: tuiColDim}))
	row = row.join(tuiText(fmt.Sprintf("  ↑%d ↓%d", w.Ahead, w.Behind), tuiInk{fg: tuiColDim}))
	row = row.join(tuiText(fmt.Sprintf("  %d threads", len(w.ThreadIDs)), tuiInk{fg: tuiColFaint}))
	if w.Dirty {
		row = row.join(tuiText("  dirty", tuiInk{fg: tuiColWarn}))
	}
	if w.Prunable || !w.Exists {
		row = row.join(tuiText("  missing", tuiInk{fg: tuiColErr}))
	}
	return row.join(tuiText("  "+w.Path, tuiInk{fg: tuiColFaint}))
}

func (st *tuiWorktreesState) key(k tuiKey) bool {
	st.touch(k)
	if st.ed.on {
		return true
	}
	if step := tuiNav(k, 8); step != 0 {
		st.cursor = tuiMoveCursor(st.cursor, step, len(st.rows))
		return true
	}
	switch {
	case k.Kind == keyRune && k.Rune == 'x':
		w, ok := st.selected()
		if !ok {
			return true
		}
		if !st.confirm("remove", fmt.Sprintf("press x again to remove %s (uncommitted work there is refused)", w.Path), k) {
			return true
		}
		projectID, path := st.projectID, w.Path
		st.run("removing worktree", func(ctx context.Context, c cliCaller) (string, error) {
			err := c.Call(ctx, "worktree.remove", map[string]any{"projectId": projectID, "path": path, "force": false}, nil)
			return "removed " + path, err
		})
	case k.Kind == keyRune && k.Rune == 'P':
		if !st.confirm("prune", "press P again to prune worktrees whose folder is gone", k) {
			return true
		}
		projectID := st.projectID
		st.run("pruning", func(ctx context.Context, c cliCaller) (string, error) {
			err := c.Call(ctx, "worktree.prune", map[string]string{"projectId": projectID}, nil)
			return "pruned missing worktrees", err
		})
	case k.Kind == keyRune && k.Rune == 'r':
		st.reload(st.fetch)
	default:
		return false
	}
	return true
}

// focus points the view at the focused thread's project; another project reloads.
func (st *tuiWorktreesState) focus(m *tuiModel) {
	projectID, name := "", ""
	if row, ok := m.focusRow(); ok {
		projectID, name = row.thread.ProjectID, row.project
	}
	if projectID == st.projectID {
		return
	}
	st.projectID, st.project, st.rows, st.cursor = projectID, name, nil, 0
	st.mu.Lock()
	st.gate.force = true
	st.mu.Unlock()
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewWorktrees,
		Title: "Worktrees",
		Load: func(ctx context.Context, c cliCaller, m *tuiModel) {
			st := tuiWorktreesOf(m)
			st.focus(m)
			tuiLoadView(ctx, c, &st.tuiBase, 5*time.Second, st.fetch)
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiWorktreesOf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiWorktreesOf(m).key(k)
		},
		Hints: []tuiHint{
			{"↑/↓", "select"}, {"x", "remove"}, {"P", "prune"}, {"r", "reload"}, {"tab", "views"}, {"esc", "threads"},
		},
	})
}
