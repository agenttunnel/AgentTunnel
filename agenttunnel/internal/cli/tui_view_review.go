package cli

import (
	"context"
	"fmt"
	"time"
)

// The Review tab: the focused thread's working-tree diff against HEAD, its open
// review comments, and the two actions a reviewer takes. c comments on the diff
// line under the cursor; s submits the open comments to the team, behind a
// second press because a submit starts agent work.

const tuiViewReview = "review"

// tuiDiffWire mirrors gitdiff.Diff.
type tuiDiffWire struct {
	Files []tuiDiffFileWire `json:"files"`
	Empty bool              `json:"empty"`
}

type tuiDiffFileWire struct {
	Path      string        `json:"path"`
	Additions int           `json:"additions"`
	Deletions int           `json:"deletions"`
	Lines     []tuiDiffLine `json:"lines"`
}

type tuiDiffLine struct {
	Old  int    `json:"old"`
	New  int    `json:"new"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// tuiReviewThreadWire mirrors domain.ReviewThread, the fields the view shows.
type tuiReviewThreadWire struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Side   string `json:"side"`
	Body   string `json:"body"`
	Status string `json:"status"`
}

// tuiThreadReviewWire is the slice of thread.get the review reads.
type tuiThreadReviewWire struct {
	Exists        bool                  `json:"exists"`
	ReviewThreads []tuiReviewThreadWire `json:"reviewThreads"`
}

// tuiAnchor is the diff line a comment would attach to.
type tuiAnchor struct {
	file string
	line int
	side string
}

type tuiReviewState struct {
	tuiBase
	threadID string
	title    string
	rows     []tuiRow
	anchors  []*tuiAnchor // parallel to rows; nil where a row cannot take a comment
	open     int          // open comments on the thread
	pending  *tuiAnchor   // the anchor the open editor comments on
}

func tuiReviewOf(m *tuiModel) *tuiReviewState {
	return tuiStateOf[tuiReviewState](m, tuiViewReview)
}

// fetchJob reads the diff and the thread's review comments in the background, and
// lays the rows out there too so each paint only windows them. The fold drops the
// result if the focus moved to another thread meanwhile.
func (st *tuiReviewState) fetchJob() tuiJob {
	id := st.threadID
	base := &st.tuiBase
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		if id == "" {
			return func(m *tuiModel) {
				st := tuiReviewOf(m)
				if st.threadID != "" {
					return // the user focused a thread meanwhile
				}
				st.rows, st.anchors, st.open = nil, nil, 0
			}
		}
		var diff tuiDiffWire
		if err := c.Call(ctx, "review.diff", map[string]string{"threadId": id}, &diff); err != nil {
			return tuiViewErrJob(base, err)
		}
		var thread tuiThreadReviewWire
		if err := c.Call(ctx, "thread.get", map[string]string{"threadId": id}, &thread); err != nil {
			return tuiViewErrJob(base, err)
		}
		rows, anchors := tuiReviewRows(diff, thread.ReviewThreads)
		open := 0
		for _, t := range thread.ReviewThreads {
			if t.Status == "open" {
				open++
			}
		}
		return func(m *tuiModel) {
			st := tuiReviewOf(m)
			if st.threadID != id {
				return // the user focused another thread meanwhile
			}
			st.rows, st.anchors, st.open = rows, anchors, open
			st.cursor = tuiMoveCursor(st.cursor, 0, len(st.rows))
		}
	}
}

// tuiReviewRows builds the comment list and the diff as one row slice. Each diff
// line records its anchor so c knows which file, line and side it targets.
func tuiReviewRows(diff tuiDiffWire, threads []tuiReviewThreadWire) ([]tuiRow, []*tuiAnchor) {
	var rows []tuiRow
	var anchors []*tuiAnchor
	push := func(r tuiRow, a *tuiAnchor) {
		rows = append(rows, r)
		anchors = append(anchors, a)
	}

	push(tuiHeading("Comments"), nil)
	if len(threads) == 0 {
		push(tuiFaint("  none yet: put the cursor on a diff line and press c"), nil)
	}
	for _, t := range threads {
		ink := tuiInk{fg: tuiColWarn}
		if t.Status != "open" {
			ink = tuiInk{fg: tuiColFaint}
		}
		where := fmt.Sprintf("%s:%d", t.File, t.Line)
		push(tuiText("  ● "+t.Status+"  ", ink).join(tuiText(where+"  ", tuiInk{fg: tuiColAccent2}),
			tuiText(firstLine(t.Body), tuiInk{fg: tuiColText})), nil)
	}
	push(nil, nil)

	if diff.Empty || len(diff.Files) == 0 {
		push(tuiFaint("no changes against HEAD"), nil)
		return rows, anchors
	}
	for _, f := range diff.Files {
		push(tuiText(f.Path, tuiInk{fg: tuiColText, bold: true}).
			join(tuiText(fmt.Sprintf("  +%d -%d", f.Additions, f.Deletions), tuiInk{fg: tuiColDim})), nil)
		for _, l := range f.Lines {
			row, anchor := tuiDiffRow(f.Path, l)
			push(row, anchor)
		}
		push(nil, nil)
	}
	return rows, anchors
}

// tuiDiffRow styles one diff line: additions green, deletions red, hunks in the
// accent, and the old and new line numbers in the gutter.
func tuiDiffRow(path string, l tuiDiffLine) (tuiRow, *tuiAnchor) {
	gutter := fmt.Sprintf("%5s %5s ", tuiLineNo(l.Old), tuiLineNo(l.New))
	switch l.Kind {
	case "hunk", "meta":
		return tuiText(gutter+l.Text, tuiInk{fg: tuiColAccent2}), nil
	case "add":
		return tuiText(gutter, tuiInk{fg: tuiColFaint}).
				join(tuiText("+"+l.Text, tuiInk{fg: tuiColOK})),
			&tuiAnchor{file: path, line: l.New, side: "new"}
	case "delete":
		return tuiText(gutter, tuiInk{fg: tuiColFaint}).
				join(tuiText("-"+l.Text, tuiInk{fg: tuiColErr})),
			&tuiAnchor{file: path, line: l.Old, side: "old"}
	}
	return tuiText(gutter, tuiInk{fg: tuiColFaint}).
			join(tuiText(" "+l.Text, tuiInk{fg: tuiColText})),
		&tuiAnchor{file: path, line: l.New, side: "new"}
}

func tuiLineNo(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

// firstLine is the first line of a comment body, for the one-line list.
func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

func (st *tuiReviewState) render(cols, rows int) []tuiRow {
	status := st.status()
	if st.ed.on {
		status = st.ed.row()
	}
	if st.threadID == "" {
		return tuiViewFrame(cols, rows, "Review", []tuiRow{tuiFaint("select a thread in the list to review its changes")}, -1, status)
	}
	return tuiViewFrame(cols, rows, "Review · "+st.title, st.rows, st.cursor, status)
}

// comment opens the editor on the diff line under the cursor.
func (st *tuiReviewState) comment() {
	if st.cursor < 0 || st.cursor >= len(st.anchors) || st.anchors[st.cursor] == nil {
		st.say("put the cursor on a diff line to comment on it")
		return
	}
	a := *st.anchors[st.cursor]
	st.pending = &a
	st.ed.open(fmt.Sprintf("comment %s:%d", a.file, a.line), "")
}

func (st *tuiReviewState) key(m *tuiModel, k tuiKey) bool {
	st.touch(k)
	if st.ed.on {
		if st.ed.key(k) {
			body := st.ed.value()
			anchor := st.pending
			st.ed.text, st.pending = nil, nil
			if body != "" && anchor != nil {
				threadID := st.threadID
				st.run("commenting", func(ctx context.Context, c cliCaller) (string, error) {
					err := c.Call(ctx, "review.comment", map[string]any{
						"threadId": threadID, "file": anchor.file, "line": anchor.line,
						"endLine": anchor.line, "side": anchor.side, "body": body,
					}, nil)
					return "comment added on " + anchor.file, err
				})
			}
		}
		return true
	}
	if step := tuiNav(k, 10); step != 0 {
		st.cursor = tuiMoveCursor(st.cursor, step, len(st.rows))
		return true
	}
	switch {
	case k.Kind == keyRune && k.Rune == 'c':
		st.comment()
	case k.Kind == keyRune && k.Rune == 's':
		if st.threadID == "" {
			return true
		}
		if !st.confirm("submit", fmt.Sprintf("press s again to send %d open comments to the team", st.open), k) {
			return true
		}
		threadID := st.threadID
		st.run("submitting review", func(ctx context.Context, c cliCaller) (string, error) {
			err := c.Call(ctx, "review.submit", map[string]string{"threadId": threadID}, nil)
			return "review submitted", err
		})
	case k.Kind == keyRune && k.Rune == 'r':
		tuiReloadJob(m, &st.tuiBase, st.fetchJob())
	default:
		return false
	}
	return true
}

// bind points the view at the focused thread. Moving the list cursor to another
// thread reloads the review for it.
func (st *tuiReviewState) focus(m *tuiModel) {
	id, title := "", ""
	if row, ok := m.focusRow(); ok {
		id, title = row.thread.ID, row.thread.Title
	}
	if id != st.threadID {
		st.threadID, st.title = id, title
		st.rows, st.anchors, st.open, st.cursor = nil, nil, 0, 0
		st.mu.Lock()
		st.gate.force = true
		st.mu.Unlock()
	}
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewReview,
		Title: "Review",
		Order: 30,
		Load: func(ctx context.Context, c cliCaller, m *tuiModel) {
			st := tuiReviewOf(m)
			st.bind(ctx, c)
			st.focus(m)
			tuiLoadJob(m, &st.tuiBase, 5*time.Second, st.fetchJob())
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiReviewOf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiReviewOf(m).key(m, k)
		},
		Hints: []tuiHint{
			{"↑/↓", "move"}, {"c", "comment"}, {"s", "submit"}, {"r", "reload"}, {"←/→", "tabs"}, {"esc", "threads"},
		},
	})
}
