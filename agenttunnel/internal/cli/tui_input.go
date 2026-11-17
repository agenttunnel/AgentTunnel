package cli

import (
	"slices"
	"strings"
)

// tuiInputHistory bounds how many sent prompts are kept for recall.
const tuiInputHistory = 100

// tuiInput is the composer's editor: the typed runes (newlines included), a
// cursor, and the prompts already sent, for recall. The zero value is an
// empty editor on the live draft.
type tuiInput struct {
	text  []rune
	cur   int      // cursor as an index into text, 0..len(text)
	hist  []string // sent prompts, oldest first, at most tuiInputHistory
	hidx  int      // which hist entry is shown; len(hist) means the live draft
	draft []rune   // the live draft, kept while history is browsed
}

// tuiInputNewlines folds every line ending into '\n'. The CRLF pair comes first
// so it wins over a lone CR.
var tuiInputNewlines = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// tuiInputRunes normalizes text for the editor: line endings become '\n', tabs
// become a space, and other control runes are dropped.
func tuiInputRunes(s string) []rune {
	s = tuiInputNewlines.Replace(s)
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\n':
		case r == '\t':
			r = ' '
		case r < 0x20, r >= 0x7f && r <= 0x9f:
			continue
		}
		out = append(out, r)
	}
	return out
}

// tuiInputSpace is what separates words: a space or a line break.
func tuiInputSpace(r rune) bool { return r == ' ' || r == '\n' }

// tuiInputCells is the width of rs in terminal cells.
func tuiInputCells(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += runeCells(r)
	}
	return n
}

// edited marks a change to the text. A recalled prompt that is typed into
// becomes the live draft; the sent history itself is never touched.
func (t *tuiInput) edited() { t.hidx = len(t.hist) }

func (t *tuiInput) insert(r rune) {
	t.text = slices.Insert(t.text, t.cur, r)
	t.cur++
	t.edited()
}

func (t *tuiInput) insertString(s string) {
	in := tuiInputRunes(s)
	if len(in) == 0 {
		return
	}
	t.text = slices.Insert(t.text, t.cur, in...)
	t.cur += len(in)
	t.edited()
}

func (t *tuiInput) backspace() {
	if t.cur == 0 {
		return
	}
	t.text = slices.Delete(t.text, t.cur-1, t.cur)
	t.cur--
	t.edited()
}

func (t *tuiInput) del() {
	if t.cur == len(t.text) {
		return
	}
	t.text = slices.Delete(t.text, t.cur, t.cur+1)
	t.edited()
}

func (t *tuiInput) left()  { t.cur = max(t.cur-1, 0) }
func (t *tuiInput) right() { t.cur = min(t.cur+1, len(t.text)) }

// prevWord is the word boundary before i: spaces are skipped, then the word.
func (t *tuiInput) prevWord(i int) int {
	for i > 0 && tuiInputSpace(t.text[i-1]) {
		i--
	}
	for i > 0 && !tuiInputSpace(t.text[i-1]) {
		i--
	}
	return i
}

// nextWord is the word boundary after i: spaces are skipped, then the word.
func (t *tuiInput) nextWord(i int) int {
	for i < len(t.text) && tuiInputSpace(t.text[i]) {
		i++
	}
	for i < len(t.text) && !tuiInputSpace(t.text[i]) {
		i++
	}
	return i
}

func (t *tuiInput) wordLeft()  { t.cur = t.prevWord(t.cur) }
func (t *tuiInput) wordRight() { t.cur = t.nextWord(t.cur) }

// deleteWordBack is ctrl+w: it removes from the previous word boundary to the cursor.
func (t *tuiInput) deleteWordBack() {
	from := t.prevWord(t.cur)
	if from == t.cur {
		return
	}
	t.text = slices.Delete(t.text, from, t.cur)
	t.cur = from
	t.edited()
}

// lineStart is where the logical line holding i begins.
func (t *tuiInput) lineStart(i int) int {
	for i > 0 && t.text[i-1] != '\n' {
		i--
	}
	return i
}

// lineEnd is the index of the '\n' that ends the logical line holding i, or
// len(text) on the last line.
func (t *tuiInput) lineEnd(i int) int {
	for i < len(t.text) && t.text[i] != '\n' {
		i++
	}
	return i
}

func (t *tuiInput) home() { t.cur = t.lineStart(t.cur) }
func (t *tuiInput) end()  { t.cur = t.lineEnd(t.cur) }

// killToStart is ctrl+u: it removes from the start of the logical line to the cursor.
func (t *tuiInput) killToStart() {
	from := t.lineStart(t.cur)
	if from == t.cur {
		return
	}
	t.text = slices.Delete(t.text, from, t.cur)
	t.cur = from
	t.edited()
}

// up moves to the previous logical line, keeping the rune column when the line
// is long enough. It reports false, and does not move, on the first line.
func (t *tuiInput) up() bool {
	start := t.lineStart(t.cur)
	if start == 0 {
		return false
	}
	prevEnd := start - 1
	prevStart := t.lineStart(prevEnd)
	t.cur = prevStart + min(t.cur-start, prevEnd-prevStart)
	return true
}

// down is up for the next logical line; it reports false on the last line.
func (t *tuiInput) down() bool {
	end := t.lineEnd(t.cur)
	if end == len(t.text) {
		return false
	}
	col := t.cur - t.lineStart(t.cur)
	next := end + 1
	t.cur = next + min(col, t.lineEnd(next)-next)
	return true
}

// historyPrev shows the next older sent prompt. Leaving the live draft saves it,
// so historyNext can restore it.
func (t *tuiInput) historyPrev() bool {
	if t.hidx == 0 {
		return false
	}
	if t.hidx == len(t.hist) {
		t.draft = slices.Clone(t.text)
	}
	t.hidx--
	t.text = []rune(t.hist[t.hidx])
	t.cur = len(t.text)
	return true
}

// historyNext shows the next newer prompt; stepping past the newest restores
// the saved draft.
func (t *tuiInput) historyNext() bool {
	if t.hidx >= len(t.hist) {
		return false
	}
	t.hidx++
	if t.hidx == len(t.hist) {
		t.text = slices.Clone(t.draft)
	} else {
		t.text = []rune(t.hist[t.hidx])
	}
	t.cur = len(t.text)
	return true
}

// set replaces the text with s and puts the cursor at its end.
func (t *tuiInput) set(s string) {
	t.text = tuiInputRunes(s)
	t.cur = len(t.text)
	t.hidx = len(t.hist)
}

// string is the text as typed, untrimmed.
func (t *tuiInput) string() string { return string(t.text) }

func (t *tuiInput) empty() bool { return len(t.text) == 0 }

// take returns the trimmed text for sending, records it in history unless it
// repeats the newest entry, and clears the editor.
func (t *tuiInput) take() string {
	s := strings.TrimSpace(string(t.text))
	if s != "" && (len(t.hist) == 0 || t.hist[len(t.hist)-1] != s) {
		t.hist = append(t.hist, s)
		if over := len(t.hist) - tuiInputHistory; over > 0 {
			t.hist = t.hist[over:]
		}
	}
	t.clear()
	return s
}

// clear empties the text and keeps the history.
func (t *tuiInput) clear() {
	t.text = nil
	t.cur = 0
	t.draft = nil
	t.hidx = len(t.hist)
}

// tuiInputRow is one display row: text[from:to].
type tuiInputRow struct{ from, to int }

// view wraps the text to width cells and returns at most maxRows rows around
// the cursor, with the cursor's row and cell column within that window.
func (t *tuiInput) view(width, maxRows int) (lines []string, row, col int) {
	width = max(width, 1)
	maxRows = max(maxRows, 1)

	var rows []tuiInputRow
	start := 0
	for {
		end := t.lineEnd(start)
		from, cells := start, 0
		for i := start; i < end; i++ {
			w := runeCells(t.text[i])
			if cells > 0 && cells+w > width {
				rows = append(rows, tuiInputRow{from, i})
				from, cells = i, 0
			}
			cells += w
		}
		rows = append(rows, tuiInputRow{from, end})
		if end == len(t.text) {
			break
		}
		start = end + 1
	}

	// The cursor belongs to the last row that starts at or before it, so a
	// cursor at a wrap point sits at the start of the following row.
	cr := 0
	for i, rw := range rows {
		if rw.from <= t.cur {
			cr = i
		}
	}
	rw := rows[cr]
	col = tuiInputCells(t.text[rw.from:t.cur])
	if t.cur == rw.to && col >= width {
		// The cursor is past a full row at the end of a logical line: give it
		// an empty row to sit on, where the next typed rune lands.
		rows = slices.Insert(rows, cr+1, tuiInputRow{t.cur, t.cur})
		cr++
		col = 0
	}

	total := len(rows)
	first := 0
	if total > maxRows {
		if cr >= total-maxRows {
			first = total - maxRows
		} else {
			first = max(cr-maxRows+1, 0)
		}
	}
	rows = rows[first:min(first+maxRows, total)]
	lines = make([]string, len(rows))
	for i, rw := range rows {
		lines[i] = string(t.text[rw.from:rw.to])
	}
	return lines, cr - first, col
}
