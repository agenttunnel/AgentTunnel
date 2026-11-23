package cli

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// tuiStreamChunks cuts a prose reply of about 300KB into the chunk sizes a
// harness streams, each piece carrying its own line breaks.
func tuiStreamChunks() []string {
	const total, piece = 300 << 10, 64
	word := "the agent streams a reply with words, commas and a line break.\n"
	body := strings.Repeat(word, total/len(word)+1)[:total]
	var chunks []string
	for len(body) > 0 {
		n := min(piece, len(body))
		chunks = append(chunks, body[:n])
		body = body[n:]
	}
	return chunks
}

// benchTUIStream folds one 300KB streamed reply into an open thread view, one
// chunk per event. A scrolled reader holds a page of history above the tail,
// the case where every event used to re-render the streaming block.
func benchTUIStream(b *testing.B, scrolled bool) {
	chunks := tuiStreamChunks()
	now := tuiTestClock()
	backlog := make([]contracts.LogEntry, 40)
	for i := range backlog {
		// Tool lines stay their own blocks, so the page of history is real
		// rows above the stream rather than merging into it.
		backlog[i] = contracts.LogEntry{AgentID: "lead", Kind: contracts.LogTool, Text: "$ earlier command", At: now}
	}
	b.SetBytes(300 << 10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		m := newTuiModel(now)
		tuiOpenWith(m, threadListRow{ID: "th_bench"}, "demo", backlog, nil)
		m.logW, m.logH = 80, 24
		if scrolled {
			m.scroll = 3
		}
		b.StartTimer()
		for _, text := range chunks {
			m.appendLog(contracts.LogEntry{AgentID: "lead", Kind: contracts.LogText, Text: text, At: now})
		}
		b.StopTimer()
		if len(m.log) == 0 {
			b.Fatal("stream left no transcript")
		}
	}
}

func BenchmarkTUIStream(b *testing.B) {
	b.Run("following", func(b *testing.B) { benchTUIStream(b, false) })
	b.Run("scrolled", func(b *testing.B) { benchTUIStream(b, true) })
}
