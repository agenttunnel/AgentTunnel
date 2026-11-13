package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// setSoloRuntime turns thread t1 into solo mode on the given runtime, the way
// a client's thread.solo.set would.
func setSoloRuntime(t *testing.T, eng *domain.Engine, ctx context.Context, rt contracts.LeadRuntime) {
	t.Helper()
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "solo-set", Type: contracts.CmdSoloRuntimeSet, ThreadID: "t1",
		SoloRuntime: &contracts.SoloRuntimeSet{Runtime: rt},
	}); err != nil {
		t.Fatal(err)
	}
}

// beginTurn records the user message the way the transport does before a
// turn runs, so the prompt's history section splits at it.
func beginTurn(t *testing.T, eng *domain.Engine, ctx context.Context, id, text string) {
	t.Helper()
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: id, Type: contracts.CmdTurnStart, ThreadID: "t1", Text: text,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedMessage(t *testing.T, eng *domain.Engine, ctx context.Context, id, author, text string) {
	t.Helper()
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: id, Type: contracts.CmdMessageAppend, ThreadID: "t1",
		Message: &contracts.MessageSent{MessageID: id, Author: author, Text: text},
	}); err != nil {
		t.Fatal(err)
	}
}

// turnOf returns the rollup of the thread's current turn.
func turnOf(t *testing.T, st *domain.ThreadState) domain.TurnUsage {
	t.Helper()
	for _, tu := range st.Turns {
		if tu.Turn == st.Turn {
			return tu
		}
	}
	t.Fatalf("no rollup for turn %d", st.Turn)
	return domain.TurnUsage{}
}

// TestSoloTurnRunsOneAgent covers the whole solo turn: exactly one parentless
// agent named solo, dispatched on the thread's solo runtime, succeeded, with
// its final text recorded as the turn's message — and no subagent, tool, or
// mailbox traffic anywhere.
func TestSoloTurnRunsOneAgent(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"all done"}\n'`)
	setSoloRuntime(t, eng, ctx, contracts.LeadRuntime{Harness: contracts.HarnessOpenCode, Model: "solo-model"})
	beginTurn(t, eng, ctx, "turn", "do the work")

	if err := New(r).RunTurn(ctx, "t1", "do the work"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	state, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.AgentOrder) != 1 || state.AgentOrder[0] != "agent-orchestrator" {
		t.Fatalf("agents = %v, want exactly the solo agent", state.AgentOrder)
	}
	a := state.Agents["agent-orchestrator"]
	if a.Name != "solo" || a.Role != contracts.RoleOrchestrator || a.ParentAgentID != "" {
		t.Fatalf("solo agent record = %+v", a)
	}
	if a.Harness != contracts.HarnessOpenCode || a.Model != "solo-model" {
		t.Fatalf("solo agent runtime = %s/%s, want opencode/solo-model", a.Harness, a.Model)
	}
	if a.Status != contracts.AgentSucceeded {
		t.Fatalf("solo agent status = %s", a.Status)
	}
	soloMessages := 0
	for _, m := range state.Messages {
		if m.Author == "solo" {
			soloMessages++
			if m.Text != "all done" {
				t.Fatalf("solo final message = %q", m.Text)
			}
		}
	}
	if soloMessages != 1 {
		t.Fatalf("messages from solo = %d, want 1", soloMessages)
	}
	if len(state.Mailbox) != 0 {
		t.Fatalf("solo turn posted mailbox messages: %+v", state.Mailbox)
	}
	for _, e := range r.Logs().Entries("t1") {
		if e.Kind == contracts.LogTool {
			t.Fatalf("solo turn logged tool traffic: %+v", e)
		}
	}
	if tu := turnOf(t, state); tu.EndedAt.IsZero() {
		t.Fatalf("turn %d was never stamped ended: %+v", state.Turn, tu)
	}
}

// TestSoloPromptIsPlain pins the solo prompt's shape: history, the request,
// and nothing from the orchestrator protocol — no AT_TOOL, no roster, no peer
// section — and no AT_* environment at all.
func TestSoloPromptIsPlain(t *testing.T) {
	var mu sync.Mutex
	var specs []providers.SessionSpec
	r, eng, ctx := newHarnessInvoking(t, func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		if strings.Contains(spec.Prompt, soloHeader) {
			mu.Lock()
			specs = append(specs, spec)
			mu.Unlock()
		}
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", `printf '{"type":"text","text":"ok"}\n'`}, PromptVia: "stdin"}, nil
	})
	seedMessage(t, eng, ctx, "m-history", "user", "please set up the parser")
	seedMessage(t, eng, ctx, "m-history-reply", "orchestrator", "the parser is set up")
	setSoloRuntime(t, eng, ctx, contracts.LeadRuntime{Harness: contracts.HarnessOpenCode, Model: "solo-model"})
	beginTurn(t, eng, ctx, "turn", "now tighten it")

	if err := New(r).RunTurn(ctx, "t1", "now tighten it"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(specs) != 1 {
		t.Fatalf("solo passes = %d, want 1", len(specs))
	}
	prompt := specs[0].Prompt
	for _, want := range []string{
		"## Conversation so far", "please set up the parser", "the parser is set up",
		"## Request", "now tighten it",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("solo prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, banned := range []string{"AT_TOOL", "Agents already dispatched", "Dispatching work", "Peer messages"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("solo prompt leaked orchestrator section %q:\n%s", banned, prompt)
		}
	}
	if len(specs[0].Env) != 0 {
		t.Fatalf("solo spec env = %v, want none", specs[0].Env)
	}
}

// TestSoloTurnHonorsSteering parks the first pass, steers while it runs, and
// asserts the steer is weighed before the turn reports done: a second pass
// runs with the steering section, and both the injected and consumed steering
// events are recorded.
func TestSoloTurnHonorsSteering(t *testing.T) {
	dir := t.TempDir()
	awake := filepath.Join(dir, "awake")
	release := filepath.Join(dir, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("go"), 0o644) })
	park := fmt.Sprintf("touch %q\nwhile [ ! -f %q ]; do sleep 0.02; done\nprintf '{\"type\":\"text\",\"text\":\"first\"}\\n'\n", awake, release)
	plain := `printf '{"type":"text","text":"second"}\n'`

	var mu sync.Mutex
	prompts := map[int]string{}
	var soloPasses int32
	r, eng, ctx := newHarnessInvoking(t, func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		if !strings.Contains(spec.Prompt, soloHeader) {
			return providers.CommandSpec{Binary: "sh", Args: []string{"-c", `printf '{"type":"text","text":"a title"}\n'`}, PromptVia: "stdin"}, nil
		}
		n := int(atomic.AddInt32(&soloPasses, 1))
		mu.Lock()
		prompts[n] = spec.Prompt
		mu.Unlock()
		script := plain
		if n == 1 {
			script = park
		}
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
	})
	setSoloRuntime(t, eng, ctx, contracts.LeadRuntime{Harness: contracts.HarnessOpenCode})
	beginTurn(t, eng, ctx, "turn", "do the thing")

	events, unsub := eng.Subscribe("t1")
	defer unsub()

	done := make(chan error, 1)
	go func() { done <- New(r).RunTurn(ctx, "t1", "do the thing") }()

	// Wait for the parked first pass to be running, then steer while it runs.
	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(awake); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first solo pass never started")
		case err := <-done:
			t.Fatalf("turn ended before the first pass parked: %v", err)
		case <-time.After(2 * time.Millisecond):
		}
	}
	const steer = "use the other approach instead"
	seedMessage(t, eng, ctx, "steer-1", "steering", steer)
	r.steering.Post("t1", "steer-1", steer)
	if err := os.WriteFile(release, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run turn: %v", err)
	}

	mu.Lock()
	p2 := prompts[2]
	mu.Unlock()
	if p2 == "" {
		t.Fatalf("no second pass ran; prompts = %v", prompts)
	}
	if !strings.Contains(p2, "## Steering (sent by the user while you were working)") || !strings.Contains(p2, steer) {
		t.Fatalf("second pass prompt never weighed the steer:\n%s", p2)
	}

	// Both drain records exist, typed the way live clients see them.
	injected, consumed := false, false
	drainDeadline := time.After(time.Second)
	for !(injected && consumed) {
		select {
		case ev := <-events:
			switch ev.Type() {
			case contracts.EvtSteeringInjected:
				injected = true
			case contracts.EvtSteeringConsumed:
				consumed = true
			}
		case <-drainDeadline:
			t.Fatalf("steering events missing: injected=%v consumed=%v", injected, consumed)
		}
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

// TestSoloTurnFailure pins that a harness that exits nonzero fails the solo
// agent, the turn, and surfaces the error from RunTurn.
func TestSoloTurnFailure(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"partial"}\n'; exit 1`)
	setSoloRuntime(t, eng, ctx, contracts.LeadRuntime{Harness: contracts.HarnessOpenCode})
	beginTurn(t, eng, ctx, "turn", "do the work")

	if err := New(r).RunTurn(ctx, "t1", "do the work"); err == nil {
		t.Fatal("run turn: want the harness error to surface")
	}

	state, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if a := state.Agents["agent-orchestrator"]; a.Status != contracts.AgentFailed {
		t.Fatalf("solo agent status = %s, want failed", a.Status)
	}
	if tu := turnOf(t, state); !tu.Failed {
		t.Fatalf("turn %d was not marked failed: %+v", state.Turn, tu)
	}
}

// TestSoloTurnEmptyOutputFails pins runTask's silent-failure guard for solo
// passes: a clean exit with no output at all is an error, not a green empty
// turn.
func TestSoloTurnEmptyOutputFails(t *testing.T) {
	r, eng, ctx := newHarness(t, `exit 0`)
	setSoloRuntime(t, eng, ctx, contracts.LeadRuntime{Harness: contracts.HarnessOpenCode})
	beginTurn(t, eng, ctx, "turn", "do the work")

	err := New(r).RunTurn(ctx, "t1", "do the work")
	if err == nil {
		t.Fatal("run turn: want the silent-failure error")
	}
	if !strings.Contains(err.Error(), "harness exited without any output") {
		t.Fatalf("run turn error = %v", err)
	}

	state, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if a := state.Agents["agent-orchestrator"]; a.Status != contracts.AgentFailed {
		t.Fatalf("solo agent status = %s, want failed", a.Status)
	}
}

// TestOrchestratorModeUnaffected is the regression guard on the RunTurn
// branch: a thread without a solo runtime still runs the full orchestrator
// prompt.
func TestOrchestratorModeUnaffected(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	r, eng, ctx := newHarnessInvoking(t, func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		if strings.Contains(spec.Prompt, "## User request") {
			mu.Lock()
			prompts = append(prompts, spec.Prompt)
			mu.Unlock()
		}
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", `printf '{"type":"text","text":"ok"}\n'`}, PromptVia: "stdin"}, nil
	})
	beginTurn(t, eng, ctx, "turn", "organize the work")

	if err := New(r).RunTurn(ctx, "t1", "organize the work"); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) == 0 {
		t.Fatal("no orchestrator pass ran")
	}
	if len(prompts) > 0 && strings.Contains(prompts[0], soloHeader) {
		t.Fatalf("non-solo thread took the solo branch:\n%s", prompts[0])
	}
	for _, want := range []string{"AT_TOOL", "Dispatching work", "## User request", "organize the work"} {
		if !strings.Contains(prompts[0], want) {
			t.Fatalf("orchestrator prompt missing %q:\n%s", want, prompts[0])
		}
	}
}
