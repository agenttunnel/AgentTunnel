package cli

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The poll's roster and list reads run off the session loop and fold back
// onto it. A fold applies only while the reader is where the read was taken,
// so a read that lands after the reader moved on leaves the view alone.
func TestTUIRosterReadDropsWhenFocusMoved(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	tuiOpenWith(m, threadListRow{ID: "th_A"}, "demo", nil, nil)
	tracker := newTurnTracker(map[string]contracts.AgentStatus{}, false)

	if !tuiRosterCurrent(m, tracker, "th_A") {
		t.Fatal("a roster read for the open thread was dropped")
	}
	if tuiRosterCurrent(m, tracker, "th_B") {
		t.Fatal("a roster read for another thread would land on the open one")
	}
	if tuiRosterCurrent(m, nil, "th_A") {
		t.Fatal("a roster read folded with no turn tracker to update")
	}

	m.screen = tuiScreenList
	if tuiRosterCurrent(m, tracker, "th_A") {
		t.Fatal("a roster read landed after the reader left the thread")
	}
}

func TestTUIListReadDropsOutsideTheList(t *testing.T) {
	m := newTuiModel(tuiTestClock())
	if !tuiListCurrent(m) {
		t.Fatal("a list read was dropped while the list was on screen")
	}
	tuiOpenWith(m, threadListRow{ID: "th_A"}, "demo", nil, nil)
	if tuiListCurrent(m) {
		t.Fatal("a list read would overwrite the rows of an open thread")
	}
}
