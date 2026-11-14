package cli

import (
	"context"
	"fmt"
	"time"
)

// The PR tab: the focused thread's pull or merge request, its checks and review
// state, and the git actions that move it forward. Every action reaches a forge
// CLI, so each one runs in the background; merge waits for a second press.

const tuiViewPR = "pr"

// tuiPRSnapshot mirrors scm.Snapshot.
type tuiPRSnapshot struct {
	Status   string        `json:"status"`
	Message  string        `json:"message"`
	Provider string        `json:"provider"`
	Branch   string        `json:"branch"`
	Request  *tuiPRRequest `json:"request"`
}

// tuiPRRequest mirrors scm.Request.
type tuiPRRequest struct {
	Number   int            `json:"number"`
	Title    string         `json:"title"`
	URL      string         `json:"url"`
	State    string         `json:"state"`
	Draft    bool           `json:"draft"`
	Review   string         `json:"review"`
	Checks   string         `json:"checks"`
	Comments []tuiPRComment `json:"comments"`
}

type tuiPRComment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
	Kind   string `json:"kind"`
}

// tuiPRListing mirrors scm.Listing, the rows pr.list returns.
type tuiPRListing struct {
	Status   string         `json:"status"`
	Message  string         `json:"message"`
	Requests []tuiPRSummary `json:"requests"`
}

type tuiPRSummary struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
}

type tuiPRState struct {
	tuiBase
	threadID string
	snap     tuiPRSnapshot
	listing  []tuiPRSummary
	edFor    string // what the open editor's text is for: commit or title
}

func tuiPROf(m *tuiModel) *tuiPRState {
	return tuiStateOf[tuiPRState](m, tuiViewPR)
}

// fetchJob reads the focused thread's request and the branch's open requests
// in the background: pr.status and pr.list shell out to git and the forge CLIs,
// so running them on the session loop would freeze keys, events, and paint.
// The fold applies on the loop, where the view's data is safe to write.
func (st *tuiPRState) fetchJob() tuiJob {
	id := st.threadID
	base := &st.tuiBase
	return func(ctx context.Context, c cliCaller) func(*tuiModel) {
		if id == "" {
			return func(m *tuiModel) {
				st := tuiPROf(m)
				st.snap, st.listing = tuiPRSnapshot{}, nil
			}
		}
		params := map[string]string{"threadId": id}
		var snap tuiPRSnapshot
		if err := c.Call(ctx, "pr.status", params, &snap); err != nil {
			return tuiViewErrJob(base, err)
		}
		var list tuiPRListing
		if err := c.Call(ctx, "pr.list", params, &list); err != nil {
			return tuiViewErrJob(base, err)
		}
		return func(m *tuiModel) {
			st := tuiPROf(m)
			st.snap, st.listing = snap, list.Requests
		}
	}
}

// action runs one forge action for the focused thread in the background.
func (st *tuiPRState) action(label, method string, params map[string]any, done string) {
	st.run(label, func(ctx context.Context, c cliCaller) (string, error) {
		err := c.Call(ctx, method, params, nil)
		return done, err
	})
}

func (st *tuiPRState) lines(width int) []tuiRow {
	var out []tuiRow
	s := st.snap
	if st.threadID == "" {
		return []tuiRow{tuiFaint("select a thread in the list to see its request")}
	}
	out = append(out, tuiKV("forge  ", usageDash(s.Provider)), tuiKV("branch ", usageDash(s.Branch)))
	switch s.Status {
	case "ready":
		r := s.Request
		if r == nil {
			break
		}
		out = append(out, nil, tuiText(fmt.Sprintf("#%d  ", r.Number), tuiInk{fg: tuiColAccent2, bold: true}).
			join(tuiText(r.Title, tuiInk{fg: tuiColText, bold: true})))
		state := tuiText("  "+r.State, tuiInk{fg: tuiColDim})
		if r.Draft {
			state = state.join(tuiText("  draft", tuiInk{fg: tuiColWarn}))
		}
		out = append(out, state)
		out = append(out, tuiKV("  checks ", usageDash(r.Checks)), tuiKV("  review ", usageDash(r.Review)))
		out = append(out, tuiFaint("  "+r.URL))
		if len(r.Comments) > 0 {
			out = append(out, nil, tuiHeading("Recent comments"))
			from := max(len(r.Comments)-3, 0)
			for _, cm := range r.Comments[from:] {
				out = append(out, tuiText("  "+cm.Author+"  ", tuiInk{fg: tuiColAccent2}).
					join(tuiText(firstLine(cm.Body), tuiInk{fg: tuiColText})))
			}
		}
	case "none":
		out = append(out, nil, tuiFaint("no request for this branch yet · press n to open one"))
	default:
		out = append(out, nil, tuiText(usageDash(s.Status), tuiInk{fg: tuiColWarn}))
		if s.Message != "" {
			out = append(out, wrapRows(s.Message, width, tuiInk{fg: tuiColDim})...)
		}
	}
	if len(st.listing) > 0 {
		out = append(out, nil, tuiHeading("Open on this repository"))
		for _, r := range st.listing {
			out = append(out, tuiText(fmt.Sprintf("  #%d ", r.Number), tuiInk{fg: tuiColDim}).
				join(tuiText(r.Title, tuiInk{fg: tuiColText}), tuiText("  "+r.State, tuiInk{fg: tuiColFaint})))
		}
	}
	return out
}

func (st *tuiPRState) render(cols, rows int) []tuiRow {
	width := cols
	if rows >= 3 && cols >= 6 {
		width = cols - 4
	}
	status := st.status()
	if st.ed.on {
		status = st.ed.row()
	}
	lines := st.lines(width)
	off := min(max(st.cursor, 0), max(len(lines)-1, 0))
	return tuiViewFrame(cols, rows, "Pull request", lines[off:], -1, status)
}

func (st *tuiPRState) key(m *tuiModel, k tuiKey) bool {
	st.touch(k)
	if st.ed.on {
		if st.ed.key(k) {
			text := st.ed.value()
			what := st.edFor
			st.ed.text = nil
			if text == "" {
				return true
			}
			switch what {
			case "commit":
				st.action("committing", "pr.commit", map[string]any{"threadId": st.threadID, "message": text, "all": true}, "committed")
			case "title":
				st.action("opening request", "pr.create", map[string]any{"threadId": st.threadID, "title": text}, "request opened")
			}
		}
		return true
	}
	if step := tuiNav(k, 8); step != 0 {
		st.cursor = max(st.cursor+step, 0)
		return true
	}
	if st.threadID == "" {
		return false
	}
	switch {
	case k.Kind == keyRune && k.Rune == 'c':
		st.edFor = "commit"
		st.ed.open("commit message", "")
	case k.Kind == keyRune && k.Rune == 'n':
		st.edFor = "title"
		st.ed.open("request title", "")
	case k.Kind == keyRune && k.Rune == 'p':
		st.action("pushing", "pr.push", map[string]any{"threadId": st.threadID}, "pushed")
	case k.Kind == keyRune && k.Rune == 'l':
		st.action("pulling", "pr.pull", map[string]any{"threadId": st.threadID}, "pulled")
	case k.Kind == keyRune && k.Rune == 'd':
		st.action("marking ready", "pr.ready", map[string]any{"threadId": st.threadID}, "marked ready")
	case k.Kind == keyRune && k.Rune == 'M':
		if st.snap.Request == nil {
			st.say("no request to merge")
			return true
		}
		if !st.confirm("merge", fmt.Sprintf("press M again to merge #%d into its base", st.snap.Request.Number), k) {
			return true
		}
		st.action("merging", "pr.merge", map[string]any{"threadId": st.threadID}, "merged")
	case k.Kind == keyRune && k.Rune == 'r':
		tuiReloadJob(m, &st.tuiBase, st.fetchJob())
	default:
		return false
	}
	return true
}

// focus points the view at the focused thread; a different thread reloads.
func (st *tuiPRState) focus(m *tuiModel) {
	id := ""
	if row, ok := m.focusRow(); ok {
		id = row.thread.ID
	}
	if id == st.threadID {
		return
	}
	st.threadID = id
	st.snap, st.listing, st.cursor = tuiPRSnapshot{}, nil, 0
	st.mu.Lock()
	st.gate.force = true
	st.mu.Unlock()
}

func init() {
	registerTuiView(tuiView{
		ID:    tuiViewPR,
		Title: "PR",
		Order: 40,
		Load: func(ctx context.Context, c cliCaller, m *tuiModel) {
			st := tuiPROf(m)
			st.bind(ctx, c)
			st.focus(m)
			// Forge CLIs cost a network round trip, so the fetch runs as a
			// background job: Load only queues it.
			tuiLoadJob(m, &st.tuiBase, 30*time.Second, st.fetchJob())
		},
		Render: func(m *tuiModel, cols, rows int) []tuiRow {
			return tuiPROf(m).render(cols, rows)
		},
		Key: func(m *tuiModel, k tuiKey) bool {
			return tuiPROf(m).key(m, k)
		},
		Hints: []tuiHint{
			{"c", "commit"}, {"p", "push"}, {"l", "pull"}, {"n", "open request"},
			{"d", "ready"}, {"M", "merge"}, {"r", "reload"}, {"tab", "views"}, {"esc", "threads"},
		},
	})
}
