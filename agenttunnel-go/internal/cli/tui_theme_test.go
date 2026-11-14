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

func TestTUIThemeNearest256(t *testing.T) {
	tests := []struct {
		name string
		c    tuiRGB
		want int
	}{
		{"black is the cube origin", tuiRGB{0, 0, 0}, 16},
		{"white is the cube corner", tuiRGB{255, 255, 255}, 231},
		{"mid gray lands on the ramp", tuiRGB{128, 128, 128}, 244},
		{"desktop accent maps into the cube", tuiColAccent, 38},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nearest256(tt.c); got != tt.want {
				t.Fatalf("nearest256(%v) = %d, want %d", tt.c, got, tt.want)
			}
		})
	}
}

// Plain output carries no SGR at all, so pipes and NO_COLOR stay clean.
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
		"\x1b[48;2;26;30;39m",    // the surface-2 bar
		"\x1b[38;2;25;195;200m",  // the accent
		"\x1b[38;2;240;133;133m", // the error red on an error line
		"\x1b[38;2;229;181;103m", // the running amber on the roster
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("truecolor frame missing %q", want)
		}
	}
}

func TestTUIThemeIndexedColorAt256(t *testing.T) {
	m := tuiColorModel(tuiTheme{depth: tuiDepth256})
	out := render(m, 60, 12)
	if !strings.Contains(out, "\x1b[38;5;38m") {
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

	wantTool := "\x1b[38;2;95;217;221m" // accent-2 for tool rows
	if !strings.Contains(out, wantTool) {
		t.Fatalf("tool row not tinted:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;2;140;147;166m10:30:00 ") {
		t.Fatalf("timestamp not drawn faint:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;2;140;147;166m") {
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
