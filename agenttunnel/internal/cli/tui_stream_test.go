package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiStreamLog opens a thread whose history is tool lines, which stay their
// own blocks, so a scrolled reader has real rows above the stream.
func tuiStreamLog(t *testing.T, scrollBy int) *tuiModel {
	t.Helper()
	now := tuiTestClock()
	backlog := make([]contracts.LogEntry, 40)
	for i := range backlog {
		backlog[i] = contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: fmt.Sprintf("$ earlier %02d", i), At: now}
	}
	m := newTuiModel(now)
	tuiOpenWith(m, threadListRow{ID: "th_stream"}, "demo", backlog, nil)
	m.logW, m.logH = 80, 24
	m.scrollUp(scrollBy)
	if m.scroll != scrollBy {
		t.Fatalf("scroll = %d, want %d: the backlog is too short to scroll", m.scroll, scrollBy)
	}
	return m
}

func tuiStreamText(now time.Time, agent, text string) contracts.LogEntry {
	return contracts.LogEntry{AgentID: contracts.AgentID(agent), Kind: contracts.LogText, Text: text, At: now}
}

// A reader scrolled up keeps the rows they were reading as a streamed reply
// grows below them: the offset rises by exactly the rows that arrived.
func TestTUIStreamKeepsScrolledViewSteady(t *testing.T) {
	m := tuiStreamLog(t, 3)
	now := tuiTestClock()
	before := m.logRows()
	for i := range 30 {
		m.appendLog(tuiStreamText(now, "lead", fmt.Sprintf("reply line %02d\n", i)))
	}
	added := m.logRows() - before
	if added <= 0 {
		t.Fatalf("the stream added %d rows, want some", added)
	}
	// Reading the view settles the growth; the offset is what the reader sees.
	m.statusRow(80)
	if want := 3 + added; m.scroll != want {
		t.Fatalf("scroll = %d after %d new rows, want %d", m.scroll, added, want)
	}
}

// Output that arrives while following does not start a scroll.
func TestTUIStreamFollowingStaysAtTail(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.logW, m.logH = 80, 24
	for i := range 30 {
		m.appendLog(tuiStreamText(now, "lead", fmt.Sprintf("reply line %02d\n", i)))
	}
	if !m.following() {
		t.Fatalf("scroll = %d while following, want 0", m.scroll)
	}
}

// A growing view settles once per read, so a reader who scrolls down after a
// stream lands where the rows put them, not where a stale measure left them.
func TestTUIStreamSettlesBeforeScrolling(t *testing.T) {
	m := tuiStreamLog(t, 5)
	now := tuiTestClock()
	before := m.logRows()
	for i := range 12 {
		m.appendLog(tuiStreamText(now, "lead", fmt.Sprintf("line %02d\n", i)))
	}
	added := m.logRows() - before
	m.scrollDown(1)
	if want := 5 + added - 1; m.scroll != want {
		t.Fatalf("scroll after scrolling down one = %d, want %d", m.scroll, want)
	}
}

// The transcript holds a byte budget as well as a block count, so a run of
// long replies cannot grow the buffer past it. The newest reply always stays.
func TestTUIStreamByteBudgetBoundsTranscript(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	m.logW, m.logH = 80, 24
	reply := strings.Repeat("a long reply word, several of them. ", 1700) // about 60KB
	var last string
	for i := range 80 {
		// A tool line between replies keeps each reply its own block.
		m.appendLog(contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "$ step", At: now})
		last = fmt.Sprintf("%02d %s", i, reply)
		m.appendLog(tuiStreamText(now, "lead", last))
	}
	total := 0
	for _, b := range m.log {
		total += len(b.text)
	}
	if budget := max(m.logCap, tuiLogCap) * tuiLogBlockBytes; total > budget+len(last) {
		t.Fatalf("transcript holds %d bytes, over the %d byte budget", total, budget)
	}
	if len(m.log) >= tuiLogCap {
		t.Fatalf("block count %d reached the block cap; the byte budget was meant to bind", len(m.log))
	}
	if got := m.log[len(m.log)-1].text; got != last {
		t.Fatalf("newest reply was trimmed away")
	}
}

// A streamed reply's text is exactly its chunks, in order, and a text changed
// outside a stream is kept as the start of the next chunk.
func TestTUIStreamBlockTextMatchesChunks(t *testing.T) {
	now := tuiTestClock()
	m := newTuiModel(now)
	var want strings.Builder
	for i := range 500 {
		chunk := fmt.Sprintf("chunk %03d\n", i)
		want.WriteString(chunk)
		m.appendLog(tuiStreamText(now, "lead", chunk))
	}
	if got := m.log[0].text; got != want.String() {
		t.Fatalf("streamed text differs from its chunks: %d bytes, want %d", len(got), want.Len())
	}

	m.log[0].text = "replaced "
	m.appendLog(tuiStreamText(now, "lead", "tail"))
	if got := m.log[0].text; got != "replaced tail" {
		t.Fatalf("text set outside a stream was lost: %q", got)
	}
}
