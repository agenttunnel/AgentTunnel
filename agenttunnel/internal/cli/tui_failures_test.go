package cli

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A method the scripted daemon does not answer replies with an error, so
// dropping a result key is how a test makes one call fail.
func tuiFailMethod(s *scriptedServer, method string) {
	s.mu.Lock()
	delete(s.results, method)
	s.mu.Unlock()
}

// When the connection comes back but the open thread cannot be read again, the
// user is told in the transcript instead of seeing a silent online header.
func TestTUIReopenErrorAfterRedialIsNotified(t *testing.T) {
	s := tuiServer("th_1", "running")
	again := httptest.NewServer(s)
	t.Cleanup(again.Close)
	back := make(chan struct{})
	var dialed atomic.Bool
	redial := func(ctx context.Context) (*Client, error) {
		if dialed.Swap(true) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		select {
		case <-back:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return Dial(ctx, "ws"+strings.TrimPrefix(again.URL, "http"), "")
	}
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), redial)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	s.mu.Lock()
	for conn := range s.conns {
		conn.CloseNow()
	}
	s.mu.Unlock()
	run.term.waitNextDraw(t, containsLine("reconnecting"))

	// The daemon is back, but it no longer answers the subscription.
	tuiFailMethod(s, "thread.subscribe")
	close(back)
	run.term.waitNextDraw(t, containsLine("reopen after reconnect: "))
}

// A turn that fails to start after the user left the thread is still reported:
// it lands on the flash line of the list it returns to.
func TestTUITurnStartErrorAfterLeavingIsNotified(t *testing.T) {
	s := tuiServer("th_1", "succeeded")
	tuiFailMethod(s, "thread.turn.start")
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	// Enter starts the turn (no agent is active, so it is not a steer); Esc
	// leaves the thread before the daemon's refusal can arrive.
	typeLine(run.keys, "start it")
	run.keys <- tuiKey{Kind: keyEsc}
	frame := run.term.waitNextDraw(t, containsLine("thread.turn.start: "))
	if strings.Contains(frame, "backlog line") {
		t.Fatalf("the session is still on the thread:\n%s", frame)
	}
}

// A failed @-mention search says why the list is empty.
func TestTUIMentionSearchErrorIsShown(t *testing.T) {
	s := tuiServer("th_1", "running")
	tuiFailMethod(s, "files.search")
	run := startTuiSession(t, s, "th_1", newFakeTuiTerm(100, 30), nil)
	run.term.waitNextDraw(t, containsLine("backlog line"))

	for _, r := range "@src" {
		run.keys <- tuiKey{Kind: keyRune, Rune: r}
	}
	run.term.waitNextDraw(t, containsLine("files.search: "))
}

// A scrolled transcript at narrow width keeps the working indicator; only the
// scroll hint may be dropped.
func TestTUIStatusRowKeepsWorkingWhenScrolledNarrow(t *testing.T) {
	m := newTuiModel(time.Now())
	m.roster = map[string]contracts.AgentStatus{"agent-orchestrator": contracts.AgentRunning}
	m.scroll = 3
	plain := m.statusRow(24).plain()
	if !strings.Contains(plain, "working") {
		t.Fatalf("the working indicator vanished at 24 columns while scrolled: %q", plain)
	}
	if got := len([]rune(m.statusRow(24).plain())); got > 24 {
		t.Fatalf("status row is %d columns, want at most 24", got)
	}
}
