package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiEnv builds a getenv over a fixed map, so detection never reads the
// developer's real environment.
func tuiEnv(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// tuiTTY opens a character device, which isTerminalWriter accepts as a terminal.
func tuiTTY(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("no character device to stand in for a terminal: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestTUIThemeDetectDepth(t *testing.T) {
	tty := tuiTTY(t)
	tests := []struct {
		name string
		w    io.Writer
		env  map[string]string
		want tuiDepth
	}{
		{"terminal with truecolor", tty, map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, tuiDepthTrueColor},
		{"terminal with 24bit", tty, map[string]string{"TERM": "xterm", "COLORTERM": "24bit"}, tuiDepthTrueColor},
		{"terminal without colorterm", tty, map[string]string{"TERM": "xterm-256color"}, tuiDepth256},
		{"NO_COLOR wins over a terminal", tty, map[string]string{"NO_COLOR": "1", "TERM": "xterm", "COLORTERM": "truecolor"}, tuiDepthNone},
		{"NO_COLOR empty does not disable", tty, map[string]string{"NO_COLOR": "", "TERM": "xterm"}, tuiDepth256},
		{"dumb terminal", tty, map[string]string{"TERM": "dumb"}, tuiDepthNone},
		{"unset TERM still colors (Windows terminals)", tty, map[string]string{}, tuiDepth256},
		{"pipe is plain", &bytes.Buffer{}, map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, tuiDepthNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectTuiTheme(tt.w, tuiEnv(tt.env))
			if got.depth != tt.want {
				t.Fatalf("depth = %v, want %v", got.depth, tt.want)
			}
		})
	}
}

func TestTUIThemePlainHasNoEscapes(t *testing.T) {
	m := tuiColorModel(tuiTheme{})
	if out := render(m, 60, 12); strings.Contains(out, "\x1b") {
		t.Fatalf("plain frame carries escape codes:\n%q", out)
	}
}

func TestTUIThemeTruecolorPaintsTheDesktopPalette(t *testing.T) {
	m := tuiColorModel(tuiTheme{depth: tuiDepthTrueColor})
	out := render(m, 60, 12)
	for _, want := range []string{
		"48;2;20;20;20",    // the panel bar
		"38;2;250;178;131", // the primary accent
		"38;2;224;108;117", // the error red on an error line
		"38;2;245;167;65",  // the running amber on the roster
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("truecolor frame missing %q", want)
		}
	}
}

func TestTUIThemeIndexedColorAt256(t *testing.T) {
	m := tuiColorModel(tuiTheme{depth: tuiDepth256})
	out := render(m, 60, 12)
	if !strings.Contains(out, "38;5;216") {
		t.Fatalf("256-color frame missing the accent index:\n%q", out)
	}
	if strings.Contains(out, "\x1b[38;2;") {
		t.Fatal("256-color frame emitted truecolor SGR")
	}
}

// tuiColorModel is a thread view with an error line, a running agent with a
// claude harness, and an idle header, painted at depth.
func tuiColorModel(theme tuiTheme) *tuiModel {
	at := tuiTestClock()
	m := newTuiModel(at)
	m.theme = theme
	m.info = tuiInfo{daemon: "127.0.0.1:7777", version: "1.2.3"}
	m.openThread(threadListRow{ID: "th_color0000001", Title: "colors", RunningAgents: 1, Agents: 1}, "demo", []contracts.LogEntry{
		{AgentID: "lead", Kind: contracts.LogError, Text: "boom", At: at},
	}, map[string]contracts.AgentStatus{"lead": contracts.AgentRunning})
	m.harness["lead"] = "claude"
	return m
}

func TestTUIThemeLogLinesColorByKind(t *testing.T) {
	at := tuiTestClock()
	m := newTuiModel(at)
	m.theme = tuiTheme{depth: tuiDepthTrueColor}
	m.openThread(threadListRow{ID: "th_kinds000001"}, "demo", []contracts.LogEntry{
		{AgentID: "lead", Kind: contracts.LogTool, Text: "$ go test", At: at},
		{AgentID: "lead", Kind: contracts.LogUsage, Text: "in 10 out 2", At: at},
	}, nil)
	m.appendStatusLines("10:30:00 lead status failed")
	out := render(m, 60, 10)

	wantTool := "\x1b[38;2;92;156;255m" // secondary for tool rows
	if !strings.Contains(out, wantTool) {
		t.Fatalf("tool row not tinted:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;2;110;110;110m10:30:00 ") {
		t.Fatalf("timestamp not drawn faint:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;2;110;110;110m") {
		t.Fatalf("usage row not faint:\n%q", out)
	}
}

func TestTUIThemeStatusInks(t *testing.T) {
	tests := map[string]tuiRGB{
		"running":   tuiColWarn,
		"succeeded": tuiColOK,
		"failed":    tuiColErr,
		"pending":   tuiColFaint,
		"cancelled": tuiColFaint,
	}
	for status, want := range tests {
		if got := tuiStatusInk(status).fg; got != want {
			t.Fatalf("status %s fg = %v, want %v", status, got, want)
		}
	}
}

func TestTUIThemeHarnessTintFallsBackToText(t *testing.T) {
	if got := tuiHarnessInk("claude").fg; got != (tuiRGB{250, 179, 135}) {
		t.Fatalf("claude tint = %v", got)
	}
	if got := tuiHarnessInk("  CODEX ").fg; got != (tuiRGB{166, 209, 137}) {
		t.Fatalf("codex tint (case and space insensitive) = %v", got)
	}
	if got := tuiHarnessInk("mystery").fg; got != tuiColText {
		t.Fatalf("unknown harness fg = %v, want the plain text color", got)
	}
}

// Every row of a boxed screen spans exactly the terminal width, so borders and
// bars line up whatever the size.
func TestTUILayoutWidthsAreExact(t *testing.T) {
	for _, cols := range []int{20, 40, 80} {
		for _, rows := range []int{3, 5, 10, 24} {
			t.Run("threads", func(t *testing.T) {
				m := newTuiModel(tuiTestClock())
				rowsOf := make([]psRow, 3)
				for i := range rowsOf {
					rowsOf[i] = psRow{thread: threadListRow{ID: "th_w" + string(rune('a'+i)), Title: "a title that is long enough to clip", Agents: 1, UpdatedAt: tuiTestClock()}, project: "demo"}
					rowsOf[i].status = threadRowStatus(rowsOf[i].thread)
				}
				m.setList(rowsOf)
				assertTuiWidths(t, render(m, cols, rows), cols, rows, false)
			})
			t.Run("thread", func(t *testing.T) {
				m := tuiColorModel(tuiTheme{})
				assertTuiWidths(t, render(m, cols, rows), cols, rows, true)
			})
		}
	}
}

// assertTuiWidths checks the frame: no row wider than cols, the header and
// footer and every boxed row exactly cols wide. The thread's prompt line is
// the one row left unpadded by design.
func assertTuiWidths(t *testing.T, frame string, cols, rows int, thread bool) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != rows {
		t.Fatalf("frame has %d rows, want %d", len(lines), rows)
	}
	for i, line := range lines {
		plain := tuiPlain(line)
		w := len([]rune(plain))
		if w > cols {
			t.Fatalf("row %d is %d wide, over %d: %q", i, w, cols, plain)
		}
		boxed := strings.HasPrefix(plain, "│") || strings.HasPrefix(plain, "╭") || strings.HasPrefix(plain, "╰")
		bar := rows >= 2 && (i == 0 || i == rows-1)
		if (boxed || bar) && rows >= 3 && cols >= 6 && w != cols {
			if thread && i == rows-2 {
				continue
			}
			t.Fatalf("row %d is %d wide, want %d: %q", i, w, cols, plain)
		}
	}
}

func TestTUIDaemonLabelNeverShowsAQuery(t *testing.T) {
	tests := map[string]string{
		"":                                 "local daemon",
		"http://127.0.0.1:7777/?token=abc": "127.0.0.1:7777",
		"ws://user:pw@daemon.local:9/ws":   "daemon.local:9",
		"not a url ::":                     "local daemon",
	}
	for raw, want := range tests {
		if got := tuiDaemonLabel(raw); got != want {
			t.Fatalf("tuiDaemonLabel(%q) = %q, want %q", raw, got, want)
		}
	}
}

// The header names the wordmark, every tab, and the daemon, in one bar.
func TestTUIHeaderBarCarriesWordmarkTabsAndDaemon(t *testing.T) {
	m := newTuiModel(time.Now())
	m.info = tuiInfo{daemon: "127.0.0.1:7777", version: "9.9.9"}
	line := tuiPlain(render(m, 80, 4))
	first := strings.Split(line, "\n")[0]
	for _, want := range []string{"◆ AgentTunnel", " Threads ", "127.0.0.1:7777  v9.9.9"} {
		if !strings.Contains(first, want) {
			t.Fatalf("header %q missing %q", first, want)
		}
	}

	// The bar reserves the wordmark and the daemon label first: with every
	// tab active in turn, one buried deep in the window never pushes the
	// label off the row, down to widths where the label itself barely fits.
	for _, id := range tuiViewOrder() {
		m.view = id
		for _, cols := range []int{80, 60, 46, 30} {
			header := strings.Split(tuiPlain(render(m, cols, 4)), "\n")[0]
			if !strings.Contains(header, "127.0.0.1:7777") {
				t.Fatalf("active tab %q at %d cols hid the daemon label: %q", id, cols, header)
			}
		}
		// The window keeps the active tab itself on screen too.
		for _, cols := range []int{80, 60} {
			header := strings.Split(tuiPlain(render(m, cols, 4)), "\n")[0]
			if !strings.Contains(header, tuiTabTitle(id)) {
				t.Fatalf("active tab %q not visible at %d cols: %q", id, cols, header)
			}
		}
	}
}

// Every surface renders at every size down to nothing without panicking, and
// never emits more rows than the terminal has.
func TestTUIRenderSurvivesEverySize(t *testing.T) {
	build := []func() *tuiModel{
		func() *tuiModel {
			m := newTuiModel(tuiTestClock())
			m.setList([]psRow{{thread: threadListRow{ID: "th_size0000001", Title: "t", Messages: 2, Agents: 1}, project: "p", depth: 1}})
			return m
		},
		func() *tuiModel { return tuiColorModel(tuiTheme{depth: tuiDepthTrueColor}) },
		func() *tuiModel {
			m := tuiColorModel(tuiTheme{})
			m.askQuestion(questionPromptEntry{id: "q", kind: contracts.QuestionChoice, question: "pick", options: []string{"a", "b"}})
			return m
		},
		func() *tuiModel {
			m := newTuiModel(tuiTestClock())
			m.openPalette()
			return m
		},
	}
	for _, mk := range build {
		for cols := 0; cols <= 24; cols++ {
			for rows := 0; rows <= 12; rows++ {
				m := mk()
				out := render(m, cols, rows)
				if n := strings.Count(out, "\n"); cols >= 1 && rows >= 1 && n+1 > rows {
					t.Fatalf("%dx%d frame has %d rows", cols, rows, n+1)
				}
			}
		}
	}
}

// Widths are counted in terminal cells: CJK, fullwidth, and emoji runes take
// two, so truncation, padding, and wrapping all treat them as the room they
// really occupy and a box never overflows its border.
func TestTUIRowWidthIsCellAware(t *testing.T) {
	if got := tuiText("中文", tuiInk{fg: tuiColText}).width(); got != 4 {
		t.Fatalf("width of two CJK runes = %d, want 4", got)
	}
	if got := tuiText("a😀", tuiInk{}).width(); got != 3 {
		t.Fatalf("width of a + emoji = %d, want 3", got)
	}
	full := tuiText("中文ab", tuiInk{})
	if got, plain := full.fit(3).width(), full.fit(3).plain(); got != 3 || plain != "中…" {
		t.Fatalf("fit(3) of 中文ab = %q (%d cells), want 中… (3)", plain, got)
	}
	if got := tuiText("中", tuiInk{}).padTo(5, tuiInk{}).width(); got != 5 {
		t.Fatalf("padTo(5) of 中 = %d cells", got)
	}
	if got := tuiText("abc中def", tuiInk{}).tail(4).plain(); got != "…def" {
		t.Fatalf("tail(4) of abc中def = %q, want …def", got)
	}
	wrapped := tuiWrap("中文中文中", 4)
	if got := strings.Join(wrapped, "|"); got != "中文|中文|中" {
		t.Fatalf("tuiWrap of wide runes = %q, want 中文|中文|中", got)
	}
	if len(tuiWrap("中中", 6)) != 1 {
		t.Fatalf("a line that fits its width must not wrap: %v", tuiWrap("中中", 6))
	}

	// The whole frame keeps its cell width with wide runes in the content: a
	// title and a log line of CJK must not push any boxed row past its border.
	now := tuiTestClock()
	m := newTuiModel(now)
	m.openThread(threadListRow{ID: "th_wide00000001", Title: strings.Repeat("中", 30)}, "demo", []contracts.LogEntry{
		tuiEntry(now, "lead", strings.Repeat("文", 40)),
	}, map[string]contracts.AgentStatus{"lead": contracts.AgentRunning})
	const cols = 40
	for i, line := range tuiFrameLines(t, m, cols, 12) {
		plain := tuiPlain(line)
		if i == len(line)-1 {
			continue
		}
		if w := stringCells(plain); w > cols {
			t.Fatalf("row %d is %d cells wide with wide runes, over %d: %q", i, w, cols, plain)
		}
	}
}

// The windowed tab bar carries its ellipsis markers and never exceeds the
// row, at either end of the tab order.
func TestTUIHeaderBarWindowsTabsAtBothEnds(t *testing.T) {
	order := tuiViewOrder()
	if len(order) < 3 {
		t.Fatalf("the real tab set is not registered (order %v); this test needs it", order)
	}
	for _, active := range []string{order[0], order[len(order)-1], order[len(order)/2]} {
		m := newTuiModel(time.Now())
		m.info = tuiInfo{daemon: "127.0.0.1:7777", version: "9.9.9"}
		m.view = active
		header := tuiPlain(strings.Split(render(m, 80, 4), "\n")[0])
		if !strings.Contains(header, " "+tuiTabTitle(active)+" ") {
			t.Fatalf("active tab %q missing from windowed header %q", active, header)
		}
		if !strings.Contains(header, "127.0.0.1:7777") {
			t.Fatalf("daemon label missing from windowed header %q", header)
		}
		if !strings.Contains(header, "…") {
			t.Fatalf("windowed header has no ellipsis marker: %q", header)
		}
		if w := stringCells(header); w > 80 {
			t.Fatalf("windowed header is %d cells wide, over 80:\n%q", w, header)
		}
	}
}
