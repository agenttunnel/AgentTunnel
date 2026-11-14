package cli

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

// The TUI's look mirrors the desktop app: the same palette as the :root
// variables in desktop/frontend/public/style.css, the same status colors, and
// the same harness tints as graph.ts. Colors are chosen per terminal depth;
// NO_COLOR, a pipe, or a dumb terminal gets plain text with no SGR at all.

// ANSI styling. The renderer wraps whole spans in these codes, so width math
// always runs on plain runes.
const (
	tuiReset = "\x1b[0m"
	tuiBold  = "\x1b[1m"
	tuiDim   = "\x1b[2m"
)

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

// The desktop's :root palette, by its CSS variable names.
var (
	tuiColBar     = rgb(0x1a1e27) // --surface-2: header and footer bars
	tuiColBorder  = rgb(0x2c3140) // hairline pane borders
	tuiColText    = rgb(0xe6e9f0) // --text
	tuiColDim     = rgb(0xadb4c4) // --dim
	tuiColFaint   = rgb(0x8c93a6) // --faint
	tuiColAccent  = rgb(0x19c3c8) // --accent
	tuiColAccent2 = rgb(0x5fd9dd) // --accent-2
	tuiColAgent   = rgb(0xa78bfa) // --agent (violet, agent mode)
	tuiColOK      = rgb(0x5fd39b) // --ok
	tuiColWarn    = rgb(0xe5b567) // --warn
	tuiColErr     = rgb(0xf08585) // --err

	// --accent-soft over the bar: the selected row and active tab wash.
	tuiColSelect = mixRGB(tuiColAccent, tuiColBar, 14)
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
	case "reasoning", "system", "status":
		return tuiInk{fg: tuiColDim}
	default:
		return tuiInk{fg: tuiColText}
	}
}

// style wraps s in the SGR for ink, or returns s unchanged when there is
// nothing to show.
func (t tuiTheme) style(s string, ink tuiInk) string {
	if t.depth == tuiDepthNone || s == "" {
		return s
	}
	var b strings.Builder
	if ink.bold {
		b.WriteString(tuiBold)
	}
	if ink.dim {
		b.WriteString(tuiDim)
	}
	if ink.fg != (tuiRGB{}) {
		b.WriteString(t.color(ink.fg, false))
	}
	if ink.bg != (tuiRGB{}) {
		b.WriteString(t.color(ink.bg, true))
	}
	if b.Len() == 0 {
		return s
	}
	return b.String() + s + tuiReset
}

// color is the SGR for one color at this depth.
func (t tuiTheme) color(c tuiRGB, background bool) string {
	base := 38
	if background {
		base = 48
	}
	if t.depth == tuiDepthTrueColor {
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", base, c.r, c.g, c.b)
	}
	return fmt.Sprintf("\x1b[%d;5;%dm", base, nearest256(c))
}

// nearest256 maps a color to the closest entry of the xterm 256 palette: the
// 6x6x6 cube or the grayscale ramp, whichever is nearer.
func nearest256(c tuiRGB) int {
	levels := [6]int{0, 95, 135, 175, 215, 255}
	nearestLevel := func(v int) int {
		best := 0
		for i, l := range levels {
			if abs(v-l) < abs(v-levels[best]) {
				best = i
			}
		}
		return best
	}
	ri, gi, bi := nearestLevel(int(c.r)), nearestLevel(int(c.g)), nearestLevel(int(c.b))
	cube := 16 + 36*ri + 6*gi + bi
	cubeColor := tuiRGB{uint8(levels[ri]), uint8(levels[gi]), uint8(levels[bi])}
	// The gray ramp is 232..255 at 8 + 10*i; pick the step nearest the mean.
	avg := (int(c.r) + int(c.g) + int(c.b)) / 3
	step := min(max((avg-8+5)/10, 0), 23)
	gray := uint8(8 + 10*step)
	if dist2(c, tuiRGB{gray, gray, gray}) < dist2(c, cubeColor) {
		return 232 + step
	}
	return cube
}

func dist2(a, b tuiRGB) int {
	dr, dg, db := int(a.r)-int(b.r), int(a.g)-int(b.g), int(a.b)-int(b.b)
	return dr*dr + dg*dg + db*db
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// tuiSpan is a run of text sharing one ink.
type tuiSpan struct {
	text string
	ink  tuiInk
}

// tuiRow is one display line as styled spans. Widths count runes, so layout
// math never sees ANSI codes.
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

// width is the row's length in runes.
func (r tuiRow) width() int {
	n := 0
	for _, s := range r {
		n += utf8.RuneCountInString(s.text)
	}
	return n
}

// cut returns the runes in [from, to) with their inks intact.
func (r tuiRow) cut(from, to int) tuiRow {
	var out tuiRow
	pos := 0
	for _, s := range r {
		runes := []rune(s.text)
		lo, hi := pos, pos+len(runes)
		pos = hi
		a, b := max(from, lo), min(to, hi)
		if a >= b {
			continue
		}
		out = append(out, tuiSpan{text: string(runes[a-lo : b-lo]), ink: s.ink})
	}
	return out
}

// fit cuts the row to width runes. A cut row ends in an ellipsis, the same
// rule tuiTruncate applies to plain lines.
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
	return r.cut(0, width-1).join(tuiRow{{text: "…"}})
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

// tuiFooter renders the key-hint bar: keys in accent, labels dim, all on the
// bar background.
func tuiFooter(cols int, hints []tuiHint) tuiRow {
	bar := tuiInk{bg: tuiColBar}
	var row tuiRow
	for i, h := range hints {
		if i > 0 {
			row = row.join(tuiText("  ", bar))
		}
		row = row.join(tuiText(h.key, tuiInk{fg: tuiColAccent, bg: tuiColBar, bold: true}),
			tuiText(" "+h.label, tuiInk{fg: tuiColDim, bg: tuiColBar}))
	}
	return row.fit(cols).padTo(cols, bar)
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
