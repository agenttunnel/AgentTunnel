package cli

import "strings"

// tuiCell is one rune of a row with its ink and width. Wrapping cuts on cells
// so a wide rune is never split and widths are counted once.
type tuiCell struct {
	r    rune
	ink  tuiInk
	w    int
	link string
}

func tuiCellsOf(row tuiRow) []tuiCell {
	var cells []tuiCell
	for _, s := range row {
		for _, r := range s.text {
			cells = append(cells, tuiCell{r: r, ink: s.ink, w: runeCells(r), link: s.link})
		}
	}
	return cells
}

// tuiCellRow rebuilds a row from cells, merging runs that share an ink and a
// link, so a link wrapped over two rows is a link on both.
func tuiCellRow(cells []tuiCell) tuiRow {
	var out tuiRow
	var b strings.Builder
	for i, c := range cells {
		if i > 0 && (c.ink != cells[i-1].ink || c.link != cells[i-1].link) {
			out = append(out, tuiSpan{text: b.String(), ink: cells[i-1].ink, link: cells[i-1].link})
			b.Reset()
		}
		b.WriteRune(c.r)
	}
	if n := len(cells); n > 0 {
		out = append(out, tuiSpan{text: b.String(), ink: cells[n-1].ink, link: cells[n-1].link})
	}
	return out
}

// tuiFitEnd is the index of the first cell from start that no longer fits in
// avail cells.
func tuiFitEnd(cells []tuiCell, start, avail int) int {
	cols, i := 0, start
	for i < len(cells) && cols+cells[i].w <= avail {
		cols += cells[i].w
		i++
	}
	return i
}

// tuiForce replaces a cell that can never fit with an ellipsis, so a row
// always makes progress and keeps its width.
func tuiForce(cells []tuiCell, i int) {
	cells[i] = tuiCell{r: '…', ink: cells[i].ink, w: 1, link: cells[i].link}
}

func tuiSkipSpaces(cells []tuiCell, i int) int {
	for i < len(cells) && cells[i].r == ' ' {
		i++
	}
	return i
}

// tuiBreakAt picks where the row that starts at cells[start] ends when it may
// use avail cells. next is where the following row starts: after a breaking
// space, past any run of spaces.
func tuiBreakAt(cells []tuiCell, start, avail int) (end, next int) {
	fit := tuiFitEnd(cells, start, avail)
	if fit == len(cells) {
		return fit, fit
	}
	if fit == start {
		tuiForce(cells, start)
		return start + 1, start + 1
	}
	at := -1
	if cells[fit].r == ' ' {
		at = fit
	} else {
		for j := fit - 1; j > start && at < 0; j-- {
			if cells[j].r == ' ' {
				at = j
			}
		}
	}
	if at < 0 {
		return fit, fit
	}
	end = at
	for end > start && cells[end-1].r == ' ' {
		end--
	}
	return end, tuiSkipSpaces(cells, at)
}

// tuiWrapRow word-wraps row to width cells. The first row keeps the row's
// indentation; every later row starts with hang plain spaces.
func tuiWrapRow(row tuiRow, width, hang int) []tuiRow {
	width = max(width, 1)
	if hang < 0 || hang >= width {
		hang = 0
	}
	if row.width() <= width {
		return []tuiRow{row}
	}
	cells := tuiCellsOf(row)
	lead := tuiSkipSpaces(cells, 0)
	indent := min(lead, width-1)
	line := tuiCellRow(cells[:indent])
	avail := width - indent
	start := lead
	var out []tuiRow
	for {
		end, next := tuiBreakAt(cells, start, avail)
		out = append(out, line.join(tuiCellRow(cells[start:end])))
		if next >= len(cells) {
			return out
		}
		start = next
		line = tuiSpaces(hang)
		avail = width - hang
	}
}

// tuiSpaces is n plain spaces, or nil for n == 0.
func tuiSpaces(n int) tuiRow {
	if n == 0 {
		return nil
	}
	return tuiRow{{text: strings.Repeat(" ", n)}}
}

// tuiHardSplit cuts row into rows of at most width cells at cell boundaries,
// ignoring spaces. A blank row gives one nil row.
func tuiHardSplit(row tuiRow, width int) []tuiRow {
	cells := tuiCellsOf(row)
	var out []tuiRow
	for start := 0; ; {
		end := tuiFitEnd(cells, start, width)
		if end == start && start < len(cells) {
			tuiForce(cells, start)
			end = start + 1
		}
		out = append(out, tuiCellRow(cells[start:end]))
		if end >= len(cells) {
			return out
		}
		start = end
	}
}

// tuiMarkdownRows renders agent-written markdown as styled rows at most width
// cells wide. base is the ink of prose. It tolerates partial input: an
// unclosed fence runs to the end, and unmatched markers stay literal.
func tuiMarkdownRows(text string, width int, base tuiInk) []tuiRow {
	if text == "" {
		return nil
	}
	width = max(width, 1)
	var out []tuiRow
	// Blank lines are held and emitted only before the next content row, so
	// runs collapse to one and the result never starts or ends with one.
	pendingBlank := false
	emit := func(rows ...tuiRow) {
		if pendingBlank && len(out) > 0 {
			out = append(out, nil)
		}
		pendingBlank = false
		out = append(out, rows...)
	}
	inCode := false
	for _, raw := range strings.Split(text, "\n") {
		line := tuiSanitize(strings.ReplaceAll(raw, "\t", "    "))
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "```"):
			inCode = !inCode
			if inCode {
				if lang := strings.TrimSpace(trim[3:]); lang != "" {
					emit(tuiText(lang, tuiInk{fg: tuiColFaint}).fit(width))
				}
			}
		case inCode:
			emit(tuiCodeRows(line, width)...)
		case trim == "":
			pendingBlank = true
		default:
			emit(tuiMarkdownBlock(line, width, base)...)
		}
	}
	return out
}

// tuiCodeRows is one line of a fenced block as a panel row exactly width wide.
// Code is never reflowed or styled inline.
func tuiCodeRows(line string, width int) []tuiRow {
	pad := min(2, width-1)
	ink := tuiInk{fg: tuiColText, bg: tuiColBar}
	prefix := tuiText(strings.Repeat(" ", pad), ink)
	rows := tuiHardSplit(tuiText(line, ink), width-pad)
	for i, r := range rows {
		rows[i] = prefix.join(r).padTo(width, tuiInk{bg: tuiColBar})
	}
	return rows
}

// tuiMarkdownBlock renders one line outside a code fence.
func tuiMarkdownBlock(line string, width int, base tuiInk) []tuiRow {
	trim := strings.TrimSpace(line)
	rest := strings.TrimLeft(line, " ")
	indent := len(line) - len(rest)
	border := tuiInk{fg: tuiColBorder}

	if tuiIsRule(trim) {
		return []tuiRow{tuiText(strings.Repeat("─", width), border)}
	}
	if strings.HasPrefix(trim, "|") {
		return tuiHardSplit(tuiText(line, base), width)
	}
	if n := tuiHeadingLevel(rest); n > 0 {
		heading := tuiInk{fg: tuiColAccent, bold: true}
		return tuiWrapRow(tuiInlineRow(rest[n+1:], heading), width, 0)
	}
	if strings.HasPrefix(rest, ">") {
		text := strings.TrimPrefix(rest[1:], " ")
		row := tuiText(line[:indent], base).join(
			tuiText("│ ", border), tuiText(text, tuiInk{fg: tuiColDim}))
		return tuiWrapRow(row, width, 2)
	}
	if strings.HasPrefix(rest, "- ") || strings.HasPrefix(rest, "* ") || strings.HasPrefix(rest, "+ ") {
		row := tuiText(line[:indent]+"• ", base).join(tuiInlineRow(rest[2:], base))
		return tuiWrapRow(row, width, indent+2)
	}
	if marker, body, ok := tuiNumbered(rest); ok {
		row := tuiText(line[:indent]+marker+" ", base).join(tuiInlineRow(body, base))
		return tuiWrapRow(row, width, indent+len(marker)+1)
	}
	return tuiWrapRow(tuiInlineRow(line, base), width, 0)
}

// tuiIsRule reports whether s is a horizontal rule: three or more of one of
// "-", "*", "_" and nothing else.
func tuiIsRule(s string) bool {
	if len(s) < 3 || !strings.ContainsRune("-*_", rune(s[0])) {
		return false
	}
	return strings.Count(s, s[:1]) == len(s)
}

// tuiHeadingLevel is the number of # markers when s is an ATX heading, else 0.
func tuiHeadingLevel(s string) int {
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(s) || s[n] != ' ' {
		return 0
	}
	return n
}

// tuiNumbered splits "12. text" or "12) text" into its marker and body.
func tuiNumbered(s string) (marker, body string, ok bool) {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n == 0 || n+1 >= len(s) || (s[n] != '.' && s[n] != ')') || s[n+1] != ' ' {
		return "", "", false
	}
	return s[:n+1], s[n+2:], true
}

// tuiInlineRow styles `code` and **bold** over base. Code takes the accent2
// color and keeps base's background. Single * and _ never emphasize, and an
// unmatched marker stays literal.
func tuiInlineRow(s string, base tuiInk) tuiRow {
	var out tuiRow
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			out = append(out, tuiSpan{text: plain.String(), ink: base})
			plain.Reset()
		}
	}
	for i := 0; i < len(s); {
		if s[i] == '`' {
			if end := strings.IndexByte(s[i+1:], '`'); end >= 0 {
				flush()
				if end > 0 {
					code := tuiInk{fg: tuiColAccent2, bg: base.bg}
					out = append(out, tuiSpan{text: s[i+1 : i+1+end], ink: code})
				}
				i += end + 2
				continue
			}
		}
		if url := tuiURLAt(s, i); url != "" {
			flush()
			link := base
			link.fg = tuiColAccent2
			out = append(out, tuiLink(url, url, link)...)
			i += len(url)
			continue
		}
		if strings.HasPrefix(s[i:], "**") {
			if end := tuiBoldClose(s, i+2); end >= 0 {
				flush()
				bold := base
				bold.bold = true
				out = append(out, tuiInlineRow(s[i+2:end], bold)...)
				i = end + 2
				continue
			}
			plain.WriteString("**")
			i += 2
			continue
		}
		plain.WriteByte(s[i])
		i++
	}
	flush()
	return out
}

// tuiURLAt is the http(s) URL that starts at s[i], or "". It must start a
// word, runs to the next whitespace, and gives back the punctuation prose and
// markdown put after a link: "see https://x.y/z." links without the full stop,
// and "[title](https://x.y/z)" without the closing bracket.
func tuiURLAt(s string, i int) string {
	rest := s[i:]
	if !strings.HasPrefix(rest, "https://") && !strings.HasPrefix(rest, "http://") {
		return ""
	}
	if i > 0 && tuiWordByte(s[i-1]) {
		return ""
	}
	end := strings.IndexAny(rest, " \t<>\"`")
	if end < 0 {
		end = len(rest)
	}
	url := strings.TrimRight(rest[:end], ".,;:!?)]}'*")
	if !tuiSafeURL(url) || strings.HasSuffix(url, "://") {
		return ""
	}
	return url
}

func tuiWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// tuiBoldClose finds the "**" that closes bold text opened before from. Code
// spans are skipped, so ** inside `code` never closes bold. It returns -1 when
// nothing closes it.
func tuiBoldClose(s string, from int) int {
	for k := from; k < len(s); {
		switch {
		case s[k] == '`':
			if end := strings.IndexByte(s[k+1:], '`'); end >= 0 {
				k += end + 2
			} else {
				k++
			}
		case strings.HasPrefix(s[k:], "**"):
			return k
		default:
			k++
		}
	}
	return -1
}
