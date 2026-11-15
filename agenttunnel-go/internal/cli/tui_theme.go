package cli

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The TUI's look follows opencode and the modern terminal coding agents: a
// near-black canvas, one warm accent, quiet gutters instead of boxes, and
// panels that read by background rather than by border. Colors are chosen per
// terminal depth and rendered through lipgloss; NO_COLOR, a pipe, or a dumb
// terminal gets plain text with no SGR at all.

// tuiDepth is how many colors the terminal can show.
type tuiDepth int

const (
	tuiDepthNone      tuiDepth = iota // plain text
	tuiDepth256                       // xterm 256-color palette
	tuiDepthTrueColor                 // 24-bit color
)

// tuiTheme turns an ink into SGR at the terminal's depth. The zero value is
// plain, which is what tests and non-terminal output get.
type tuiTheme struct {
	depth tuiDepth
}

// detectTuiTheme picks the depth for output w. Color needs a terminal, a
// TERM that is not dumb, and no NO_COLOR (any non-empty value, per no-color.org).
// An unset TERM still gets color: Windows terminals rarely set it.
func detectTuiTheme(w io.Writer, getenv func(string) string) tuiTheme {
	if getenv("NO_COLOR") != "" || !isTerminalWriter(w) {
		return tuiTheme{}
	}
	if getenv("TERM") == "dumb" {
		return tuiTheme{}
	}
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return tuiTheme{depth: tuiDepthTrueColor}
	}
	return tuiTheme{depth: tuiDepth256}
}

// tuiRGB is one color in 8-bit channels. The zero value means "unset" in an
// ink, which is safe because no palette entry is pure black.
type tuiRGB struct{ r, g, b uint8 }

func rgb(v uint32) tuiRGB {
	return tuiRGB{uint8(v >> 16), uint8(v >> 8), uint8(v)}
}

// mixRGB blends a into b: pct percent of a, the rest of b.
func mixRGB(a, b tuiRGB, pct int) tuiRGB {
	mix := func(x, y uint8) uint8 {
		return uint8((int(x)*pct + int(y)*(100-pct)) / 100)
	}
	return tuiRGB{mix(a.r, b.r), mix(a.g, b.g), mix(a.b, b.b)}
}

// The palette is opencode's default dark theme. The names predate it and stay
// so views keep reading by role: bar is the panel background, accent is the
// primary peach, agent is the secondary blue.
var (
	tuiColBar     = rgb(0x141414) // panel: header, input, and dialog background
	tuiColBorder  = rgb(0x3c3c3c) // subtle borders and gutters
	tuiColText    = rgb(0xeeeeee)
	tuiColDim     = rgb(0xa0a0a0)
	tuiColFaint   = rgb(0x6e6e6e) // muted
	tuiColAccent  = rgb(0xfab283) // primary
	tuiColAccent2 = rgb(0x5c9cff) // secondary: tools and links
	tuiColAgent   = rgb(0x9d7cd8) // purple: reasoning and agent mode
	tuiColOK      = rgb(0x7fd88f)
	tuiColWarn    = rgb(0xf5a742)
	tuiColErr     = rgb(0xe06c75)
	tuiColInk     = rgb(0x0a0a0a) // text on a primary-colored background

	// The selected row: a primary wash, dark text on top.
	tuiColSelect = tuiColAccent
)

// tuiHarnessColors are the foreground tints graph.ts assigns each harness,
// lifted from the desktop's h-* washes to their solid hue.
var tuiHarnessColors = map[string]tuiRGB{
	"claude":      rgb(0xfab387),
	"opencode":    rgb(0x8ca9ee),
	"codex":       rgb(0xa6d189),
	"cursor":      rgb(0xed8796),
	"pi":          rgb(0xf4e794),
	"commandcode": rgb(0xe3c471),
	"grok":        rgb(0xabb2c4),
}

// tuiInk is the styling of one span. Zero fields inherit nothing: plain text.
type tuiInk struct {
	fg, bg    tuiRGB
	bold, dim bool
}

// tuiStatusInk colors an agent or thread status the way the desktop's .dot
// classes do: running amber, succeeded green, failed red, everything else faint.
func tuiStatusInk(status string) tuiInk {
	switch status {
	case "running":
		return tuiInk{fg: tuiColWarn}
	case "succeeded", "clean":
		return tuiInk{fg: tuiColOK}
	case "failed":
		return tuiInk{fg: tuiColErr}
	default:
		return tuiInk{fg: tuiColFaint}
	}
}

// tuiHarnessInk tints a harness's name; an unknown or empty harness keeps the
// plain text color.
func tuiHarnessInk(harness string) tuiInk {
	if c, ok := tuiHarnessColors[strings.ToLower(strings.TrimSpace(harness))]; ok {
		return tuiInk{fg: c}
	}
	return tuiInk{fg: tuiColText}
}

// tuiKindInk colors a log line by its entry kind. Unknown kinds read as text.
func tuiKindInk(kind string) tuiInk {
	switch kind {
	case "error":
		return tuiInk{fg: tuiColErr}
	case "tool":
		return tuiInk{fg: tuiColAccent2}
	case "usage", "raw":
		return tuiInk{fg: tuiColFaint}
	case "reasoning":
		return tuiInk{fg: tuiColAgent}
	case "system", "status":
		return tuiInk{fg: tuiColDim}
	default:
		return tuiInk{fg: tuiColText}
	}
}

// Renderers per depth, so a lipgloss style downsamples to the depth the
// theme asks for no matter what the host terminal is.
var (
	tuiTrueColorRenderer = newTuiRenderer(termenv.TrueColor)
	tui256Renderer       = newTuiRenderer(termenv.ANSI256)
)

func newTuiRenderer(profile termenv.Profile) *lipgloss.Renderer {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(profile)
	return r
}

// style wraps s in the SGR for ink, or returns s unchanged when there is
// nothing to show.
func (t tuiTheme) style(s string, ink tuiInk) string {
	if t.depth == tuiDepthNone || s == "" || ink == (tuiInk{}) {
		return s
	}
	r := tui256Renderer
	if t.depth == tuiDepthTrueColor {
		r = tuiTrueColorRenderer
	}
	st := r.NewStyle().Bold(ink.bold).Faint(ink.dim)
	if ink.fg != (tuiRGB{}) {
		st = st.Foreground(lipgloss.Color(ink.fg.hex()))
	}
	if ink.bg != (tuiRGB{}) {
		st = st.Background(lipgloss.Color(ink.bg.hex()))
	}
	return st.Render(s)
}

func (c tuiRGB) hex() string { return fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b) }

// tuiSpan is a run of text sharing one ink.
type tuiSpan struct {
	text string
	ink  tuiInk
}

// tuiRow is one display line as styled spans. Widths count terminal cells, so
// layout math never sees ANSI codes and never undercounts wide runes.
type tuiRow []tuiSpan

// tuiText is a single-ink row; the empty string gives an empty row.
func tuiText(s string, ink tuiInk) tuiRow {
	if s == "" {
		return nil
	}
	return tuiRow{{text: s, ink: ink}}
}

// join appends rows and spans in order.
func (r tuiRow) join(parts ...tuiRow) tuiRow {
	out := append(tuiRow(nil), r...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// runeCells is how many terminal cells r occupies: wide runes — CJK,
// fullwidth forms, emoji — take two, everything else one.
func runeCells(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E,   // CJK radicals and symbols
		r >= 0x3041 && r <= 0x33FF,   // kana, CJK compatibility
		r >= 0x3400 && r <= 0x4DBF,   // CJK extension A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK unified ideographs
		r >= 0xA000 && r <= 0xA4CF,   // Yi
		r >= 0xAC00 && r <= 0xD7A3,   // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF,   // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE4F,   // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60,   // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,   // fullwidth signs
		r >= 0x1F300 && r <= 0x1F64F, // emoji: pictographs, emoticons
		r >= 0x1F680 && r <= 0x1F6FF, // emoji: transport and map
		r >= 0x1F900 && r <= 0x1F9FF, // emoji: supplemental
		r >= 0x1FA70 && r <= 0x1FAFF: // emoji: symbols and pictographs extended
		return 2
	}
	return 1
}

// stringCells is s's width in terminal cells.
func stringCells(s string) int {
	n := 0
	for _, r := range s {
		n += runeCells(r)
	}
	return n
}

// width is the row's length in terminal cells.
func (r tuiRow) width() int {
	n := 0
	for _, s := range r {
		n += stringCells(s.text)
	}
	return n
}

// cut returns the cells in [from, to) with their inks intact. A rune that
// straddles a boundary is kept, so text is never silently dropped.
func (r tuiRow) cut(from, to int) tuiRow {
	var out tuiRow
	pos := 0
	for _, s := range r {
		var b strings.Builder
		for _, rn := range s.text {
			w := runeCells(rn)
			if pos < to && pos+w > from {
				b.WriteRune(rn)
			}
			pos += w
		}
		if b.Len() > 0 {
			out = append(out, tuiSpan{text: b.String(), ink: s.ink})
		}
	}
	return out
}

// fit cuts the row to width cells. A cut row ends in an ellipsis, the same
// rule tuiTruncate applies to plain lines; a wide rune that would straddle
// the cut is dropped whole.
func (r tuiRow) fit(width int) tuiRow {
	if width < 1 {
		return nil
	}
	if r.width() <= width {
		return r
	}
	if width == 1 {
		return tuiRow{{text: "…"}}
	}
	out := make(tuiRow, 0, len(r)+1)
	cells := 0
	full := false
	for _, s := range r {
		if full {
			break
		}
		var b strings.Builder
		for _, rn := range s.text {
			w := runeCells(rn)
			if cells+w > width-1 {
				full = true
				break
			}
			b.WriteRune(rn)
			cells += w
		}
		if b.Len() > 0 {
			out = append(out, tuiSpan{text: b.String(), ink: s.ink})
		}
	}
	return append(out, tuiSpan{text: "…"})
}

// tail keeps the row's end visible: when it is wider than width cells, the
// head is cut to an ellipsis so the caret side of a long typed line stays on
// screen.
func (r tuiRow) tail(width int) tuiRow {
	if width < 1 {
		return nil
	}
	if r.width() <= width {
		return r
	}
	if width == 1 {
		return tuiRow{{text: "…"}}
	}
	// Collect the spans of the last width-1 cells, then lead with an ellipsis.
	var spans tuiRow
	cells := 0
	for i := len(r) - 1; i >= 0 && cells < width-1; i-- {
		runes := []rune(r[i].text)
		var kept []rune
		for j := len(runes) - 1; j >= 0; j-- {
			w := runeCells(runes[j])
			if cells+w > width-1 {
				break
			}
			kept = append([]rune{runes[j]}, kept...)
			cells += w
		}
		if len(kept) > 0 {
			spans = append(tuiRow{{text: string(kept), ink: r[i].ink}}, spans...)
		}
	}
	return tuiRow{{text: "…"}}.join(spans)
}

// padTo extends the row to width with ink-colored spaces, so a bar or a
// selected row paints its whole width.
func (r tuiRow) padTo(width int, ink tuiInk) tuiRow {
	if n := width - r.width(); n > 0 {
		return r.join(tuiRow{{text: strings.Repeat(" ", n), ink: ink}})
	}
	return r
}

// withBG paints every span of the row onto background bg.
func (r tuiRow) withBG(bg tuiRGB) tuiRow {
	out := make(tuiRow, len(r))
	for i, s := range r {
		s.ink.bg = bg
		out[i] = s
	}
	return out
}

// selected paints the row as the highlighted one: the primary wash with dark
// text over every span, so status colors never fight the highlight.
func (r tuiRow) selected() tuiRow {
	out := make(tuiRow, len(r))
	for i, s := range r {
		s.ink = tuiInk{fg: tuiColInk, bg: tuiColSelect, bold: s.ink.bold}
		out[i] = s
	}
	return out
}

// render is the row's bytes at theme t.
func (r tuiRow) render(t tuiTheme) string {
	var b strings.Builder
	for _, s := range r {
		b.WriteString(t.style(s.text, s.ink))
	}
	return b.String()
}

// plain is the row's text with no styling, for tests and logs.
func (r tuiRow) plain() string {
	var b strings.Builder
	for _, s := range r {
		b.WriteString(s.text)
	}
	return b.String()
}

// tuiBox frames inner rows in a rounded border exactly height rows tall. The
// top edge carries title at the left and tail at the right. Inner rows get a
// column of padding on each side. Overlong rows are cut; short ones padded.
func tuiBox(cols, height int, title, tail tuiRow, inner []tuiRow) []tuiRow {
	border := tuiInk{fg: tuiColBorder}
	innerH := height - 2
	if cols < 6 || innerH < 0 {
		return nil
	}
	width := cols - 2
	contentW := cols - 4

	// Build the top edge around the title and tail, dropping them when the
	// width cannot hold them.
	fillFor := func(t, tl tuiRow) int {
		n := 3 // "╭─" and "╮"
		if len(t) > 0 {
			n += t.width() + 2
		}
		if len(tl) > 0 {
			n += tl.width() + 3
		}
		return cols - n
	}
	if len(tail) > 0 && fillFor(title, tail) < 1 {
		tail = nil
	}
	if len(title) > 0 && fillFor(title, tail) < 1 {
		title = title.fit(max(cols-8, 1))
	}
	fill := max(fillFor(title, tail), 0)

	top := tuiText("╭─", border)
	if len(title) > 0 {
		top = top.join(tuiText(" ", tuiInk{}), title, tuiText(" ", tuiInk{}))
	}
	top = top.join(tuiText(strings.Repeat("─", fill), border))
	if len(tail) > 0 {
		top = top.join(tuiText(" ", tuiInk{}), tail, tuiText(" ─", border))
	}
	top = top.join(tuiText("╮", border))

	rows := make([]tuiRow, 0, height)
	rows = append(rows, top)
	if len(inner) > innerH {
		inner = inner[len(inner)-innerH:]
	}
	side := tuiText("│", border)
	for i := 0; i < innerH; i++ {
		var content tuiRow
		if i < len(inner) {
			content = inner[i].fit(contentW)
		}
		row := side.join(tuiText(" ", tuiInk{}), content.padTo(contentW, tuiInk{}), tuiText(" ", tuiInk{}), side)
		rows = append(rows, row)
	}
	rows = append(rows, tuiText("╰"+strings.Repeat("─", width)+"╯", border))
	return rows
}

// tuiHint is one key and its label in the footer bar.
type tuiHint struct{ key, label string }

// tuiFooter renders the key-hint line the way opencode's does: the key in
// bright text, its label muted, no bar behind them.
func tuiFooter(cols int, hints []tuiHint) tuiRow {
	row := tuiText(" ", tuiInk{})
	for i, h := range hints {
		if i > 0 {
			row = row.join(tuiText("  ", tuiInk{}))
		}
		row = row.join(tuiText(h.key, tuiInk{fg: tuiColText, bold: true}),
			tuiText(" "+h.label, tuiInk{fg: tuiColFaint}))
	}
	return row.fit(cols).padTo(cols, tuiInk{})
}

// tuiDaemonLabel is the header's daemon name: the host of an explicit
// daemon URL (never its query, which can carry a token), else "local daemon".
func tuiDaemonLabel(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "local daemon"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "local daemon"
	}
	return u.Host
}
