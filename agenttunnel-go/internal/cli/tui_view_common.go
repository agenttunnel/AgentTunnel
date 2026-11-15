package cli

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"
)

// tuiCallTimeout bounds one daemon request a view makes. Forge and model calls
// can run long, so they go through tuiBase.run instead of blocking the screen.
const tuiCallTimeout = 30 * time.Second

// tuiGate decides when a view refetches. A view loads when it has never loaded,
// when a reload was asked for, or when its ttl has elapsed. Poll ticks call Load
// every couple of seconds, so the ttl keeps forge CLIs from running on every tick.
type tuiGate struct {
	loaded bool
	at     time.Time
	force  bool
}

func (g *tuiGate) due(now time.Time, ttl time.Duration) bool {
	return g.force || !g.loaded || now.Sub(g.at) >= ttl
}

func (g *tuiGate) finish(now time.Time) {
	g.loaded, g.at, g.force = true, now, false
}

// tuiBase is the state every registered view embeds. Load binds the context and
// client the session gave it; keys and palette actions reuse that binding.
// Fields read by a background run are guarded by mu; the view's own data is only
// touched from the session goroutine.
type tuiBase struct {
	mu     sync.Mutex
	gate   tuiGate
	ctx    context.Context
	c      cliCaller
	cursor int
	ed     tuiEditor
	armed  tuiArm
	busy   string
	// loading is a queued fetch still in flight, so a poll tick never
	// stacks a second one behind it.
	loading bool
	notice  string
	err     string
	// idle is closed when the latest run finishes, so a test can wait for its
	// outcome instead of sleeping.
	idle chan struct{}
}

// bind records the session context and client on every load.
func (b *tuiBase) bind(ctx context.Context, c cliCaller) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ctx, b.c = ctx, c
}

// bound is the recorded context and client, set by Load.
func (b *tuiBase) bound() (context.Context, cliCaller) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ctx, b.c
}

// call makes one synchronous daemon request. Use it for reads the local daemon
// answers at once; anything that shells out to a forge or a model goes to run
// or a queued load job.
func (b *tuiBase) call(method string, params, out any) error {
	ctx, c := b.bound()
	if c == nil || ctx == nil {
		return errors.New("not connected yet")
	}
	ctx, cancel := context.WithTimeout(ctx, tuiCallTimeout)
	defer cancel()
	return c.Call(ctx, method, params, out)
}

// run starts a slow daemon action in the background so the screen keeps
// painting. While it runs the status row names it; its outcome becomes the
// notice, and the view reloads on its next load. A second action while one is
// in flight is refused rather than queued.
func (b *tuiBase) run(label string, fn func(ctx context.Context, c cliCaller) (string, error)) {
	b.mu.Lock()
	if b.busy != "" {
		b.notice, b.err = "", "still working on "+b.busy
		b.mu.Unlock()
		return
	}
	if b.c == nil || b.ctx == nil {
		b.err = "not connected yet"
		b.mu.Unlock()
		return
	}
	b.busy, b.notice, b.err = label, "", ""
	b.idle = make(chan struct{})
	idle := b.idle
	ctx, c := b.ctx, b.c
	b.mu.Unlock()

	go func() {
		defer close(idle)
		ctx, cancel := context.WithTimeout(ctx, tuiCallTimeout)
		defer cancel()
		msg, err := fn(ctx, c)
		b.mu.Lock()
		defer b.mu.Unlock()
		b.busy = ""
		if err != nil {
			b.err, b.notice = err.Error(), ""
		} else {
			b.notice, b.err = msg, ""
		}
		b.gate.force = true
	}()
}

// setErr records a failed synchronous read or action, keeping the last good data.
func (b *tuiBase) setErr(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.err, b.notice = err.Error(), ""
	} else {
		b.err = ""
	}
}

// say sets the notice line for a finished synchronous action.
func (b *tuiBase) say(msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notice, b.err = msg, ""
}

// status is the one-line feedback row: a running action, a fetch in flight,
// else an error, else the last result. It is read under the lock because run
// and the queued load write it.
func (b *tuiBase) status() tuiRow {
	b.mu.Lock()
	busy, loading := b.busy, b.loading
	notice, errText := b.notice, b.err
	armed := b.armed.prompt
	b.mu.Unlock()
	switch {
	case busy != "":
		return tuiText("… "+busy, tuiInk{fg: tuiColWarn})
	case loading:
		return tuiText("… refreshing", tuiInk{fg: tuiColWarn})
	case errText != "":
		return tuiText("✗ "+errText, tuiInk{fg: tuiColErr})
	case armed != "":
		return tuiText(armed, tuiInk{fg: tuiColWarn, bold: true})
	case notice != "":
		return tuiText(notice, tuiInk{fg: tuiColOK})
	}
	return nil
}

// tuiLoadView fetches a view's data when its gate says so. The gate is closed
// before the fetch, so a failing forge CLI is retried after the ttl, not on every tick.
func tuiLoadView(ctx context.Context, c cliCaller, b *tuiBase, ttl time.Duration, fetch func() error) {
	b.bind(ctx, c)
	b.mu.Lock()
	due := b.gate.due(time.Now(), ttl)
	if due {
		b.gate.finish(time.Now())
	}
	b.mu.Unlock()
	if !due {
		return
	}
	b.setErr(fetch())
}

// tuiLoadJob queues a view's slow fetch as a background job the session loop
// runs, so Load stays cache-only: the loop never blocks on a forge or network
// call. The job's fold applies on the loop, where the view's data is safe to
// write. The gate closes at queue time, so a failed fetch retries only after
// the ttl, and a fetch already in flight is not stacked behind.
func tuiLoadJob(m *tuiModel, b *tuiBase, ttl time.Duration, job tuiJob) bool {
	b.mu.Lock()
	if b.loading || !b.gate.due(time.Now(), ttl) {
		b.mu.Unlock()
		return false
	}
	b.gate.finish(time.Now())
	b.loading = true
	b.mu.Unlock()
	m.startJob(func(ctx context.Context, c cliCaller) func(*tuiModel) {
		apply := job(ctx, c)
		return func(m *tuiModel) {
			b.mu.Lock()
			b.loading = false
			b.mu.Unlock()
			if apply != nil {
				apply(m)
			}
		}
	})
	return true
}

// tuiReloadJob queues a fetch now, for a view's refresh key: the gate is
// forced first so the ttl cannot delay it.
func tuiReloadJob(m *tuiModel, b *tuiBase, job tuiJob) {
	b.mu.Lock()
	b.gate.force = true
	b.mu.Unlock()
	tuiLoadJob(m, b, 0, job)
}

// tuiViewErrJob reports a failed background fetch on its view's status row,
// keeping the last good data. The base is captured when the job is queued and
// outlives the fetch.
func tuiViewErrJob(b *tuiBase, err error) func(*tuiModel) {
	return func(*tuiModel) {
		b.setErr(err)
	}
}

// reload forces the next load and runs it now, for the view's refresh key.
func (b *tuiBase) reload(fetch func() error) {
	b.mu.Lock()
	b.gate.finish(time.Now())
	b.mu.Unlock()
	b.setErr(fetch())
}

// tuiArm is a destructive action waiting for its confirming key.
type tuiArm struct {
	name, prompt string
	key          rune
}

// confirm guards an action that changes state outside the TUI (a merge, a
// remove, a submit). The first press of key arms it and shows prompt; the same
// key again returns true and runs it. touch disarms it on any other key.
func (b *tuiBase) confirm(name, prompt string, k tuiKey) bool {
	if k.Kind != keyRune {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.armed.name == name && b.armed.key == k.Rune {
		b.armed = tuiArm{}
		return true
	}
	b.armed = tuiArm{name: name, prompt: prompt, key: k.Rune}
	return false
}

// touch runs first in every view's key handler: any key other than the one
// that armed an action clears the arming, so a stray key can never leave a
// merge one press away.
func (b *tuiBase) touch(k tuiKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.armed.name != "" && (k.Kind != keyRune || k.Rune != b.armed.key) {
		b.armed = tuiArm{}
	}
}

// tuiStateOf returns view id's typed state, creating it on first use.
func tuiStateOf[T any](m *tuiModel, id string) *T {
	if m.viewState == nil {
		m.viewState = map[string]any{}
	}
	if s, ok := m.viewState[id].(*T); ok {
		return s
	}
	s := new(T)
	m.viewState[id] = s
	return s
}

// tuiEditor is a one-line entry a view opens for text it needs: a model name, a
// commit message, a comment. While open it takes every key but Ctrl-C.
type tuiEditor struct {
	on   bool
	what string
	text []rune
}

func (e *tuiEditor) open(what, initial string) {
	e.on, e.what, e.text = true, what, []rune(initial)
}

// key edits the line and reports whether Enter submitted it. The text stays
// readable through value until the caller takes it.
func (e *tuiEditor) key(k tuiKey) (submitted bool) {
	switch k.Kind {
	case keyEsc:
		e.on, e.text = false, nil
	case keyEnter:
		e.on = false
		return true
	case keyBackspace:
		if len(e.text) > 0 {
			e.text = e.text[:len(e.text)-1]
		}
	case keyRune:
		e.text = append(e.text, k.Rune)
	}
	return false
}

func (e *tuiEditor) value() string { return strings.TrimSpace(string(e.text)) }

// row is the editor's prompt line, with a cursor after the text.
func (e *tuiEditor) row() tuiRow {
	return tuiText(e.what+": ", tuiInk{fg: tuiColAccent, bold: true}).
		join(tuiText(string(e.text), tuiInk{fg: tuiColText}), tuiText("▏", tuiInk{fg: tuiColAccent}))
}

// tuiNav maps a movement key to a step: j/down and k/up move one row, PgDn and
// PgUp move page rows. Zero means the key does not move a cursor.
func tuiNav(k tuiKey, page int) int {
	switch {
	case k.Kind == keyDown || (k.Kind == keyRune && k.Rune == 'j'):
		return 1
	case k.Kind == keyUp || (k.Kind == keyRune && k.Rune == 'k'):
		return -1
	case k.Kind == keyPgDn:
		return page
	case k.Kind == keyPgUp:
		return -page
	}
	return 0
}

// tuiMoveCursor clamps cursor+step into [0, n).
func tuiMoveCursor(cursor, step, n int) int {
	if n <= 0 {
		return 0
	}
	return min(max(cursor+step, 0), n-1)
}

// tuiWindow is the first visible row that keeps cursor on screen.
func tuiWindow(cursor, n, rows int) int {
	if rows <= 0 || cursor < rows {
		return 0
	}
	return max(min(cursor-rows+1, n-rows), 0)
}

// tuiScrollRows returns the height rows that keep row sel visible.
func tuiScrollRows(rows []tuiRow, sel, height int) []tuiRow {
	start := tuiWindow(sel, len(rows), height)
	end := min(start+height, len(rows))
	return rows[start:end]
}

// tuiViewFrame lays a view's content into the body: lines windowed so row sel
// stays visible, one status row at the bottom, and the whole body boxed under
// title when it is big enough. A sel outside lines highlights nothing.
func tuiViewFrame(cols, rows int, title string, lines []tuiRow, sel int, status tuiRow) []tuiRow {
	boxed := rows >= 3 && cols >= 6
	w, h := cols, rows
	if boxed {
		w, h = cols-4, rows-2
	}
	if h < 1 || w < 1 {
		return fitRows(nil, rows)
	}
	content := max(h-1, 0)
	marked := make([]tuiRow, len(lines))
	copy(marked, lines)
	if sel >= 0 && sel < len(marked) {
		marked[sel] = marked[sel].fit(w).padTo(w, tuiInk{}).withBG(tuiColSelect)
	}
	out := fitRows(append([]tuiRow(nil), tuiScrollRows(marked, max(sel, 0), content)...), content)
	out = append(out, fitRows([]tuiRow{status}, 1)...)
	if !boxed {
		return fitRows(out, h)
	}
	return tuiBox(cols, rows, tuiText(title, tuiInk{fg: tuiColAccent, bold: true}), nil, out)
}

// tuiMeter draws a bar width cells wide filled to pct, with label after it. The
// fill follows the desktop's limit thresholds: ok under 70, warn under 90, err above.
func tuiMeter(width int, pct float64, label string) tuiRow {
	pct = math.Max(0, math.Min(100, pct))
	barW := max(width-len([]rune(label))-1, 4)
	filled := int(math.Round(pct / 100 * float64(barW)))
	return tuiText(strings.Repeat("█", filled), tuiInk{fg: tuiPctColor(pct)}).
		join(tuiText(strings.Repeat("░", barW-filled), tuiInk{fg: tuiColBorder}),
			tuiText(" "+label, tuiInk{fg: tuiColDim}))
}

// tuiPctColor is the fill color for a usage percentage.
func tuiPctColor(pct float64) tuiRGB {
	switch {
	case pct >= 90:
		return tuiColErr
	case pct >= 70:
		return tuiColWarn
	}
	return tuiColOK
}

// tuiHeading is a section title inside a view.
func tuiHeading(s string) tuiRow {
	return tuiText(s, tuiInk{fg: tuiColAccent2, bold: true})
}

// tuiKV is a dim label and a value on one line.
func tuiKV(label, value string) tuiRow {
	return tuiText(label+" ", tuiInk{fg: tuiColDim}).join(tuiText(value, tuiInk{fg: tuiColText}))
}

// tuiFaint is a dim note row.
func tuiFaint(s string) tuiRow {
	return tuiText(s, tuiInk{fg: tuiColFaint})
}

// focusRow is the thread a thread-scoped view reads: the open thread, else the
// list cursor's row. It is false when there is no thread to read.
func (m *tuiModel) focusRow() (psRow, bool) {
	if m.screen == tuiScreenThread {
		return psRow{thread: m.thread, project: m.project}, m.thread.ID != ""
	}
	return m.selected()
}

// wrapRows wraps text to width, one dim or ink row per wrapped line. The text
// is sanitized first: forge and agent bodies are not trusted to be printable,
// and a raw ESC in them would break the frame.
func wrapRows(text string, width int, ink tuiInk) []tuiRow {
	var out []tuiRow
	for _, line := range strings.Split(text, "\n") {
		for _, part := range tuiWrap(tuiSanitize(line), max(width, 1)) {
			out = append(out, tuiText(part, ink))
		}
	}
	return out
}
