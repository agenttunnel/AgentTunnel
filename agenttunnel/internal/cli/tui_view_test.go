package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// tuiFakeCaller answers RPCs from a method table and records every call, so the
// views can be driven through their real Load and Key hooks with no daemon.
type tuiFakeCaller struct {
	mu      sync.Mutex
	replies map[string]string
	calls   []tuiFakeCall
}

type tuiFakeCall struct {
	method string
	params string
}

func (f *tuiFakeCaller) Call(_ context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, tuiFakeCall{method: method, params: string(raw)})
	reply, ok := f.replies[method]
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("no reply scripted for %s", method)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(reply), out)
}

// sent reports whether method was called with params containing every fragment.
func (f *tuiFakeCaller) sent(method string, fragments ...string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.method != method {
			continue
		}
		match := true
		for _, frag := range fragments {
			if !strings.Contains(c.params, frag) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// tuiViewReplies scripts every RPC the built-in views read, so one caller can
// load all of them.
func tuiViewReplies() map[string]string {
	return map[string]string{
		"settings.get":        `{"workdir":"/w","autoMode":false,"roles":[{"id":"executor","name":"Executor","harness":"claude","model":"m1","enabled":true}],"lead":{"harness":"","model":""},"staleAfterMinutes":10}`,
		"harness.list":        `{"all":["claude","codex"],"available":["claude"],"enabled":["claude"],"models":{"claude":["m1"]}}`,
		"usage.get":           `{"orchestrator":{"inputTokens":100,"outputTokens":50,"costUsd":0.5},"subagents":{"inputTokens":10,"outputTokens":5,"costUsd":0.1},"byAgent":[{"agentId":"agent-1","role":"executor","usage":{"inputTokens":10,"outputTokens":5}}]}`,
		"usage.limits":        `[{"harness":"claude","available":true,"windows":[{"id":"5h","label":"5-hour","usedPercent":95,"resetsAt":"2026-11-14T20:00:00Z"}]}]`,
		"stats.harnesses":     `[{"harness":"claude","threads":1,"agents":2,"subagents":1,"usage":{"inputTokens":10}}]`,
		"stats.models":        `[{"harness":"claude","model":"m1","subagents":1,"usage":{"inputTokens":10}}]`,
		"review.diff":         `{"files":[{"path":"a.go","additions":1,"deletions":1,"lines":[{"old":1,"kind":"delete","text":"x := 0"},{"new":1,"kind":"add","text":"x := 1"}]}],"empty":false}`,
		"thread.get":          `{"exists":true,"reviewThreads":[{"id":"r1","file":"a.go","line":1,"side":"new","body":"nit","status":"open"}]}`,
		"pr.status":           `{"status":"ready","provider":"github","branch":"feat","request":{"number":7,"title":"Add thing","state":"open","checks":"passing","review":"approved","url":"https://x/7","comments":[{"author":"ana","body":"lgtm"}]}}`,
		"pr.list":             `{"status":"ready","requests":[{"number":8,"title":"Other","state":"open"}]}`,
		"files.tree":          `{"entries":[{"name":"src","path":"src","dir":true,"children":[{"name":"a.go","path":"src/a.go","dir":false}]},{"name":"README.md","path":"README.md","dir":false}],"status":{"README.md":"modified"}}`,
		"files.read":          `{"path":"src/a.go","size":11,"text":"package a\nvar x"}`,
		"files.search":        `{"matches":[{"path":"src/a.go","dir":false}]}`,
		"worktree.list":       `[{"path":"/p/wt","branch":"b","base":"main","exists":true,"dirty":true,"ahead":1,"behind":0,"threadIds":["t1"]}]`,
		"project.list":        `[{"id":"p1","name":"proj","path":"/p","threads":1}]`,
		"issues.list":         `{"status":"ready","provider":"github","issues":[{"iid":1,"title":"Bug","description":"it breaks","webUrl":"https://x/1"}]}`,
		"master.history.list": `[{"id":"c1","title":"Plan","messages":2}]`,
		"master.history.open": `{"id":"c1","title":"Plan","messages":[{"role":"user","text":"hi"},{"role":"master","text":"hello"}]}`,
		"master.send":         `{"reply":"hello"}`,
		"remote.status":       `{"items":[{"id":"phone","label":"Phone","state":"connected"},{"id":"whatsapp","label":"WhatsApp","state":"off","controlSupported":true}]}`,
		"relay.status":        `{"enabled":false}`,
		"update.check":        `{"current":"1.0.0","latest":"1.1.0","enabled":true,"available":true}`,
	}
}

// tuiViewModel is a plain-text model with one thread in the list, so views that
// read the focused thread have one to read.
func tuiViewModel() *tuiModel {
	m := newTuiModel(time.Now())
	m.theme = tuiTheme{}
	m.setList([]psRow{{thread: threadListRow{ID: "t1", ProjectID: "p1", Title: "Thread one"}, project: "proj"}})
	return m
}

// tuiViewOf returns the registered view id, failing the test if it is missing.
func tuiViewOf(t *testing.T, id string) tuiView {
	t.Helper()
	v, ok := tuiViewByID(id)
	if !ok {
		t.Fatalf("view %q is not registered", id)
	}
	return v
}

// tuiWaitIdle blocks until a background run has finished, without a sleep.
func tuiWaitIdle(t *testing.T, b *tuiBase) {
	t.Helper()
	b.mu.Lock()
	ch := b.idle
	b.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// tuiDrainJobs runs the model's queued jobs the way the session loop would:
// each runs off the loop and its fold applies back on it. It makes a
// Load-queued fetch deterministic in tests, with no sleeps.
func tuiDrainJobs(t *testing.T, m *tuiModel, c cliCaller) {
	t.Helper()
	for _, job := range m.takeJobs() {
		if apply := job(context.Background(), c); apply != nil {
			apply(m)
		}
	}
}

// tuiKeys feeds a sequence of keys through a view's Key hook, one by one.
func tuiKeys(t *testing.T, m *tuiModel, id string, keys ...tuiKey) {
	t.Helper()
	v := tuiViewOf(t, id)
	for _, k := range keys {
		v.Key(m, k)
	}
}

func tuiRunes(s string) []tuiKey {
	var out []tuiKey
	for _, r := range s {
		out = append(out, tuiKey{Kind: keyRune, Rune: r})
	}
	return out
}

var tuiEnter = tuiKey{Kind: keyEnter}

// tuiGateCaller stands in for a daemon that does not answer until gate opens.
// Each call announces itself on entered before it blocks, so a call made on the
// loop shows up at once, with no timeout involved.
type tuiGateCaller struct {
	*tuiFakeCaller
	entered chan string
	gate    chan struct{}
	once    sync.Once
}

func newTuiGateCaller(t *testing.T) *tuiGateCaller {
	t.Helper()
	g := &tuiGateCaller{
		tuiFakeCaller: &tuiFakeCaller{replies: tuiViewReplies()},
		entered:       make(chan string, 64),
		gate:          make(chan struct{}),
	}
	t.Cleanup(g.open)
	return g
}

func (g *tuiGateCaller) Call(ctx context.Context, method string, params, out any) error {
	g.entered <- method
	<-g.gate
	return g.tuiFakeCaller.Call(ctx, method, params, out)
}

// open lets every blocked and future call through.
func (g *tuiGateCaller) open() { g.once.Do(func() { close(g.gate) }) }

// tuiOffLoop runs fn and fails the test if fn reaches the daemon before it
// returns. A synchronous call blocks on the gate, so fn cannot return and the
// select takes the announcement instead; nothing waits on a timeout.
func tuiOffLoop(t *testing.T, g *tuiGateCaller, fn func()) {
	t.Helper()
	returned := make(chan struct{})
	go func() {
		fn()
		close(returned)
	}()
	select {
	case <-returned:
	case method := <-g.entered:
		t.Fatalf("%s ran on the loop: the handler waited on the daemon", method)
	}
}

func TestTUIViewsRegisterEveryAppView(t *testing.T) {
	want := []string{"settings", "usage", "review", "pr", "files", "worktrees", "projects", "issues", "master", "remote"}
	for _, id := range want {
		v := tuiViewOf(t, id)
		if v.Title == "" {
			t.Errorf("view %s has no title", id)
		}
		if v.Render == nil || v.Load == nil || len(v.Hints) == 0 {
			t.Errorf("view %s is missing Render, Load, or Hints", id)
		}
	}
}

// TestTUIViewsRenderEverySizeAndTheme loads every built-in view and paints it at
// sizes from one cell up. Each frame must be exactly the requested height, and a
// plain theme must emit no escape codes, so NO_COLOR and dumb terminals stay clean.
func TestTUIViewsRenderEverySizeAndTheme(t *testing.T) {
	sizes := [][2]int{{1, 1}, {2, 2}, {3, 3}, {4, 2}, {6, 3}, {10, 5}, {40, 12}, {100, 30}}
	for _, v := range tuiViewRegistry {
		m := tuiViewModel()
		c := &tuiFakeCaller{replies: tuiViewReplies()}
		m.view = v.ID
		v.Load(context.Background(), c, m)
		tuiDrainJobs(t, m, c)
		for _, sz := range sizes {
			cols, rows := sz[0], sz[1]
			out := render(m, cols, rows)
			if got := len(strings.Split(out, "\n")); got != rows {
				t.Errorf("view %s at %dx%d: %d lines, want %d", v.ID, cols, rows, got, rows)
			}
			if strings.Contains(out, "\x1b") {
				t.Errorf("view %s at %dx%d: plain theme emitted an escape code", v.ID, cols, rows)
			}
		}
	}
}

func TestTUIViewUsageShowsWindowMeters(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewUsage
	tuiViewOf(t, tuiViewUsage).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)

	out := render(m, 80, 30)
	for _, want := range []string{"5-hour", "95%", "claude/m1", "$0.6000"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage view missing %q in:\n%s", want, out)
		}
	}
	if tuiPctColor(95) != tuiColErr || tuiPctColor(75) != tuiColWarn || tuiPctColor(10) != tuiColOK {
		t.Errorf("meter thresholds are not ok<70<=warn<90<=err")
	}
	if got := tuiMeter(30, 50, "50%").width(); got != 30 {
		t.Errorf("meter width = %d, want 30", got)
	}
}

func TestTUIViewSettingsTogglesThroughSettingsSet(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewSettings
	tuiViewOf(t, tuiViewSettings).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)

	// The first switch is auto mode; Enter flips it on with one settings.set.
	tuiKeys(t, m, tuiViewSettings, tuiEnter)
	tuiWaitIdle(t, &tuiSettingsOf(m).tuiBase)
	if !c.sent("settings.set", `"autoMode":true`) {
		t.Fatalf("enter on auto mode did not send settings.set autoMode=true; calls: %+v", c.calls)
	}
}

func TestTUIViewSettingsDownArrowMovesToHarnessRows(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewSettings
	tuiViewOf(t, tuiViewSettings).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)

	// auto, experimental, the executor role, then claude: three steps down.
	tuiKeys(t, m, tuiViewSettings, tuiKey{Kind: keyDown}, tuiKey{Kind: keyDown}, tuiKey{Kind: keyDown})
	tuiKeys(t, m, tuiViewSettings, tuiEnter)
	tuiWaitIdle(t, &tuiSettingsOf(m).tuiBase)
	if !c.sent("settings.set", `"harness":{"enabled":false,"id":"claude"}`) {
		t.Fatalf("enter on the claude harness did not disable it; calls: %+v", c.calls)
	}
}

func TestTUIViewPRMergeNeedsASecondPress(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewPR
	v := tuiViewOf(t, tuiViewPR)
	v.Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	st := tuiPROf(m)

	tuiKeys(t, m, tuiViewPR, tuiKey{Kind: keyRune, Rune: 'M'})
	if c.sent("pr.merge") {
		t.Fatal("the first M merged; it should only arm the merge")
	}
	if st.status() == nil {
		t.Fatal("arming a merge should show its prompt in the status row")
	}

	// Any other key disarms: the next M arms again instead of merging.
	tuiKeys(t, m, tuiViewPR, tuiKey{Kind: keyDown}, tuiKey{Kind: keyRune, Rune: 'M'})
	if c.sent("pr.merge") {
		t.Fatal("a stray key left the merge armed")
	}

	tuiKeys(t, m, tuiViewPR, tuiKey{Kind: keyRune, Rune: 'M'})
	tuiWaitIdle(t, &st.tuiBase)
	if !c.sent("pr.merge", `"threadId":"t1"`) {
		t.Fatalf("the second M did not merge; calls: %+v", c.calls)
	}
}

func TestTUIViewMasterSendsIntoTheDurableConversation(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewMaster
	v := tuiViewOf(t, tuiViewMaster)
	v.Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	st := tuiMasterOf(m)

	tuiKeys(t, m, tuiViewMaster, tuiEnter) // open the first conversation
	tuiDrainJobs(t, m, c)
	if !st.inChat || st.chatID != "c1" {
		t.Fatalf("enter did not open conversation c1 (inChat=%v chat=%q)", st.inChat, st.chatID)
	}
	tuiKeys(t, m, tuiViewMaster, tuiRunes("ok")...)
	tuiKeys(t, m, tuiViewMaster, tuiEnter)
	tuiWaitIdle(t, &st.tuiBase)
	if !c.sent("master.send", `"text":"ok"`, `"threadId":"c1"`) {
		t.Fatalf("send did not reach master.send with the durable thread; calls: %+v", c.calls)
	}
}

func TestTUIViewMasterEscLeavesTheChat(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewMaster
	tuiViewOf(t, tuiViewMaster).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	tuiKeys(t, m, tuiViewMaster, tuiEnter)
	tuiDrainJobs(t, m, c)

	if !tuiViewOf(t, tuiViewMaster).Key(m, tuiKey{Kind: keyEsc}) {
		t.Fatal("esc in the chat should be consumed, not fall through to the thread list")
	}
	if tuiMasterOf(m).inChat {
		t.Fatal("esc did not return to the conversation list")
	}
}

func TestTUIViewFilesExpandPreviewAndSearch(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewFiles
	tuiViewOf(t, tuiViewFiles).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	st := tuiFilesOf(m)

	tuiKeys(t, m, tuiViewFiles, tuiEnter) // expand src
	if got := len(st.visible()); got != 3 {
		t.Fatalf("after expanding src the tree has %d rows, want 3 (src, a.go, README.md)", got)
	}
	tuiKeys(t, m, tuiViewFiles, tuiKey{Kind: keyDown}, tuiEnter) // open src/a.go
	tuiDrainJobs(t, m, c)
	if !c.sent("files.read", `"path":"src/a.go"`) || st.preview == nil {
		t.Fatalf("enter on a file did not open its preview; calls: %+v", c.calls)
	}
	if !tuiViewOf(t, tuiViewFiles).Key(m, tuiKey{Kind: keyEsc}) || st.preview != nil {
		t.Fatal("esc should close the preview and stay on the files tab")
	}

	tuiKeys(t, m, tuiViewFiles, tuiRunes("/")...)
	tuiKeys(t, m, tuiViewFiles, tuiRunes("a.go")...)
	tuiKeys(t, m, tuiViewFiles, tuiEnter)
	tuiDrainJobs(t, m, c)
	if !c.sent("files.search", `"query":"a.go"`) || len(st.search) != 1 {
		t.Fatalf("search did not run; search=%v calls=%+v", st.search, c.calls)
	}
}

func TestTUIViewReviewCommentsTheDiffLineUnderTheCursor(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewReview
	tuiViewOf(t, tuiViewReview).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	st := tuiReviewOf(m)

	idx := -1
	for i, a := range st.anchors {
		if a != nil && a.side == "new" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("the diff has no added line to anchor a comment on")
	}
	st.cursor = idx
	tuiKeys(t, m, tuiViewReview, tuiRunes("c")...)
	tuiKeys(t, m, tuiViewReview, tuiRunes("nit")...)
	tuiKeys(t, m, tuiViewReview, tuiEnter)
	tuiWaitIdle(t, &st.tuiBase)
	if !c.sent("review.comment", `"file":"a.go"`, `"side":"new"`, `"body":"nit"`) {
		t.Fatalf("comment did not anchor to the added line; calls: %+v", c.calls)
	}
}

func TestTUIViewReviewSubmitNeedsASecondPress(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewReview
	tuiViewOf(t, tuiViewReview).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	st := tuiReviewOf(m)

	tuiKeys(t, m, tuiViewReview, tuiRunes("s")...)
	if c.sent("review.submit") {
		t.Fatal("the first s submitted the review")
	}
	tuiKeys(t, m, tuiViewReview, tuiRunes("s")...)
	tuiWaitIdle(t, &st.tuiBase)
	if !c.sent("review.submit", `"threadId":"t1"`) {
		t.Fatalf("the second s did not submit; calls: %+v", c.calls)
	}
}

func TestTUIViewRemoteNeverShowsThePairingQR(t *testing.T) {
	replies := tuiViewReplies()
	replies["remote.status"] = `{"items":[{"id":"phone","label":"Phone","state":"pairing","qr":"SECRET-PAIR-CODE"}]}`
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: replies}
	m.view = tuiViewRemote
	tuiViewOf(t, tuiViewRemote).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)

	if out := render(m, 80, 24); strings.Contains(out, "SECRET-PAIR-CODE") {
		t.Fatal("the pairing QR leaked into the remote view")
	}
}

func TestTUIViewUpdateApplyNeedsASecondPress(t *testing.T) {
	replies := tuiViewReplies()
	replies["update.apply"] = `{"current":"1.0.0","latest":"1.1.0","available":false,"installed":"1.1.0"}`
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: replies}
	m.view = tuiViewRemote
	tuiViewOf(t, tuiViewRemote).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	st := tuiRemoteOf(m)

	tuiKeys(t, m, tuiViewRemote, tuiRunes("A")...)
	if c.sent("update.apply") {
		t.Fatal("the first A installed the update")
	}
	tuiKeys(t, m, tuiViewRemote, tuiRunes("A")...)
	tuiWaitIdle(t, &st.tuiBase)
	if !c.sent("update.apply") {
		t.Fatalf("the second A did not apply; calls: %+v", c.calls)
	}
	if got := st.updateStatus().Installed; got != "1.1.0" {
		t.Fatalf("installed version = %q, want 1.1.0 after the apply", got)
	}
}

func TestTUIViewWorktreesRemoveNeedsASecondPress(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewWorktrees
	tuiViewOf(t, tuiViewWorktrees).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	st := tuiWorktreesOf(m)

	tuiKeys(t, m, tuiViewWorktrees, tuiRunes("x")...)
	if c.sent("worktree.remove") {
		t.Fatal("the first x removed the worktree")
	}
	tuiKeys(t, m, tuiViewWorktrees, tuiRunes("x")...)
	tuiWaitIdle(t, &st.tuiBase)
	if !c.sent("worktree.remove", `"path":"/p/wt"`, `"force":false`) {
		t.Fatalf("the second x did not remove; calls: %+v", c.calls)
	}
}

func TestTUIViewProjectsAddDerivesTheNameFromThePath(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewProjects
	tuiViewOf(t, tuiViewProjects).Load(context.Background(), c, m)
	st := tuiProjectsOf(m)

	tuiKeys(t, m, tuiViewProjects, tuiRunes("a")...)
	tuiKeys(t, m, tuiViewProjects, tuiRunes("/src/widget")...)
	tuiKeys(t, m, tuiViewProjects, tuiEnter)
	tuiWaitIdle(t, &st.tuiBase)
	if !c.sent("project.create", `"name":"widget"`, `"path":"/src/widget"`) {
		t.Fatalf("add did not create the project; calls: %+v", c.calls)
	}
}

func TestTUIViewIssuesDetailsToggle(t *testing.T) {
	m := tuiViewModel()
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewIssues
	tuiViewOf(t, tuiViewIssues).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)

	if out := render(m, 80, 24); strings.Contains(out, "it breaks") {
		t.Fatal("the description should be closed until enter opens it")
	}
	tuiKeys(t, m, tuiViewIssues, tuiEnter)
	if out := render(m, 80, 24); !strings.Contains(out, "it breaks") {
		t.Fatal("enter did not open the issue's description")
	}
}

// TestTUIViewReloadOnTheFocusedThreadChange checks that moving the list cursor
// to another thread reloads the focused views instead of showing the old thread.
func TestTUIViewReloadOnTheFocusedThreadChange(t *testing.T) {
	m := tuiViewModel()
	m.setList([]psRow{
		{thread: threadListRow{ID: "t1", ProjectID: "p1", Title: "one"}, project: "proj"},
		{thread: threadListRow{ID: "t2", ProjectID: "p1", Title: "two"}, project: "proj"},
	})
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewPR
	tuiViewOf(t, tuiViewPR).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	before := len(c.calls)

	m.cursor = 1
	tuiViewOf(t, tuiViewPR).Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	if len(c.calls) == before || !c.sent("pr.status", `"threadId":"t2"`) {
		t.Fatalf("moving to thread t2 did not reload its request; calls: %+v", c.calls)
	}
}

// TestTUIViewPRDropsStaleFoldWhenThreadFocusMoved pins the focus guard in the
// PR fetch's fold: a fetch queued for one thread must not land its data after
// the user focused another thread, or the view would render the old thread's
// request and the merge prompt would quote a stale request number. The fetch's
// RPCs run before the fold, so the guard is the only thing that can drop it.
func TestTUIViewPRDropsStaleFoldWhenThreadFocusMoved(t *testing.T) {
	m := tuiViewModel()
	m.setList([]psRow{
		{thread: threadListRow{ID: "t1", ProjectID: "p1", Title: "one"}, project: "proj"},
		{thread: threadListRow{ID: "t2", ProjectID: "p2", Title: "two"}, project: "other"},
	})
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewPR
	v := tuiViewOf(t, tuiViewPR)
	v.Load(context.Background(), c, m) // queues a fetch for t1
	jobs := m.takeJobs()
	if len(jobs) != 1 {
		t.Fatalf("the first load queued %d jobs, want 1", len(jobs))
	}

	// The user focuses t2 before the fold lands; the in-flight fetch keeps
	// loading=true, so no second one is queued behind it.
	m.cursor = 1
	v.Load(context.Background(), c, m)
	st := tuiPROf(m)
	if st.threadID != "t2" {
		t.Fatalf("focus did not move to t2: %q", st.threadID)
	}

	// The fetch for t1 runs and its fold applies with focus on t2.
	apply := jobs[0](context.Background(), c)
	if !c.sent("pr.status", `"threadId":"t1"`) {
		t.Fatalf("the in-flight fetch did not call pr.status for t1; calls: %+v", c.calls)
	}
	apply(m)

	if st.snap.Status != "" || st.snap.Request != nil || len(st.listing) != 0 {
		t.Fatalf("t1's fold landed after focus moved to t2: snap=%+v listing=%v", st.snap, st.listing)
	}
	if out := render(m, 80, 24); strings.Contains(out, "#7") || strings.Contains(out, "Add thing") {
		t.Fatalf("stale request rendered under t2's focus:\n%s", out)
	}

	// A fold for the focused thread still applies.
	v.Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	if st.snap.Status != "ready" || st.snap.Request == nil || st.snap.Request.Number != 7 {
		t.Fatalf("the matching fold for t2's fetch did not land: snap=%+v", st.snap)
	}
	if !c.sent("pr.status", `"threadId":"t2"`) {
		t.Fatalf("t2 never got its own fetch; calls: %+v", c.calls)
	}
}

// TestTUIViewIssuesDropsStaleFoldWhenProjectFocusMoved is the Issues twin: a
// slow issues.list resolved after the focus moved to another project must be
// dropped, so the old project's issues never render under the new title.
func TestTUIViewIssuesDropsStaleFoldWhenProjectFocusMoved(t *testing.T) {
	m := tuiViewModel()
	m.setList([]psRow{
		{thread: threadListRow{ID: "t1", ProjectID: "p1", Title: "one"}, project: "proj"},
		{thread: threadListRow{ID: "t2", ProjectID: "p2", Title: "two"}, project: "other"},
	})
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewIssues
	v := tuiViewOf(t, tuiViewIssues)
	v.Load(context.Background(), c, m) // queues a fetch for p1
	jobs := m.takeJobs()
	if len(jobs) != 1 {
		t.Fatalf("the first load queued %d jobs, want 1", len(jobs))
	}

	// The user focuses t2 (project p2, "other") before the fold lands.
	m.cursor = 1
	v.Load(context.Background(), c, m)
	st := tuiIssuesOf(m)
	if st.projectID != "p2" {
		t.Fatalf("focus did not move to p2: %q", st.projectID)
	}

	apply := jobs[0](context.Background(), c)
	if !c.sent("issues.list", `"projectId":"p1"`) {
		t.Fatalf("the in-flight fetch did not call issues.list for p1; calls: %+v", c.calls)
	}
	apply(m)

	if len(st.list.Issues) != 0 {
		t.Fatalf("p1's fold landed after focus moved to p2: %+v", st.list)
	}
	if out := render(m, 80, 24); strings.Contains(out, "Bug") {
		t.Fatalf("p1's issues rendered under p2's focus:\n%s", out)
	}

	// A fold for the focused project still applies.
	v.Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	if len(st.list.Issues) != 1 {
		t.Fatalf("the matching fold for p2's fetch did not land: %+v", st.list)
	}
	if !c.sent("issues.list", `"projectId":"p2"`) {
		t.Fatalf("p2 never got its own fetch; calls: %+v", c.calls)
	}
}

// TestTUIViewLoadNeverCallsTheDaemonOnTheLoop holds every registered view's
// daemon calls behind a gate. Load must return with a fetch queued and no call
// made from the loop.
func TestTUIViewLoadNeverCallsTheDaemonOnTheLoop(t *testing.T) {
	for _, v := range tuiViewRegistry {
		m := tuiViewModel()
		m.view = v.ID
		g := newTuiGateCaller(t)
		tuiOffLoop(t, g, func() { v.Load(context.Background(), g, m) })
		if jobs := m.takeJobs(); len(jobs) == 0 {
			t.Errorf("view %s: Load queued no background job", v.ID)
		}
	}
}

// TestTUIViewKeysNeverCallTheDaemonOnTheLoop presses the keys that used to call
// the daemon inline. Each key must return with its work queued, and the result
// must land once the daemon answers.
func TestTUIViewKeysNeverCallTheDaemonOnTheLoop(t *testing.T) {
	cases := []struct {
		name  string
		view  string
		setup []tuiKey // keys pressed before the probe, so its target exists
		probe tuiKey
		check func(t *testing.T, m *tuiModel, g *tuiGateCaller)
	}{
		{
			name:  "files preview",
			view:  tuiViewFiles,
			setup: []tuiKey{tuiEnter, {Kind: keyDown}}, // expand src, move onto src/a.go
			probe: tuiEnter,
			check: func(t *testing.T, m *tuiModel, g *tuiGateCaller) {
				if tuiFilesOf(m).preview == nil || !g.sent("files.read", `"path":"src/a.go"`) {
					t.Fatalf("the preview never landed; calls: %+v", g.calls)
				}
			},
		},
		{
			name:  "master conversation",
			view:  tuiViewMaster,
			probe: tuiEnter,
			check: func(t *testing.T, m *tuiModel, g *tuiGateCaller) {
				if st := tuiMasterOf(m); !st.inChat || st.chatID != "c1" {
					t.Fatalf("the transcript never opened (inChat=%v chat=%q)", st.inChat, st.chatID)
				}
			},
		},
		{
			name:  "settings toggle",
			view:  tuiViewSettings,
			probe: tuiEnter,
			check: func(t *testing.T, m *tuiModel, g *tuiGateCaller) {
				tuiWaitIdle(t, &tuiSettingsOf(m).tuiBase)
				if !g.sent("settings.set", `"autoMode":true`) {
					t.Fatalf("the toggle never reached settings.set; calls: %+v", g.calls)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tuiViewModel()
			m.view = tc.view
			plain := &tuiFakeCaller{replies: tuiViewReplies()}
			tuiViewOf(t, tc.view).Load(context.Background(), plain, m)
			tuiDrainJobs(t, m, plain)
			tuiKeys(t, m, tc.view, tc.setup...)
			tuiDrainJobs(t, m, plain)

			// The session binds its client on every Load, and run uses that binding,
			// so one more Load points the view at the gated caller before the probe.
			g := newTuiGateCaller(t)
			tuiViewOf(t, tc.view).Load(context.Background(), g, m)
			tuiOffLoop(t, g, func() { tuiKeys(t, m, tc.view, tc.probe) })

			g.open()
			tuiDrainJobs(t, m, g)
			tc.check(t, m, g)
		})
	}
}

// TestTUIViewFilesDropsStaleFoldWhenThreadFocusMoved is the files twin of the PR
// stale-fold test: a tree fetched for one thread must not land after the focus
// moved to another.
func TestTUIViewFilesDropsStaleFoldWhenThreadFocusMoved(t *testing.T) {
	m := tuiViewModel()
	m.setList([]psRow{
		{thread: threadListRow{ID: "t1", ProjectID: "p1", Title: "one"}, project: "proj"},
		{thread: threadListRow{ID: "t2", ProjectID: "p2", Title: "two"}, project: "other"},
	})
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewFiles
	v := tuiViewOf(t, tuiViewFiles)
	v.Load(context.Background(), c, m) // queues the tree for t1
	jobs := m.takeJobs()
	if len(jobs) != 1 {
		t.Fatalf("the first load queued %d jobs, want 1", len(jobs))
	}

	m.cursor = 1
	v.Load(context.Background(), c, m) // focus moves to t2 while t1's tree is in flight
	st := tuiFilesOf(m)
	if st.threadID != "t2" {
		t.Fatalf("focus did not move to t2: %q", st.threadID)
	}

	apply := jobs[0](context.Background(), c)
	if !c.sent("files.tree", `"threadId":"t1"`) {
		t.Fatalf("the in-flight fetch did not call files.tree for t1; calls: %+v", c.calls)
	}
	apply(m)
	if st.root != nil || st.marks != nil {
		t.Fatalf("t1's tree landed after focus moved to t2: %+v", st.root)
	}

	v.Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	if len(st.root) == 0 {
		t.Fatal("the matching fold for t2's tree did not land")
	}
	if !c.sent("files.tree", `"threadId":"t2"`) {
		t.Fatalf("t2 never got its own tree; calls: %+v", c.calls)
	}
}

// TestTUIViewReviewDropsStaleFoldWhenThreadFocusMoved is the review twin: a diff
// fetched for one thread must not land after the focus moved to another.
func TestTUIViewReviewDropsStaleFoldWhenThreadFocusMoved(t *testing.T) {
	m := tuiViewModel()
	m.setList([]psRow{
		{thread: threadListRow{ID: "t1", ProjectID: "p1", Title: "one"}, project: "proj"},
		{thread: threadListRow{ID: "t2", ProjectID: "p2", Title: "two"}, project: "other"},
	})
	c := &tuiFakeCaller{replies: tuiViewReplies()}
	m.view = tuiViewReview
	v := tuiViewOf(t, tuiViewReview)
	v.Load(context.Background(), c, m) // queues the diff for t1
	jobs := m.takeJobs()
	if len(jobs) != 1 {
		t.Fatalf("the first load queued %d jobs, want 1", len(jobs))
	}

	m.cursor = 1
	v.Load(context.Background(), c, m) // focus moves to t2 while t1's diff is in flight
	st := tuiReviewOf(m)
	if st.threadID != "t2" {
		t.Fatalf("focus did not move to t2: %q", st.threadID)
	}

	apply := jobs[0](context.Background(), c)
	if !c.sent("review.diff", `"threadId":"t1"`) {
		t.Fatalf("the in-flight fetch did not call review.diff for t1; calls: %+v", c.calls)
	}
	apply(m)
	if len(st.rows) != 0 || st.open != 0 {
		t.Fatalf("t1's diff landed after focus moved to t2: rows=%d open=%d", len(st.rows), st.open)
	}

	v.Load(context.Background(), c, m)
	tuiDrainJobs(t, m, c)
	if len(st.rows) == 0 || st.open != 1 {
		t.Fatalf("the matching fold for t2's diff did not land: rows=%d open=%d", len(st.rows), st.open)
	}
	if !c.sent("review.diff", `"threadId":"t2"`) {
		t.Fatalf("t2 never got its own diff; calls: %+v", c.calls)
	}
}

func TestTUIViewEditorTakesTextAndSubmits(t *testing.T) {
	var e tuiEditor
	e.open("name", "")
	e.key(tuiKey{Kind: keyRune, Rune: 'q'})
	if e.value() != "q" {
		t.Fatalf("editor did not take q as text: %q", e.value())
	}
	if !e.key(tuiEnter) || e.on {
		t.Fatal("enter should submit and close the editor")
	}
}

// The views whose reads shell out to forge CLIs or hit the network never run
// them on the session loop: their Load makes no synchronous call at all and
// only queues the fetch, whose fold lands on the loop.
func TestTUIViewLoadQueuesSlowFetches(t *testing.T) {
	views := []struct {
		id     string
		method string
		data   func(*testing.T, *tuiModel)
	}{
		{tuiViewPR, "pr.status", func(t *testing.T, m *tuiModel) {
			if got := tuiPROf(m).snap.Status; got != "ready" {
				t.Fatalf("the fold never landed the request snapshot: %q", got)
			}
		}},
		{tuiViewIssues, "issues.list", func(t *testing.T, m *tuiModel) {
			if got := len(tuiIssuesOf(m).list.Issues); got != 1 {
				t.Fatalf("the fold never landed the issue list: %d rows", got)
			}
		}},
	}
	for _, v := range views {
		m := tuiViewModel()
		m.view = v.id
		c := &tuiFakeCaller{replies: tuiViewReplies()}
		tuiViewOf(t, v.id).Load(context.Background(), c, m)
		if len(c.calls) != 0 {
			t.Fatalf("%s Load made %d synchronous calls: %+v", v.id, len(c.calls), c.calls)
		}
		tuiDrainJobs(t, m, c)
		if !c.sent(v.method) {
			t.Fatalf("%s fetch never ran %s; calls: %+v", v.id, v.method, c.calls)
		}
		v.data(t, m)
	}
}

// The update check is a network call to the release feed, so it runs as a
// background job on first open — never inline in Load.
func TestTUIViewUpdateCheckRunsInTheBackground(t *testing.T) {
	replies := tuiViewReplies()
	delete(replies, "remote.status")
	replies["remote.status"] = `{}`
	m := tuiViewModel()
	m.view = tuiViewRemote
	c := &tuiFakeCaller{replies: replies}
	tuiViewOf(t, tuiViewRemote).Load(context.Background(), c, m)
	for _, call := range c.calls {
		if call.method == "update.check" {
			t.Fatal("update.check ran synchronously inside Load")
		}
	}
	tuiDrainJobs(t, m, c)
	if !c.sent("update.check") {
		t.Fatalf("the first open never queued an update check; calls: %+v", c.calls)
	}
	if got := tuiRemoteOf(m).updateStatus().Current; got != "1.0.0" {
		t.Fatalf("checked version = %q, want 1.0.0", got)
	}

	// The refresh key runs the check again, on demand, in the background too.
	before := len(c.calls)
	m.view = tuiViewRemote
	tuiKeys(t, m, tuiViewRemote, tuiKey{Kind: keyRune, Rune: 'u'})
	if len(c.calls) != before {
		t.Fatal("pressing u made a synchronous call")
	}
	tuiDrainJobs(t, m, c)
	if len(c.calls) == before {
		t.Fatal("pressing u did not re-run the update check")
	}
}
