package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// A steer taken between a solo agent's tool calls is weighed there, not again:
// the pass that took it is the only pass, and the transcript records the drain.
func TestSoloSteeringTakenMidPassIsNotWeighedAgain(t *testing.T) {
	dir := t.TempDir()
	awake := filepath.Join(dir, "awake")
	release := filepath.Join(dir, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("go"), 0o644) })
	park := fmt.Sprintf("touch %q\nwhile [ ! -f %q ]; do sleep 0.02; done\nprintf '{\"type\":\"text\",\"text\":\"done\"}\\n'\n", awake, release)

	var soloPasses int32
	r, eng, ctx := newHarnessInvoking(t, func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		if !strings.Contains(spec.Prompt, soloHeader) {
			return providers.CommandSpec{Binary: "sh", Args: []string{"-c", `printf '{"type":"text","text":"a title"}\n'`}, PromptVia: "stdin"}, nil
		}
		atomic.AddInt32(&soloPasses, 1)
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", park}, PromptVia: "stdin"}, nil
	})
	setSoloRuntime(t, eng, ctx, contracts.LeadRuntime{Harness: contracts.HarnessOpenCode})
	beginTurn(t, eng, ctx, "turn", "do the thing")

	done := make(chan error, 1)
	go func() { done <- New(r).RunTurn(ctx, "t1", "do the thing") }()
	waitForFile(t, awake, done)

	const steer = "use the other approach instead"
	seedMessage(t, eng, ctx, "steer-1", "steering", steer)
	r.steering.Post("t1", "steer-1", steer)
	block, err := r.TakeSoloSteering(ctx, "t1")
	if err != nil {
		t.Fatalf("take steering: %v", err)
	}
	if !strings.Contains(block, "## Steering (sent by the user while you were working)") || !strings.Contains(block, steer) {
		t.Fatalf("hook context missing the steer:\n%s", block)
	}
	if again, _ := r.TakeSoloSteering(ctx, "t1"); again != "" {
		t.Fatalf("second take returned %q, want nothing pending", again)
	}

	if err := os.WriteFile(release, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if n := atomic.LoadInt32(&soloPasses); n != 1 {
		t.Fatalf("solo passes = %d, want 1: a steer already taken must not earn another pass", n)
	}
	state, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range state.Messages {
		if m.ID == "steer-1" && !m.Consumed {
			t.Fatalf("steer was not projected as consumed: %+v", m)
		}
	}
}

// Only solo threads take steering between tool calls; a team's steering waits
// for the lead's next pass and stays queued.
func TestTakeSoloSteeringRefusesTeamThread(t *testing.T) {
	r, _, ctx := newHarnessInvoking(t, func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", "true"}, PromptVia: "stdin"}, nil
	})
	r.steering.Post("t1", "m1", "focus on tests")

	if _, err := r.TakeSoloSteering(ctx, "t1"); err == nil {
		t.Fatal("a team thread handed out steering between tool calls")
	}
	if !r.HasSteering("t1") {
		t.Fatal("refused steering was drained from the lead's queue")
	}
}

// waitForFile polls until a file the harness script touched exists, failing if
// the turn ends first.
func waitForFile(t *testing.T, path string, done <-chan error) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s never appeared", path)
		case err := <-done:
			t.Fatalf("turn ended before %s appeared: %v", path, err)
		case <-time.After(2 * time.Millisecond):
		}
	}
}
