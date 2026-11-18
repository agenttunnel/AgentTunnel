package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Scrolling past the oldest row held asks the daemon for the page before it,
// and the page lands above the transcript without moving the view.
func TestTUIScrollingPastTheTopLoadsOlderOutput(t *testing.T) {
	at := func(sec int) time.Time { return time.Date(2024, 1, 1, 10, 0, sec, 0, time.Local) }
	m := newTuiModel(tuiTestClock())
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	var tail []contracts.LogEntry
	for i := 30; i < 40; i++ {
		tail = append(tail, contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: fmt.Sprint("cmd ", i), At: at(i)})
	}
	m.appendBacklog(tail, nil)
	render(m, 60, 10)
	m.scrollUp(1000) // past the top: clamps, and asks for more
	jobs := m.takeJobs()
	if len(jobs) != 1 || !m.older.loading {
		t.Fatalf("scrolling past the top queued %d jobs (loading=%v), want one", len(jobs), m.older.loading)
	}
	m.scrollUp(1000)
	if again := m.takeJobs(); len(again) != 0 {
		t.Fatal("a second page was requested while one was on its way")
	}

	var asked map[string]any
	c := &tuiFakeCaller{replies: map[string]string{}}
	older := []contracts.LogEntry{
		{AgentID: "lead", Kind: contracts.LogTool, Text: "cmd 28", At: at(28)},
		{AgentID: "lead", Kind: contracts.LogTool, Text: "cmd 29", At: at(29)},
	}
	body, _ := json.Marshal(older)
	c.replies["thread.logs"] = string(body)
	fold := jobs[0](t.Context(), c)
	if !c.sent("thread.logs", `"before"`) {
		t.Fatalf("the page was not asked for with before; calls: %v (%v)", c.calls, asked)
	}
	scroll := m.scroll
	fold(m)
	if len(m.log) != 1 || m.log[0].text != "cmd 28\ncmd 29\ncmd 30\ncmd 31\ncmd 32\ncmd 33\ncmd 34\ncmd 35\ncmd 36\ncmd 37\ncmd 38\ncmd 39" {
		// Tool lines from one speaker are separate blocks only when the kind
		// changes; same-kind tool output stays its own blocks.
		if len(m.log) < 11 {
			t.Fatalf("the older page was not placed above the transcript: %d blocks", len(m.log))
		}
	}
	if m.older.loading || !m.older.before.Equal(at(28)) {
		t.Fatalf("paging state after a page: loading=%v before=%v", m.older.loading, m.older.before)
	}
	if m.scroll != scroll {
		t.Fatalf("older output moved the view: scroll %d -> %d", scroll, m.scroll)
	}
	if first := m.log[0].text; first != "cmd 28" {
		t.Fatalf("the oldest block is %q, want cmd 28", first)
	}
}

// A page for a thread that is no longer on screen is dropped.
func TestTUIOlderPageForAnotherThreadIsDropped(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	m.older.before = time.Unix(100, 0)
	m.loadOlder()
	jobs := m.takeJobs()
	m.closeThread()
	m.openThread(threadListRow{ID: "th_2"}, "demo", nil)
	c := &tuiFakeCaller{replies: map[string]string{"thread.logs": `[{"agentId":"lead","kind":"text","text":"stale"}]`}}
	jobs[0](t.Context(), c)(m)
	if len(m.log) != 0 {
		t.Fatalf("a page for th_1 landed in th_2: %+v", m.log)
	}
}

// "@" opens a search of the thread's files; Tab completes the highlighted path,
// which then rides with the line as an attachment.
func TestTUIMentionPicksAFileAndSendsItAsAttachment(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	var tracker *turnTracker
	var sent []string
	press := func(k tuiKey) { tuiThreadKey(m, &tracker, k, 20, func(l string) { sent = append(sent, l) }) }
	for _, r := range "read @ma" {
		press(tuiKey{Kind: keyRune, Rune: r})
	}
	jobs := m.takeJobs()
	if len(jobs) == 0 {
		t.Fatal("typing an @ word did not search")
	}
	c := &tuiFakeCaller{replies: map[string]string{
		"files.search": `{"matches":[{"path":"cmd/main.go","dir":false},{"path":"cmd","dir":true}]}`,
	}}
	// Only the answer to the newest question may fill the list.
	for _, job := range jobs {
		job(t.Context(), c)(m)
	}
	if got := m.mentionMatches(); len(got) != 2 {
		t.Fatalf("mention matches = %v", got)
	}
	if frame := tuiPlain(render(m, 70, 18)); !strings.Contains(frame, "▸ cmd/main.go") || !strings.Contains(frame, "cmd/") {
		t.Fatalf("mention list not drawn:\n%s", frame)
	}
	press(tuiKey{Kind: keyDown})
	press(tuiKey{Kind: keyUp})
	press(tuiKey{Kind: keyTab})
	if got := m.input.string(); got != "read @cmd/main.go " {
		t.Fatalf("completed to %q", got)
	}
	if got := m.takeAttachments(m.input.string()); len(got) != 1 || got[0] != "cmd/main.go" {
		t.Fatalf("attachments = %v", got)
	}
	// A path that was edited out of the line no longer rides along.
	m.attached = map[string]map[string]bool{"th_1": {"gone.go": true, "cmd/main.go": true}}
	if got := m.takeAttachments("only @cmd/main.go now"); len(got) != 1 || got[0] != "cmd/main.go" {
		t.Fatalf("attachments for an edited line = %v", got)
	}
	if len(sent) != 0 {
		t.Fatalf("completing a mention sent %q", sent)
	}
}

// Esc closes an open mention list before it leaves the thread.
func TestTUIMentionEscClosesTheListFirst(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	var tracker *turnTracker
	press := func(k tuiKey) { tuiThreadKey(m, &tracker, k, 20, func(string) {}) }
	for _, r := range "@x" {
		press(tuiKey{Kind: keyRune, Rune: r})
	}
	m.mention = tuiMention{query: "x", loaded: true, matches: []tuiFileMatch{{Path: "x.go"}}}
	press(tuiKey{Kind: keyEsc})
	if m.screen != tuiScreenThread || len(m.mentionMatches()) != 0 {
		t.Fatalf("first Esc: screen=%v matches=%d, want the list closed and the thread open", m.screen, len(m.mentionMatches()))
	}
	press(tuiKey{Kind: keyEsc})
	if m.screen != tuiScreenList {
		t.Fatal("a second Esc did not leave the thread")
	}
}

func TestTUILinksAreWrappedInOSC8OnlyWhenSafe(t *testing.T) {
	theme := tuiTheme{depth: tuiDepthTrueColor}
	out := tuiLink("https://example.com/a", "https://example.com/a", tuiInk{}).render(theme)
	if !strings.Contains(out, "\x1b]8;;https://example.com/a\x1b\\") || !strings.HasSuffix(out, "\x1b]8;;\x1b\\") {
		t.Fatalf("link not wrapped: %q", out)
	}
	if got := tuiLink("x", "https://example.com/\x07evil", tuiInk{}).render(theme); strings.Contains(got, "\x1b]8") || strings.Contains(got, "\x07") {
		t.Fatalf("an unsafe url became a link: %q", got)
	}
	if got := tuiLink("x", "javascript:alert(1)", tuiInk{}).render(theme); strings.Contains(got, "\x1b]8") {
		t.Fatalf("a non-http url became a link: %q", got)
	}
	if got := tuiLink("https://example.com/a", "https://example.com/a", tuiInk{}).render(tuiTheme{}); got != "https://example.com/a" {
		t.Fatalf("plain output carries escapes: %q", got)
	}
}

// URLs in agent prose are clickable, without the punctuation around them, and
// a link wrapped over two rows is a link on both.
func TestTUIMarkdownAutolinksURLs(t *testing.T) {
	rows := tuiMarkdownRows("see https://example.com/docs/page, then (https://example.com/x).", 80, tuiInk{})
	var links []string
	for _, span := range rows[0] {
		if span.link != "" {
			links = append(links, span.link)
		}
	}
	if fmt.Sprint(links) != "[https://example.com/docs/page https://example.com/x]" {
		t.Fatalf("links = %v", links)
	}
	wrapped := tuiMarkdownRows("go to https://example.com/a/very/long/path/that/must/wrap/somewhere/ok", 24, tuiInk{})
	n := 0
	for _, row := range wrapped {
		for _, span := range row {
			if span.link != "" {
				n++
			}
		}
	}
	if n < 2 {
		t.Fatalf("a wrapped URL kept its link on %d row(s), want every row of it", n)
	}
	if rows := tuiMarkdownRows("not a link: http:// or snake_https://x", 80, tuiInk{}); len(rows) != 1 {
		t.Fatal("unexpected rows")
	} else {
		for _, span := range rows[0] {
			if span.link != "" {
				t.Fatalf("%q became a link", span.text)
			}
		}
	}
}

// Started inside a registered project, the TUI opens a new thread there, and
// takes it back on the way out if nothing was done in it.
func TestTUISessionOpensAThreadForTheCurrentProject(t *testing.T) {
	s := tuiServer("th_1", "idle")
	dir := t.TempDir()
	s.results["project.list"] = []projectsRow{{ID: "prj_1", Name: "here", Path: dir}}
	s.results["thread.create"] = map[string]any{"threadId": "th_new"}
	s.results["thread.list"] = []threadListRow{
		{ID: "th_1", Title: "old job", ProjectID: "prj_1", UpdatedAt: time.Now()},
		{ID: "th_new", Title: "New thread", ProjectID: "prj_1", UpdatedAt: time.Now()},
	}
	s.results["thread.get"] = map[string]any{"version": 0}
	s.results["thread.delete"] = map[string]any{"ok": true}
	run := startTuiSession(t, s, "", newFakeTuiTerm(100, 30), nil, dir)
	run.term.waitNextDraw(t, containsLine("New thread  here  th_new"))
	var created map[string]string
	if err := json.Unmarshal(s.waitCall(t, "thread.create").params, &created); err != nil || created["projectId"] != "prj_1" {
		t.Fatalf("thread.create params = %v (%v)", created, err)
	}
	if tuiCallCount(s, "thread.markSeen") != 0 {
		t.Fatal("opening the fresh thread marked it read, which would have saved it")
	}
	// Leaving the session discards the unused thread; the cleanup waits for
	// the session to end.
	run.keys <- tuiKey{Kind: keyCtrlC}
	var deleted map[string]any
	if err := json.Unmarshal(s.waitCall(t, "thread.delete").params, &deleted); err != nil || deleted["threadId"] != "th_new" {
		t.Fatalf("thread.delete params = %v (%v)", deleted, err)
	}
}

// Frames the client had to discard are output the transcript never saw, so
// the open thread is read again.
func TestTUISessionResyncsAfterDroppedFrames(t *testing.T) {
	s := tuiServer("th_1", "running")
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))
	before := tuiCallCount(s, "thread.subscribe")
	s.mu.Lock()
	s.results["thread.logs"] = []contracts.LogEntry{
		{AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "backlog line\nmissed while behind"},
	}
	s.mu.Unlock()
	run.c.dropped.Add(1) // as if the read loop had discarded a frame
	waitMoreCalls(t, s, "thread.subscribe", before)
	run.term.waitNextDraw(t, containsLine("missed while behind"))
}
