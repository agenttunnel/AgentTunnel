package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// With nothing typed the composer has no cursor to move, so the arrows walk
// the tabs; once there is a draft they edit it again.
func TestTUIArrowsCycleTabsOnlyWhileTheComposerIsEmpty(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	m.openThread(threadListRow{ID: "th_1"}, "demo", nil)
	order := tuiViewOrder()
	press := func(kind tuiKeyKind) {
		tuiThreadKey(m, new(*turnTracker), tuiKey{Kind: kind}, 20, func(string) {})
	}

	press(keyRight)
	if m.view != order[1] {
		t.Fatalf("right on an empty composer: view = %q, want %q", m.view, order[1])
	}
	m.switchView(tuiThreadsViewID)
	press(keyLeft)
	if want := order[len(order)-1]; m.view != want {
		t.Fatalf("left from the first tab: view = %q, want it to wrap to %q", m.view, want)
	}

	m.switchView(tuiThreadsViewID)
	m.input.set("draft")
	press(keyLeft)
	press(keyRight)
	if m.view != tuiThreadsViewID {
		t.Fatalf("arrows left the thread with a draft typed: view = %q", m.view)
	}
}

// The list and every registered tab take the arrows too, unless the tab uses
// them itself.
func TestTUISessionArrowsCycleTabsFromTheListAndATab(t *testing.T) {
	tuiTestRegistry(t, notesView())
	s := tuiServer("th_1", "running")
	run := startTuiSession(t, s, "", newFakeTuiTerm(100, 24), nil)
	run.term.waitNextDraw(t, containsLine("the tui job"))

	run.keys <- tuiKey{Kind: keyRight}
	run.term.waitNextDraw(t, containsLine("notes body loaded"))
	run.keys <- tuiKey{Kind: keyLeft}
	run.term.waitNextDraw(t, containsLine("the tui job"))
}

func soloRow() threadListRow {
	return threadListRow{
		ID: "th_solo000001", Title: "quick fix", ProjectID: "prj_1",
		SoloRuntime: &contracts.LeadRuntime{Harness: "claude", Model: "sonnet"},
	}
}

// A solo thread says so everywhere a thread is shown: its list row, the
// preview beside the list, its title bar, and the sidebar.
func TestTUISoloThreadsAreMarkedEverywhere(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	m.setList([]psRow{
		{thread: soloRow(), project: "demo"},
		{thread: threadListRow{ID: "th_team000001", Title: "big job"}, project: "demo"},
	})

	list := tuiPlainFrame(m.renderList(130, 20))
	if !strings.Contains(list, "◇ solo") {
		t.Fatalf("the solo row has no badge:\n%s", list)
	}
	if !strings.Contains(list, "claude/sonnet") {
		t.Fatalf("the preview does not name the solo runtime:\n%s", list)
	}
	m.moveCursor(1)
	if team := tuiPlainFrame(m.renderList(130, 20)); strings.Contains(team, "claude/sonnet") || !strings.Contains(team, "orchestrator team") {
		t.Fatalf("a team thread is shown as solo or unlabelled:\n%s", team)
	}

	m.openThread(soloRow(), "demo", nil)
	if bar := m.threadBar(100).plain(); !strings.Contains(bar, "◇ solo") {
		t.Fatalf("the title bar has no solo mark: %q", bar)
	}
	side := tuiPlainFrame(m.sideRows(34))
	if !strings.Contains(side, "claude/sonnet") || !strings.Contains(side, "parked while solo") {
		t.Fatalf("the sidebar does not show solo mode:\n%s", side)
	}
}

// The preview needs room: narrow terminals keep the whole width for the list
// until the user asks for it, and every row fills the width either way.
func TestTUIListPreviewFollowsTheWidth(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	m.setList([]psRow{{thread: soloRow(), project: "demo"}})
	if strings.Contains(tuiPlainFrame(m.renderList(80, 20)), "Mode") {
		t.Fatal("an 80-column list shows the preview by default")
	}
	m.toggleSide()
	if !strings.Contains(tuiPlainFrame(m.renderList(100, 20)), "Mode") {
		t.Fatal("ctrl+b did not show the preview")
	}
	for _, cols := range []int{90, 109, 110, 150} {
		for i, row := range m.renderList(cols, 20) {
			if got := row.width(); got != cols {
				t.Fatalf("cols %d: row %d is %d cells wide", cols, i, got)
			}
		}
	}
}

func TestTUISoloCommandTurnsSoloOnAndOff(t *testing.T) {
	s := tuiServer("th_1", "idle")
	s.results["thread.setSolo"] = map[string]any{"ok": true}
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	var params struct {
		ThreadID string `json:"threadId"`
		Harness  string `json:"harness"`
		Model    string `json:"model"`
	}
	typeLine(run.keys, "/solo codex gpt-5")
	if err := json.Unmarshal(s.waitCall(t, "thread.setSolo").params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "th_1" || params.Harness != "codex" || params.Model != "gpt-5" {
		t.Fatalf("thread.setSolo params = %+v", params)
	}
	run.term.waitNextDraw(t, containsLine("solo mode on: codex/gpt-5"))
	run.term.waitNextDraw(t, containsLine("◇ solo"))

	typeLine(run.keys, "/solo off")
	run.term.waitNextDraw(t, containsLine("solo mode off"))
	if got := run.m.thread.SoloRuntime; got != nil {
		t.Fatalf("thread still solo after /solo off: %+v", got)
	}
}

// A bare /solo flips the mode like the desktop toggle: on with the
// orchestrator role's harness, and off again when it is already on.
func TestTUISoloToggleFollowsTheDesktopRule(t *testing.T) {
	s := tuiServer("th_1", "idle")
	s.results["thread.setSolo"] = map[string]any{"ok": true}
	s.results["thread.get"] = map[string]any{"title": "the tui job"}
	s.results["settings.get"] = map[string]any{
		"roles": []map[string]any{{"id": "orchestrator", "harness": "claude", "enabled": true}},
	}
	s.results["harness.list"] = map[string]any{"enabled": []string{"codex", "claude"}}
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	typeLine(run.keys, "/solo")
	var params struct {
		Harness string `json:"harness"`
	}
	if err := json.Unmarshal(s.waitCall(t, "thread.setSolo").params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Harness != "claude" {
		t.Fatalf("solo turned on with %q, want the orchestrator role's claude", params.Harness)
	}

	s.results["thread.get"] = map[string]any{"soloRuntime": map[string]any{"harness": "claude"}}
	typeLine(run.keys, "/solo")
	run.term.waitNextDraw(t, containsLine("solo mode off"))
}

// A new solo thread asks for its harness before the draft exists, and sets it
// on the draft before opening it.
func TestTUINewSoloThreadSetsTheRuntimeOnTheDraft(t *testing.T) {
	s := tuiServer("th_1", "idle")
	s.results["thread.create"] = map[string]any{"threadId": "th_1"}
	s.results["thread.setSolo"] = map[string]any{"ok": true}
	s.results["harness.list"] = map[string]any{
		"enabled": []string{"claude"}, "models": map[string][]string{"claude": {"sonnet"}},
	}
	run := startTuiSession(t, s, "", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("the tui job"))

	run.keys <- tuiKey{Kind: keyRune, Rune: 's'}
	run.term.waitNextDraw(t, containsLine("new solo thread in"))
	run.keys <- tuiKey{Kind: keyEnter} // the project
	run.term.waitNextDraw(t, containsLine("solo harness"))
	run.keys <- tuiKey{Kind: keyEnter} // claude
	run.term.waitNextDraw(t, containsLine("sonnet"))
	run.keys <- tuiKey{Kind: keyDown}
	run.keys <- tuiKey{Kind: keyEnter} // the model

	var params struct {
		ThreadID string `json:"threadId"`
		Harness  string `json:"harness"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(s.waitCall(t, "thread.setSolo").params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "th_1" || params.Harness != "claude" || params.Model != "sonnet" {
		t.Fatalf("thread.setSolo params = %+v", params)
	}
}

// The Threads tab carries a count, so the bar says something is happening
// from any tab: working threads first, else the unread ones.
func TestTUIThreadsTabBadge(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	if got := m.tabBadge(tuiThreadsViewID); got != "" {
		t.Fatalf("empty list badge = %q", got)
	}
	running := threadListRow{ID: "th_run", RunningAgents: 1, Agents: 1}
	m.setList([]psRow{{thread: running}, {thread: threadListRow{ID: "th_idle", Messages: 1}}})
	if got := m.tabBadge(tuiThreadsViewID); got != "●1" {
		t.Fatalf("badge with one running thread = %q, want ●1", got)
	}
	if got := m.tabBadge("files"); got != "" {
		t.Fatalf("a view tab has badge %q", got)
	}
	if header := m.headerRow(80).plain(); !strings.Contains(header, "Threads ●1") {
		t.Fatalf("the header does not show the badge: %q", header)
	}
}
