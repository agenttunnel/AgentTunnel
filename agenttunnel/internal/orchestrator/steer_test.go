package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

func TestSteerQueueKeysByThreadAndAgent(t *testing.T) {
	q := newSteerQueue()
	q.Post("t1", "agent-hub", "  narrow the scope  ")
	q.Post("t1", "agent-hub", "and skip the migration")
	q.Post("t2", "agent-hub", "other thread's steer")
	q.Post("t1", "agent-spoke", "a different agent")
	q.Post("t1", "agent-hub", "   ") // blank is ignored
	q.Post("", "agent-hub", "no thread")

	got := q.Drain("t1", "agent-hub")
	if len(got) != 2 || got[0] != "narrow the scope" || got[1] != "and skip the migration" {
		t.Fatalf("t1/agent-hub drained %q", got)
	}
	if again := q.Drain("t1", "agent-hub"); len(again) != 0 {
		t.Fatalf("drain should clear the queue, got %q", again)
	}
	if other := q.Drain("t2", "agent-hub"); len(other) != 1 || other[0] != "other thread's steer" {
		t.Fatalf("t2/agent-hub drained %q", other)
	}
	if spoke := q.Drain("t1", "agent-spoke"); len(spoke) != 1 {
		t.Fatalf("t1/agent-spoke drained %q", spoke)
	}
}

func TestHasSubagentsIgnoresLeads(t *testing.T) {
	leadsOnly := &domain.ThreadState{Agents: map[contracts.AgentID]contracts.Agent{
		"agent-orchestrator":       {ID: "agent-orchestrator"},
		"agent-orchestrator-lead2": {ID: "agent-orchestrator-lead2"},
	}}
	if hasSubagents(leadsOnly) {
		t.Fatal("a thread with only leads has no subagent to steer")
	}
	leadsOnly.Agents["agent-hub"] = contracts.Agent{ID: "agent-hub"}
	if !hasSubagents(leadsOnly) {
		t.Fatal("a subagent should enable the steer tool")
	}
}

// A steer for one subagent reaches only that agent's next run in that thread.
// Both threads share the lead id and the subagent id, so an id-only key would
// deliver t1's steer into t2's run.
func TestSteerReachesOnlyItsThreadsNextRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// Each run writes the prompt it was given to <thread>-<agent>, then reports.
	script := fmt.Sprintf(`printf '%%s' "$1" > '%s'"/$AT_THREAD_ID-$AT_AGENT_ID"; printf '{"type":"text","text":"done"}\n'`, dir)
	r, eng, _ := newPromptHarness(t, script)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}

	hub := func() []contracts.DispatchTask {
		return []contracts.DispatchTask{{AgentID: "agent-hub", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work"}}
	}
	for _, th := range []contracts.ThreadID{"t1", "t2"} {
		if err := r.Dispatch(ctx, th, hub()); err != nil {
			t.Fatalf("dispatch hub on %s: %v", th, err)
		}
	}

	// Each thread's own lead steers its hub. Both leads have the same id.
	if _, err := r.Steer(ctx, "t1", "agent-orchestrator", "hub", "T1-STEER narrow the scope"); err != nil {
		t.Fatalf("steer t1: %v", err)
	}
	if _, err := r.Steer(ctx, "t2", "agent-orchestrator", "hub", "T2-STEER keep the scope"); err != nil {
		t.Fatalf("steer t2: %v", err)
	}

	if err := r.Run(ctx, "t2", hub()); err != nil {
		t.Fatalf("run t2 hub: %v", err)
	}
	t2 := readPrompt(t, dir, "t2", "agent-hub")
	if !strings.Contains(t2, "T2-STEER keep the scope") || strings.Contains(t2, "T1-STEER") {
		t.Fatalf("t2 hub prompt should carry only t2's steer:\n%s", t2)
	}

	if err := r.Run(ctx, "t1", hub()); err != nil {
		t.Fatalf("run t1 hub: %v", err)
	}
	t1 := readPrompt(t, dir, "t1", "agent-hub")
	if !strings.Contains(t1, "T1-STEER narrow the scope") || strings.Contains(t1, "T2-STEER") {
		t.Fatalf("t1 hub prompt should carry only t1's steer:\n%s", t1)
	}

	// Read once, then cleared: nothing is left for a later run to render.
	if left := r.steers.Drain("t1", "agent-hub"); len(left) != 0 {
		t.Fatalf("steer should be cleared once rendered, got %q", left)
	}
}

func TestSteerRefusesWhatItCannotDeliver(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := fmt.Sprintf(`printf '%%s' "$1" > '%s'"/$AT_THREAD_ID-$AT_AGENT_ID"; printf '{"type":"text","text":"done"}\n'`, dir)
	r, _, _ := newPromptHarness(t, script)
	hub := []contracts.DispatchTask{{AgentID: "agent-hub", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work"}}
	if err := r.Dispatch(ctx, "t1", hub); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		from contracts.AgentID
		to   string
		text string
		want error
	}{
		{"subagent cannot steer", "agent-hub", "hub", "x", ErrSteerNotLead},
		{"lead is not steerable", "agent-orchestrator", "orchestrator", "x", ErrSteerTargetLead},
		{"unknown agent", "agent-orchestrator", "ghost", "x", ErrUnknownRecipient},
		{"blank text", "agent-orchestrator", "hub", "   ", ErrEmptyMessage},
	}
	for _, tc := range cases {
		if _, err := r.Steer(ctx, "t1", tc.from, tc.to, tc.text); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if left := r.steers.Drain("t1", "agent-hub"); len(left) != 0 {
		t.Fatalf("refused steers were queued: %q", left)
	}

	// Once the agent has finished it has no next run, so a steer is refused
	// rather than reported as queued.
	if err := r.Run(ctx, "t1", hub); err != nil {
		t.Fatalf("run hub: %v", err)
	}
	if _, err := r.Steer(ctx, "t1", "agent-orchestrator", "hub", "too late"); !errors.Is(err, ErrSteerFinished) {
		t.Fatalf("finished agent: err = %v, want ErrSteerFinished", err)
	}
}

func readPrompt(t *testing.T, dir, thread, agent string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, thread+"-"+agent))
	if err != nil {
		t.Fatalf("read %s/%s prompt: %v", thread, agent, err)
	}
	return string(b)
}
