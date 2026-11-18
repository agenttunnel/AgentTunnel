package cli

import (
	"context"
	"sort"
	"strings"
)

// "@" in the composer attaches a file or folder of the thread's worktree: the
// word after it searches the daemon's file list, a pick completes the path,
// and every completed path still in the text rides with the turn as an
// attachment, the way the desktop's chips do.

// tuiMentionLimit is how many matches a search asks for; tuiMentionRows how
// many the hint list shows at once. tuiMaxAttachments mirrors
// files.MaxAttachments on the daemon.
const (
	tuiMentionLimit   = 20
	tuiMentionRows    = 8
	tuiMaxAttachments = 20
)

// tuiMention is the open "@" search: the matches for one query, the
// highlighted one, the query a search is still out for, and a token the user
// dismissed with Esc, which stays closed until it changes.
type tuiMention struct {
	query   string
	loaded  bool
	matches []tuiFileMatch
	sel     int
	asked   string
	asking  bool
	closed  string
}

// mentionAt is the "@" word that ends at the cursor: where its "@" sits and
// the text after it. An "@" opens a mention only at the start of a word, and
// only while no whitespace separates it from the cursor.
func (t *tuiInput) mentionAt() (start int, query string, ok bool) {
	for i := t.cur - 1; i >= 0; i-- {
		switch r := t.text[i]; {
		case r == '@':
			if i > 0 && !tuiInputSpace(t.text[i-1]) {
				return 0, "", false
			}
			return i, string(t.text[i+1 : t.cur]), true
		case tuiInputSpace(r):
			return 0, "", false
		}
	}
	return 0, "", false
}

// replace swaps text[from:to] for s and leaves the cursor after it.
func (t *tuiInput) replace(from, to int, s string) {
	in := tuiInputRunes(s)
	text := append([]rune(nil), t.text[:from]...)
	text = append(text, in...)
	t.text = append(text, t.text[to:]...)
	t.cur = from + len(in)
	t.edited()
}

// syncMention keeps the search in step with the composer after a key: a new
// "@" word asks the daemon for its matches, and leaving the word closes the
// list. A question owns the prompt, so no mention opens under one.
func (m *tuiModel) syncMention() {
	_, query, ok := m.input.mentionAt()
	if !ok || m.pending != nil {
		m.mention = tuiMention{}
		return
	}
	if query != m.mention.closed {
		m.mention.closed = ""
	}
	if (m.mention.loaded && m.mention.query == query) || (m.mention.asking && m.mention.asked == query) {
		return
	}
	m.mention.asked, m.mention.asking = query, true
	threadID := m.thread.ID
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		var reply struct {
			Matches []tuiFileMatch `json:"matches"`
		}
		params := map[string]any{"threadId": threadID, "query": query, "limit": tuiMentionLimit}
		err := c.Call(ctx, "files.search", params, &reply)
		return func(m *tuiModel) {
			// Only the answer to the newest question, on the thread that
			// asked it, may fill the list.
			if !m.onThread(threadID) || !m.mention.asking || m.mention.asked != query {
				return
			}
			m.mention.asking = false
			if err != nil {
				return
			}
			m.mention.query, m.mention.loaded = query, true
			m.mention.matches, m.mention.sel = reply.Matches, 0
		}
	})
}

// mentionMatches is the list under the cursor's "@" word: the matches for
// exactly what is typed, unless the user closed it.
func (m *tuiModel) mentionMatches() []tuiFileMatch {
	_, query, ok := m.input.mentionAt()
	if !ok || m.pending != nil || !m.mention.loaded || m.mention.query != query || m.mention.closed == query {
		return nil
	}
	return m.mention.matches
}

// moveMention shifts the highlighted match by d, wrapping, and reports
// whether a list was showing to take the key.
func (m *tuiModel) moveMention(d int) bool {
	n := len(m.mentionMatches())
	if n == 0 {
		return false
	}
	m.mention.sel = ((m.mention.sel+d)%n + n) % n
	return true
}

// completeMention replaces the "@" word with the highlighted path and marks
// it attached. It reports whether a list was showing.
func (m *tuiModel) completeMention() bool {
	matches := m.mentionMatches()
	if len(matches) == 0 {
		return false
	}
	pick := matches[min(m.mention.sel, len(matches)-1)]
	start, _, _ := m.input.mentionAt()
	m.input.replace(start, m.input.cur, "@"+pick.Path+" ")
	if m.attached == nil {
		m.attached = map[string]map[string]bool{}
	}
	if m.attached[m.thread.ID] == nil {
		m.attached[m.thread.ID] = map[string]bool{}
	}
	m.attached[m.thread.ID][pick.Path] = true
	m.mention = tuiMention{}
	return true
}

// dismissMention closes the list for the word under the cursor, and reports
// whether one was showing.
func (m *tuiModel) dismissMention() bool {
	if len(m.mentionMatches()) == 0 {
		return false
	}
	_, m.mention.closed, _ = m.input.mentionAt()
	return true
}

// takeAttachments is the paths that ride with a sent line: the ones completed
// from the "@" list in this thread that the line still names. It forgets the
// thread's picks either way: they belonged to the draft that is now gone.
func (m *tuiModel) takeAttachments(line string) []string {
	picks := m.attached[m.thread.ID]
	delete(m.attached, m.thread.ID)
	if len(picks) == 0 {
		return nil
	}
	named := map[string]bool{}
	for _, word := range strings.Fields(line) {
		if path, ok := strings.CutPrefix(word, "@"); ok {
			named[path] = true
		}
	}
	var out []string
	for path := range picks {
		if named[path] {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	if len(out) > tuiMaxAttachments {
		out = out[:tuiMaxAttachments]
	}
	return out
}

// mentionRows is the hint list above the prompt: a window of the matches
// around the highlighted one, folders marked with a trailing slash.
func (m *tuiModel) mentionRows(width int) []tuiRow {
	matches := m.mentionMatches()
	if len(matches) == 0 {
		return nil
	}
	sel := min(m.mention.sel, len(matches)-1)
	top := tuiWindow(sel, len(matches), tuiMentionRows)
	var rows []tuiRow
	for i := top; i < len(matches) && i < top+tuiMentionRows; i++ {
		path := tuiSanitize(matches[i].Path)
		if matches[i].Dir {
			path += "/"
		}
		ink, mark := tuiInk{fg: tuiColText}, "  "
		if i == sel {
			ink, mark = tuiInk{fg: tuiColAccent, bold: true}, "▸ "
		}
		rows = append(rows, tuiText(mark, tuiInk{fg: tuiColAccent}).join(tuiText(path, ink)).fit(width))
	}
	return append(rows, tuiText("tab attach · esc dismiss", tuiInk{fg: tuiColFaint}).fit(width))
}
