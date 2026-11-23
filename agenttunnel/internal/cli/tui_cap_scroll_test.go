package cli

import (
	"fmt"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiCapLog opens a thread whose history fills the transcript to its block
// cap, so every append from here on drops the oldest block.
func tuiCapLog(t *testing.T) *tuiModel {
	t.Helper()
	now := tuiTestClock()
	backlog := make([]contracts.LogEntry, tuiLogCap)
	for i := range backlog {
		backlog[i] = contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: fmt.Sprintf("$ step %03d", i), At: now}
	}
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_cap"}, "demo", backlog, nil)
	m.logW, m.logH = 80, 24
	if len(m.log) != tuiLogCap {
		t.Fatalf("log holds %d blocks, want the cap %d", len(m.log), tuiLogCap)
	}
	return m
}

// A reader scrolled up stays on the same rows when the log is trimmed at the
// cap. Each one-row append adds one row to the offset, and the head block that
// falls off does not move it, exactly as an uncapped log would behave.
func TestTUIScrolledReaderSteadyAtTheBlockCap(t *testing.T) {
	m := tuiCapLog(t)
	m.scrollUp(5)
	now := tuiTestClock()

	m.appendLog(contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "$ next", At: now})
	if len(m.log) != tuiLogCap {
		t.Fatalf("log holds %d blocks after the append, want the cap %d", len(m.log), tuiLogCap)
	}
	if m.log[len(m.log)-1].text != "$ next" {
		t.Fatalf("newest block = %q, want the appended one", m.log[len(m.log)-1].text)
	}
	m.statusRow(80)
	if m.scroll != 6 {
		t.Fatalf("scroll = %d after one row arrived past the cap, want 6", m.scroll)
	}
}

// The same holds across several trims in a row, with a streamed reply in the
// window: the offset rises by the rows appended and no more.
func TestTUIScrolledReaderSteadyAcrossRepeatedTrims(t *testing.T) {
	m := tuiCapLog(t)
	m.scrollUp(5)
	now := tuiTestClock()

	for i := range 3 {
		m.appendLog(contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: fmt.Sprintf("$ burst %d", i), At: now})
	}
	m.appendLog(tuiStreamText(now, "lead", "a reply\n"))
	m.statusRow(80)
	if want := 5 + 4; m.scroll != want {
		t.Fatalf("scroll = %d after four rows arrived past the cap, want %d", m.scroll, want)
	}
	if len(m.log) != tuiLogCap {
		t.Fatalf("log holds %d blocks, want the cap %d", len(m.log), tuiLogCap)
	}
}
