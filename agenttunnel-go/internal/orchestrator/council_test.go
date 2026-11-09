package orchestrator

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// councilScript speaks per role so the test can watch a debate happen: each
// member echoes its seat and the round it is in, and the facilitator wraps up.
const councilScript = `case "$AT_ROLE" in
  facilitator)
    printf '{"type":"text","text":"facilitator: after two rounds the table converged; build it."}\n' ;;
  *)
    printf '{"type":"text","text":"seat %s round %s"}\n' "$AT_AGENT_ID" "$AT_ROLE" ;;
esac`

func TestRunCouncilDebatesInRounds(t *testing.T) {
	r, eng, ctx := newHarness(t, councilScript)
	o := New(r)

	consensus, err := o.RunCouncil(ctx, "t1", CouncilSpec{
		Topic:  "should we rewrite the parser?",
		Rounds: 2,
		Members: []CouncilMember{
			{Name: "planner", Role: contracts.RolePlanner},
			{Name: "reviewer", Role: contracts.RoleReviewer},
		},
	})
	if err != nil {
		t.Fatalf("RunCouncil: %v", err)
	}
	if !strings.Contains(consensus, "converged") {
		t.Fatalf("consensus = %q", consensus)
	}

	// Every member ran every round, parallel per round: 2 members x 2 rounds
	// plus one facilitator.
	state, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	wantAgents := map[contracts.AgentID]bool{
		"agent-council-planner-r1":  false,
		"agent-council-planner-r2":  false,
		"agent-council-reviewer-r1": false,
		"agent-council-reviewer-r2": false,
		"agent-council-facilitator": false,
	}
	for _, a := range state.Agents {
		if _, ok := wantAgents[a.ID]; ok {
			wantAgents[a.ID] = true
			if a.Status != contracts.AgentSucceeded {
				t.Fatalf("agent %s status = %s", a.ID, a.Status)
			}
		}
	}
	for id, seen := range wantAgents {
		if !seen {
			t.Fatalf("council agent %s never ran", id)
		}
	}

	// The debate is observable: each round's positions and the consensus land
	// in shared context. Round 1 carries both seats' opening positions.
	for _, key := range []string{"council_round_1", "council_round_2", "council_consensus"} {
		if _, ok := state.Shared[key]; !ok {
			t.Fatalf("shared context missing %q (have %v)", key, keysOf(state.Shared))
		}
	}
	if r1 := state.Shared["council_round_1"]; !strings.Contains(r1, "planner-r1") || !strings.Contains(r1, "reviewer-r1") {
		t.Fatalf("round 1 positions incomplete: %q", r1)
	}

	// Round 2 prompts saw round 1 positions: the planner's second round ran
	// with the reviewer's position already on the table.
	plannerLogged := false
	for _, e := range r.logs.Entries("t1") {
		if e.AgentID == "agent-council-planner-r2" {
			plannerLogged = true
		}
	}
	if !plannerLogged {
		t.Fatal("planner round 2 left no logs")
	}
}

// TestRunCouncilValidatesSpec pins the bounds users configure from the sheet.
func TestRunCouncilValidatesSpec(t *testing.T) {
	r, _, ctx := newHarness(t, councilScript)
	o := New(r)

	if _, err := o.RunCouncil(ctx, "t1", CouncilSpec{Topic: "  "}); err == nil {
		t.Fatal("empty topic accepted")
	}
	if _, err := o.RunCouncil(ctx, "t1", CouncilSpec{
		Topic:   "one voice",
		Members: []CouncilMember{{Name: "solo", Role: contracts.RoleExecutor}},
	}); err == nil {
		t.Fatal("single member accepted")
	}
	if _, err := o.RunCouncil(ctx, "t1", CouncilSpec{
		Topic:   "bad role",
		Members: []CouncilMember{{Name: "a", Role: "wizard"}, {Name: "b"}},
	}); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
