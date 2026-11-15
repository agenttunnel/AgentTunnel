package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Only known thread commands are slash commands; anything else is steering text.
func TestTUIThreadSlashCommandsResolveKnownNamesOnly(t *testing.T) {
	if _, arg, ok := tuiSlashCommand("/btw   is it fast?  "); !ok || arg != "is it fast?" {
		t.Fatalf("/btw resolved to ok=%v arg=%q", ok, arg)
	}
	for _, line := range []string{"plain steering", "/nosuch thing", "/new", "/"} {
		if _, _, ok := tuiSlashCommand(line); ok {
			t.Fatalf("%q resolved as a thread command", line)
		}
	}
}

// The palette lists each command only on the screen it runs on.
func TestTUIThreadPaletteShowsCommandsOnTheirScreen(t *testing.T) {
	m := newTuiModel(time.Now())
	titles := func() string {
		var b strings.Builder
		for _, a := range m.allActions() {
			b.WriteString(a.Title + "\n")
		}
		return b.String()
	}
	list := titles()
	if !strings.Contains(list, "Threads: new thread") || strings.Contains(list, "Thread: stop turn") {
		t.Fatalf("list palette wrong:\n%s", list)
	}
	m.screen = tuiScreenThread
	m.thread = threadListRow{ID: "th_1"}
	thread := titles()
	if !strings.Contains(thread, "Thread: stop turn") || strings.Contains(thread, "Threads: new thread") {
		t.Fatalf("thread palette wrong:\n%s", thread)
	}
}

// A prompt edits text; a picker moves a cursor. Enter submits and closes.
func TestTUIThreadDialogPromptAndPickerSubmit(t *testing.T) {
	m := newTuiModel(time.Now())
	var got string
	m.openPrompt("rename", "ab", func(_ *tuiModel, v string) { got = v })
	tuiDialogKey(m, tuiKey{Kind: keyBackspace})
	tuiDialogKey(m, tuiKey{Kind: keyRune, Rune: 'c'})
	tuiDialogKey(m, tuiKey{Kind: keyEnter})
	if got != "ac" || m.dialog != nil {
		t.Fatalf("prompt submitted %q, dialog open=%v", got, m.dialog != nil)
	}

	m.openPicker("pick", []tuiPickItem{{"one", "1"}, {"two", "2"}, {"three", "3"}}, func(_ *tuiModel, v string) { got = v })
	tuiDialogKey(m, tuiKey{Kind: keyDown})
	tuiDialogKey(m, tuiKey{Kind: keyDown})
	tuiDialogKey(m, tuiKey{Kind: keyDown}) // stops at the last row
	tuiDialogKey(m, tuiKey{Kind: keyEnter})
	if got != "3" {
		t.Fatalf("picker submitted %q, want 3", got)
	}
}

// An empty picker is a flash, never an empty dialog.
func TestTUIThreadEmptyPickerIsAFlash(t *testing.T) {
	m := newTuiModel(time.Now())
	m.openPicker("nothing", nil, func(*tuiModel, string) {})
	if m.dialog != nil || m.flash == "" {
		t.Fatalf("dialog=%v flash=%q", m.dialog, m.flash)
	}
}

// The pane follows the newest rows, scrolls back on Up, and Esc closes it.
func TestTUIPaneFollowsScrollsAndCloses(t *testing.T) {
	m := newTuiModel(time.Now())
	m.screen = tuiScreenThread
	m.thread = threadListRow{ID: "th_1"}
	var lines []tuiLogLine
	for i := 0; i < 30; i++ {
		lines = append(lines, tuiLogLine{text: fmt.Sprintf("line %d", i), kind: string(contracts.LogText)})
	}
	m.openPane("agent a1", lines, true)

	const cols, rows = 40, 6
	last := func() string {
		out := m.paneRows(cols, rows)
		return strings.TrimLeft(out[len(out)-1].plain(), " ")
	}
	if got := last(); !strings.HasPrefix(got, "line 29") {
		t.Fatalf("follow view ends at %q, want line 29", got)
	}
	tuiPaneKey(m, tuiKey{Kind: keyUp}, rows)
	if got := last(); strings.HasPrefix(got, "line 29") {
		t.Fatalf("Up did not scroll back: still %q", got)
	}
	tuiPaneKey(m, tuiKey{Kind: keyEsc}, rows)
	if m.pane != nil {
		t.Fatal("Esc did not close the pane")
	}
}

// A job's outcome lands in the thread log when that thread is open, and in the
// list's flash otherwise.
func TestTUIThreadJobOutcomeRoutesToItsThread(t *testing.T) {
	m := newTuiModel(time.Now())
	m.screen = tuiScreenThread
	m.thread = threadListRow{ID: "th_1"}
	tuiNote("th_1", "stop requested")(m)
	if len(m.log) == 0 || !strings.Contains(m.log[len(m.log)-1].text, "stop requested") {
		t.Fatalf("log = %+v, want the note on th_1", m.log)
	}
	tuiNote("th_2", "elsewhere")(m)
	if m.flash != "elsewhere" {
		t.Fatalf("flash = %q, want the note for a thread not on screen", m.flash)
	}
}

// Composer slash commands reach the daemon: /rename and /stop each make their RPC.
func TestTUISessionSlashCommandsReachTheDaemon(t *testing.T) {
	s := tuiServer("th_1", "running")
	s.results["thread.rename"] = map[string]any{"ok": true}
	s.results["thread.markSeen"] = map[string]any{"ok": true}
	c := dialScripted(t, s)
	term := newFakeTuiTerm(100, 24)
	keys := make(chan tuiKey, 32)
	m := newTuiModel(time.Now())

	done := make(chan error, 1)
	go func() {
		done <- tuiSession(context.Background(), c, m, "", keys, term)
	}()
	term.waitNextDraw(t, containsLine("the tui job"))
	keys <- tuiKey{Kind: keyEnter}
	term.waitNextDraw(t, containsLine("backlog line"))

	typeLine(keys, "/rename Fresh Title")
	term.waitNextDraw(t, containsLine("renamed to Fresh Title"))
	rename := s.waitCall(t, "thread.rename")
	var params struct {
		ThreadID string `json:"threadId"`
		Title    string `json:"title"`
	}
	if err := json.Unmarshal(rename.params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "th_1" || params.Title != "Fresh Title" {
		t.Fatalf("rename params = %+v", params)
	}

	typeLine(keys, "/stop")
	term.waitNextDraw(t, containsLine("stop requested"))

	keys <- tuiKey{Kind: keyCtrlC}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tuiSession: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctrl-c did not leave the session")
	}
}

// Palette > New thread > project picker creates a draft in that project and
// opens it. The new thread joins the scripted list before the open, the way the
// daemon lists it after the create.
func TestTUISessionNewThreadFromThePalette(t *testing.T) {
	s := tuiServer("th_1", "running")
	s.results["thread.create"] = map[string]any{"threadId": "th_2"}
	s.results["thread.markSeen"] = map[string]any{"ok": true}
	c := dialScripted(t, s)
	term := newFakeTuiTerm(100, 24)
	keys := make(chan tuiKey, 64)
	m := newTuiModel(time.Now())

	done := make(chan error, 1)
	go func() {
		done <- tuiSession(context.Background(), c, m, "", keys, term)
	}()
	term.waitNextDraw(t, containsLine("the tui job"))

	keys <- tuiKey{Kind: keyCtrlK}
	term.waitNextDraw(t, containsLine("Commands"))
	for _, r := range "new thread" {
		keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	term.waitNextDraw(t, containsLine("Threads: new thread"))
	keys <- tuiKey{Kind: keyEnter}
	term.waitNextDraw(t, containsLine("New project"))

	// The first row is the existing project: Enter picks it.
	s.mu.Lock()
	s.results["thread.list"] = []threadListRow{
		{ID: "th_1", Title: "the tui job", ProjectID: "prj_1", Agents: 1, RunningAgents: 1, UpdatedAt: time.Now()},
		{ID: "th_2", ProjectID: "prj_1", UpdatedAt: time.Now()},
	}
	s.mu.Unlock()
	keys <- tuiKey{Kind: keyEnter}

	create := s.waitCall(t, "thread.create")
	var params map[string]any
	if err := json.Unmarshal(create.params, &params); err != nil {
		t.Fatal(err)
	}
	if params["projectId"] != "prj_1" || params["draft"] != true {
		t.Fatalf("thread.create params = %v", params)
	}
	term.waitNextDraw(t, containsLine("backlog line"))

	keys <- tuiKey{Kind: keyCtrlC}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tuiSession: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctrl-c did not leave the session")
	}
}
