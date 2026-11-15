package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Two tabs share this file because both are about projects: Projects lists the
// registered checkouts, and Issues shows the focused project's forge issues.
// Adding and deleting a project change the registry, so delete waits for a
// second press.

const (
	tuiViewProjects = "projects"
	tuiViewIssues   = "issues"
)

// tuiIssueWire mirrors scm.Issue.
type tuiIssueWire struct {
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	WebURL      string `json:"webUrl"`
}

// tuiIssueListWire mirrors scm.IssueList.
type tuiIssueListWire struct {
	Status   string         `json:"status"`
	Message  string         `json:"message"`
	Provider string         `json:"provider"`
	Issues   []tuiIssueWire `json:"issues"`
}

type tuiProjectsState struct {
	tuiBase
	rows []projectsRow
}

func tuiProjectsOf(m *tuiModel) *tuiProjectsState {
	return tuiStateOf[tuiProjectsState](m, tuiViewProjects)
}

func (st *tuiProjectsState) fetch() error {
	var rows []projectsRow
	if err := st.call("project.list", nil, &rows); err != nil {
		return err
	}
	st.rows = rows
	st.cursor = tuiMoveCursor(st.cursor, 0, len(rows))
	return nil
}

func (st *tuiProjectsState) render(cols, rows int) []tuiRow {
	status := st.status()
	if st.ed.on {
		status = st.ed.row()
	}
	if len(st.rows) == 0 {
		return tuiViewFrame(cols, rows, "Projects", []tuiRow{tuiFaint("no projects yet · press a to add a checkout")}, -1, status)
	}
	out := make([]tuiRow, 0, len(st.rows))
	for _, p := range st.rows {
		out = append(out, tuiText(p.Name, tuiInk{fg: tuiColText, bold: true}).
			join(tuiText(fmt.Sprintf("  %d threads", p.Threads), tuiInk{fg: tuiColDim}),
				tuiText("  "+p.Path, tuiInk{fg: tuiColFaint})))
	}
	return tuiViewFrame(cols, rows, "Projects", out, st.cursor, status)
}

func (st *tuiProjectsState) key(k tuiKey) bool {
	st.touch(k)
	if st.ed.on {
		if st.ed.key(k) {
			path := st.ed.value()
			st.ed.text = nil
			if path != "" {
				name := filepath.Base(path)
				st.run("adding project", func(ctx context.Context, c cliCaller) (string, error) {
					err := c.Call(ctx, "project.create", map[string]string{"name": name, "path": path}, nil)
					return "added " + name, err
				})
			}
		}
		return true
	}
	if step := tuiNav(k, 8); step != 0 {
		st.cursor = tuiMoveCursor(st.cursor, step, len(st.rows))
		return true
	}
	switch {
	case k.Kind == keyRune && k.Rune == 'a':
		st.ed.open("checkout path", "")
	case k.Kind == keyRune && k.Rune == 'x':
		if st.cursor >= len(st.rows) {
			return true
		}
		p := st.rows[st.cursor]
		if !st.confirm("delete", fmt.Sprintf("press x again to remove %s from the registry (its files stay)", p.Name), k) {
			return true
		}
		id := p.ID
		st.run("removing project", func(ctx context.Context, c cliCaller) (string, error) {
			err := c.Call(ctx, "project.delete", map[string]string{"id": id}, nil)
			return "removed " + p.Name, err
		})
	case k.Kind == keyRune && k.Rune == 'r':
		st.reload(st.fetch)
	default:
		return false
	}
	return true
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewProjects,
		Title: "Projects",
		Order: 80,
		Load: func(ctx context.Context, c cliCaller, m *tuiModel) {
			st := tuiProjectsOf(m)
			tuiLoadView(ctx, c, &st.tuiBase, 5*time.Second, st.fetch)
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiProjectsOf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiProjectsOf(m).key(k)
		},
		Hints: []tuiHint{
			{"↑/↓", "select"}, {"a", "add"}, {"x", "remove"}, {"r", "reload"}, {"tab", "views"}, {"esc", "threads"},
		},
	})
}

// tuiIssuesState is the Issues tab: the focused project's forge issues, with the
// selected issue's description open beside the list.
type tuiIssuesState struct {
	tuiBase
	projectID string
	project   string
	list      tuiIssueListWire
	detail    bool
}

func tuiIssuesOf(m *tuiModel) *tuiIssuesState {
	return tuiStateOf[tuiIssuesState](m, tuiViewIssues)
}

// fetchJob reads the focused project's issues in the background: the list
// shells out to a forge CLI, so it must never run on the session loop.
func (st *tuiIssuesState) fetchJob() tuiJob {
	id := st.projectID
	base := &st.tuiBase
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		if id == "" {
			return func(m *tuiModel) {
				st := tuiIssuesOf(m)
				if st.projectID != "" {
					return // the user focused another project meanwhile
				}
				st.list = tuiIssueListWire{}
			}
		}
		var list tuiIssueListWire
		if err := c.Call(ctx, "issues.list", map[string]string{"projectId": id}, &list); err != nil {
			return tuiViewErrJob(base, err)
		}
		return func(m *tuiModel) {
			st := tuiIssuesOf(m)
			if st.projectID != id {
				return // the user focused another project meanwhile
			}
			st.list = list
			st.cursor = tuiMoveCursor(st.cursor, 0, len(list.Issues))
		}
	}
}

func (st *tuiIssuesState) render(cols, rows int) []tuiRow {
	status := st.status()
	if st.projectID == "" {
		return tuiViewFrame(cols, rows, "Issues", []tuiRow{tuiFaint("select a thread in the list to see its project's issues")}, -1, status)
	}
	title := "Issues · " + st.project
	if st.list.Status != "" && st.list.Status != "ready" {
		msg := usageDash(st.list.Message)
		return tuiViewFrame(cols, rows, title, []tuiRow{tuiText(st.list.Status+": "+msg, tuiInk{fg: tuiColWarn})}, -1, status)
	}
	if len(st.list.Issues) == 0 {
		return tuiViewFrame(cols, rows, title, []tuiRow{tuiFaint("no open issues")}, -1, status)
	}
	width := cols
	if rows >= 3 && cols >= 6 {
		width = cols - 4
	}
	var lines []tuiRow
	for _, is := range st.list.Issues {
		lines = append(lines, tuiText(fmt.Sprintf("#%d  ", is.IID), tuiInk{fg: tuiColAccent2}).
			join(tuiText(is.Title, tuiInk{fg: tuiColText})))
	}
	if st.detail && st.cursor < len(st.list.Issues) {
		is := st.list.Issues[st.cursor]
		lines = append(lines, nil, tuiFaint(is.WebURL))
		body := strings.TrimSpace(is.Description)
		if body == "" {
			body = "no description"
		}
		lines = append(lines, wrapRows(body, width, tuiInk{fg: tuiColDim})...)
	}
	return tuiViewFrame(cols, rows, title, lines, min(st.cursor, len(lines)-1), status)
}

func (st *tuiIssuesState) key(m *tuiModel, k tuiKey) bool {
	st.touch(k)
	if step := tuiNav(k, 8); step != 0 {
		st.cursor = tuiMoveCursor(st.cursor, step, len(st.list.Issues))
		return true
	}
	switch {
	case k.Kind == keyEnter || (k.Kind == keyRune && k.Rune == 'd'):
		st.detail = !st.detail
	case k.Kind == keyRune && k.Rune == 'r':
		tuiReloadJob(m, &st.tuiBase, st.fetchJob())
	default:
		return false
	}
	return true
}

// focus points the view at the focused thread's project; another project reloads.
func (st *tuiIssuesState) focus(m *tuiModel) {
	projectID, name := "", ""
	if row, ok := m.focusRow(); ok {
		projectID, name = row.thread.ProjectID, row.project
	}
	if projectID == st.projectID {
		return
	}
	st.projectID, st.project, st.list, st.cursor, st.detail = projectID, name, tuiIssueListWire{}, 0, false
	st.mu.Lock()
	st.gate.force = true
	st.mu.Unlock()
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewIssues,
		Title: "Issues",
		Order: 90,
		Load: func(_ context.Context, _ cliCaller, m *tuiModel) {
			st := tuiIssuesOf(m)
			st.focus(m)
			// Forge issue lists cost a network round trip, so the fetch runs
			// as a background job on a minute's ttl: Load only queues it.
			tuiLoadJob(m, &st.tuiBase, time.Minute, st.fetchJob())
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiIssuesOf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiIssuesOf(m).key(m, k)
		},
		Hints: []tuiHint{
			{"↑/↓", "select"}, {"enter", "details"}, {"r", "reload"}, {"tab", "views"}, {"esc", "threads"},
		},
	})
}
