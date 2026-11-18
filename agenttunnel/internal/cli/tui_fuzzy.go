package cli

import (
	"sort"
	"strings"
	"unicode"
)

// tuiFuzzy scores how well query matches text, for the palette and the thread
// list filter. Every query rune must appear in order (case-insensitive); the
// score favors an exact substring, then runs of adjacent runes and matches at
// the start of a word, so "nt" finds "new thread" and "thr" ranks a title
// containing "thread" above one that merely has those letters scattered. An
// empty query matches everything with score zero.
func tuiFuzzy(query, text string) (int, bool) {
	q := []rune(strings.ToLower(strings.TrimSpace(query)))
	if len(q) == 0 {
		return 0, true
	}
	t := []rune(strings.ToLower(text))
	score, qi, last := 0, 0, -2
	for i, r := range t {
		if qi == len(q) {
			break
		}
		if r != q[qi] {
			continue
		}
		score += 10
		if i == last+1 {
			score += 8
		}
		if i == 0 || !tuiWordRune(t[i-1]) {
			score += 12
		}
		last = i
		qi++
	}
	if qi < len(q) {
		return 0, false
	}
	if strings.Contains(string(t), string(q)) {
		score += 100
	}
	return score, true
}

func tuiWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// tuiFuzzyOrder returns the indexes of the texts query matches, best first;
// equal scores keep their original order.
func tuiFuzzyOrder(query string, texts []string) []int {
	type hit struct{ index, score int }
	var hits []hit
	for i, text := range texts {
		if score, ok := tuiFuzzy(query, text); ok {
			hits = append(hits, hit{i, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]int, len(hits))
	for i, h := range hits {
		out[i] = h.index
	}
	return out
}

// tuiListFilter is the thread list's "/" filter: the typed query, and whether
// keys are still going into it.
type tuiListFilter struct {
	query  []rune
	typing bool
}

// filtering reports whether keys are being typed into the list filter.
func (m *tuiModel) filtering() bool {
	return m.filter != nil && m.filter.typing
}

// listRows is the thread list as shown: every row, or with a filter the rows
// whose title, project, or id match it, in list order.
func (m *tuiModel) listRows() []psRow {
	if m.filter == nil || len(m.filter.query) == 0 {
		return m.rows
	}
	query := string(m.filter.query)
	var out []psRow
	for _, row := range m.rows {
		if _, ok := tuiFuzzy(query, row.thread.Title+" "+row.project+" "+row.thread.ID); ok {
			out = append(out, row)
		}
	}
	return out
}

// tuiListFilterKey runs the list filter's keys and reports whether it took the
// key. "/" starts typing; Enter keeps the filter and returns the keys to the
// list; Esc clears it. Arrows always move the list, filter or not.
func tuiListFilterKey(m *tuiModel, k tuiKey) bool {
	f := m.filter
	if f == nil || !f.typing {
		switch {
		case k.Kind == keyRune && k.Rune == '/':
			if f == nil {
				m.filter = &tuiListFilter{}
			}
			m.filter.typing = true
			return true
		case k.Kind == keyEsc && f != nil:
			m.filter = nil
			m.moveCursor(0)
			return true
		}
		return false
	}
	switch k.Kind {
	case keyEsc:
		m.filter = nil
	case keyEnter:
		f.typing = false
		if len(f.query) == 0 {
			m.filter = nil
		}
	case keyBackspace:
		if len(f.query) == 0 {
			m.filter = nil
		} else {
			f.query = f.query[:len(f.query)-1]
		}
	case keyRune:
		f.query = append(f.query, k.Rune)
		m.cursor = 0
	default:
		return false
	}
	m.moveCursor(0)
	return true
}
