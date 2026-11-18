package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
)

// logsFixture wires a dispatcher over one log store, the way the embedded and
// headless daemons share it, so RPC reads see exactly what the runner wrote.
func logsFixture(t *testing.T) (*Dispatcher, *logstore.Store) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	logs := logstore.New(t.TempDir())
	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:  eng,
		Logs:    logs,
		Mailbox: contextstore.NewMailbox(),
	})
	return NewDispatcher(DispatcherDeps{Engine: eng, Runner: runner, Logs: logs}), logs
}

func callRPC(t *testing.T, d *Dispatcher, method, params string) []contracts.LogEntry {
	t.Helper()
	out, err := d.Handle(context.Background(), Request{Method: method, Params: json.RawMessage(params)})
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	entries, ok := out.([]contracts.LogEntry)
	if !ok {
		t.Fatalf("%s returned %T", method, out)
	}
	return entries
}

// The team lead's id is the same in every thread, so thread.logs must answer
// per thread: one thread's lead output never shows up in another thread's log.
func TestThreadLogsStayWithTheirThread(t *testing.T) {
	d, logs := logsFixture(t)
	now := time.Unix(0, 0).UTC()
	_ = logs.Append("t1", contracts.LogEntry{AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "planning one", At: now})
	_ = logs.Append("t2", contracts.LogEntry{AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "planning two", At: now})

	got := callRPC(t, d, "thread.logs", `{"threadId":"t1"}`)
	if len(got) != 1 || got[0].Text != "planning one" {
		t.Fatalf("t1 logs = %+v", got)
	}
	if got := callRPC(t, d, "thread.logs", `{"threadId":"t3"}`); len(got) != 0 {
		t.Fatalf("unknown thread logs = %+v", got)
	}
}

// A thread's log is returned as its most recent lines, bounded so restoring a
// long thread never ships its whole history over the socket.
func TestThreadLogsReturnsBoundedTail(t *testing.T) {
	d, logs := logsFixture(t)
	total := threadLogTail + 5
	for i := 0; i < total; i++ {
		_ = logs.Append("t1", contracts.LogEntry{AgentID: "agent-a", Kind: contracts.LogText, Text: fmt.Sprint(i)})
	}

	got := callRPC(t, d, "thread.logs", `{"threadId":"t1"}`)
	if len(got) != threadLogTail {
		t.Fatalf("tail length = %d, want %d", len(got), threadLogTail)
	}
	if last := got[len(got)-1].Text; last != fmt.Sprint(total-1) {
		t.Fatalf("newest line = %q, want %d", last, total-1)
	}
}

// `before` pages backwards through a thread's log: each call returns the tail
// of what is older than the given time, so a client holding the newest lines
// walks to the start without ever receiving a line twice.
func TestThreadLogsPagesBackwardsWithBefore(t *testing.T) {
	d, logs := logsFixture(t)
	start := time.Unix(1_700_000_000, 0).UTC()
	total := threadLogTail*2 + 5
	for i := 0; i < total; i++ {
		_ = logs.Append("t1", contracts.LogEntry{
			AgentID: "agent-a", Kind: contracts.LogText, Text: fmt.Sprint(i), At: start.Add(time.Duration(i) * time.Millisecond),
		})
	}
	page := func(before time.Time) []contracts.LogEntry {
		params, _ := json.Marshal(map[string]any{"threadId": "t1", "before": before})
		return callRPC(t, d, "thread.logs", string(params))
	}

	tail := callRPC(t, d, "thread.logs", `{"threadId":"t1"}`)
	older := page(tail[0].At)
	if len(older) != threadLogTail {
		t.Fatalf("older page holds %d lines, want %d", len(older), threadLogTail)
	}
	if first, last := older[0].Text, older[len(older)-1].Text; first != "5" || last != fmt.Sprint(threadLogTail+4) {
		t.Fatalf("older page spans %s..%s, want 5..%d", first, last, threadLogTail+4)
	}
	oldest := page(older[0].At)
	if len(oldest) != 5 || oldest[0].Text != "0" || oldest[4].Text != "4" {
		t.Fatalf("oldest page = %d lines starting %q", len(oldest), oldest[0].Text)
	}
	if beyond := page(oldest[0].At); len(beyond) != 0 {
		t.Fatalf("a page before the first line returned %d lines", len(beyond))
	}
}

// agents.logs is scoped by thread too, so the detail panel of one thread's
// agent never lists another thread's agent with the same id.
func TestAgentLogsFilterByThread(t *testing.T) {
	d, logs := logsFixture(t)
	_ = logs.Append("t1", contracts.LogEntry{AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "one"})
	_ = logs.Append("t2", contracts.LogEntry{AgentID: "agent-orchestrator", Kind: contracts.LogText, Text: "two"})

	got := callRPC(t, d, "agents.logs", `{"threadId":"t2","agentId":"agent-orchestrator"}`)
	if len(got) != 1 || got[0].Text != "two" {
		t.Fatalf("agent logs = %+v", got)
	}
}

// The thread buffer is bounded, but agents.logs reads the thread's full file: a
// noisy agent filling the buffer must not evict a quiet agent's earlier lines.
func TestAgentLogsSurviveNoisyAgentInSameThread(t *testing.T) {
	d, logs := logsFixture(t)
	_ = logs.Append("t1", contracts.LogEntry{AgentID: "agent-quiet", Kind: contracts.LogText, Text: "quiet line"})
	// Past the buffer bound (2000), so the quiet line is gone from the buffer.
	for i := 0; i < 2100; i++ {
		_ = logs.Append("t1", contracts.LogEntry{AgentID: "agent-noisy", Kind: contracts.LogText, Text: fmt.Sprint(i)})
	}

	got := callRPC(t, d, "agents.logs", `{"threadId":"t1","agentId":"agent-quiet"}`)
	if len(got) != 1 || got[0].Text != "quiet line" {
		t.Fatalf("quiet agent logs = %+v", got)
	}
}
