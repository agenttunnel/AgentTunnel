package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// titleScript fakes a harness for both title generations. A title call is
// recognized by its prompt ("Name this coding session"); the start-of-turn call
// sees only the user message while the after-cycle call also carries the
// summary, so each answers a distinct title. Every other invocation is a lead
// pass, which spins until gate exists — the test writes it only once the first
// rename has landed, so the turn cannot finish before the start title does.
func titleScript(gate string) string {
	return fmt.Sprintf(`case "$1" in
*"Name this coding session"*)
  case "$1" in
  *"What the team did"*) printf '{"type":"text","text":"Titled After The Cycle"}\n' ;;
  *) printf '{"type":"text","text":"Titled From The Prompt"}\n' ;;
  esac
  exit 0 ;;
esac
while [ ! -f %q ]; do :; done
printf '{"type":"text","text":"the first cycle is done"}\n'`, gate)
}

// nextTitle waits for the next thread.renamed event. The deadline only fails
// the test; the success path is driven by typed events.
func nextTitle(t *testing.T, renames <-chan string) string {
	t.Helper()
	select {
	case title := <-renames:
		return title
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for a thread.renamed event")
		return ""
	}
}

// TestRunTurnTitlesThreadAtStartAndAfterCycle pins the two moments a fresh
// thread is named: from the prompt alone while the first turn is still running,
// and again once that cycle completes, from the prompt plus the final summary.
func TestRunTurnTitlesThreadAtStartAndAfterCycle(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "titled")
	r, eng, ctx := newPromptHarness(t, titleScript(gate))
	events, unsub := eng.Subscribe("t1")
	defer unsub()

	renames := make(chan string, 4)
	go func() {
		for ev := range events {
			if renamed, ok := ev.Payload.(contracts.ThreadRenamed); ok {
				renames <- renamed.Title
			}
		}
	}()

	turnDone := make(chan error, 1)
	go func() { turnDone <- New(r).RunTurn(ctx, "t1", "please summarize the work") }()

	// The lead's pass is parked on the gate until this rename lands, so the
	// turn is provably still running when the thread gets its first title.
	if title := nextTitle(t, renames); title != "Titled From The Prompt" {
		t.Fatalf("start-of-thread title = %q", title)
	}
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-turnDone; err != nil {
		t.Fatalf("run turn: %v", err)
	}

	// The completed cycle renames again, now from the prompt plus the summary.
	if title := nextTitle(t, renames); title != "Titled After The Cycle" {
		t.Fatalf("after-cycle title = %q", title)
	}
	state, _ := eng.State(ctx, "t1")
	if state.Title != "Titled After The Cycle" {
		t.Fatalf("thread title = %q", state.Title)
	}

	// A later turn leaves the title alone: nothing more arrives.
	if err := New(r).RunTurn(ctx, "t1", "round two, other priorities"); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	select {
	case title := <-renames:
		t.Fatalf("second turn renamed the thread to %q", title)
	default:
	}
}
