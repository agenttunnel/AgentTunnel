package orchestrator

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// subagentID matches every id a dispatched subagent can receive: a safe name
// followed by a sequence number.
var subagentID = regexp.MustCompile(`^agent-.+-\d+$`)

func TestLeadIDsNeverCollideWithSubagentIDs(t *testing.T) {
	for i := 0; i < contracts.MaxLeads; i++ {
		id := leadAgentID(i)
		if subagentID.MatchString(string(id)) {
			t.Fatalf("lead %d id %q could be minted for a subagent", i, id)
		}
		if !isLeadID(id) {
			t.Fatalf("lead %d id %q not recognized as a lead", i, id)
		}
	}
	if isLeadID("agent-orchestrator-2") {
		t.Fatal("a subagent named orchestrator must not be treated as a lead")
	}
}

// startLead sets the thread's team size through the decider, as the desktop
// toggle does.
func startLead(t *testing.T, r *Runner, ctx context.Context, n int) {
	t.Helper()
	if err := r.engine.Dispatch(ctx, contracts.Command{
		ID: "leads", Type: contracts.CmdLeadsSet, ThreadID: "t1", Leads: &n,
	}); err != nil {
		t.Fatalf("set leads: %v", err)
	}
}

// TestTeamLeadsDiscussAcrossOneRound runs a two-lead team. Lead one messages
// lead two during its pass, and lead two must see that message in the same
// round, because leads speak in order. Lead two replies, so lead one owes a
// second round and must see the reply in its prompt. The fake harness asserts
// both prompts and fails the turn if either is missing.
func TestTeamLeadsDiscussAcrossOneRound(t *testing.T) {
	script := `case "$AT_AGENT_ID:$AT_PASS" in
  agent-orchestrator:0)
    printf '%s\n' 'tool message --to orchestrator-2 --text "agree on the plan"'
    printf '{"type":"text","text":"lead one proposed a plan"}\n' ;;
  agent-orchestrator-lead2:0)
    case "$1" in *"from agent-orchestrator: agree on the plan"*) ;;
      *) printf '{"type":"error","text":"lead two missed the same-round peer message"}\n'; exit 1 ;;
    esac
    case "$1" in *"lead 2 of 2"*) ;;
      *) printf '{"type":"error","text":"lead two prompt lacks its team brief"}\n'; exit 1 ;;
    esac
    printf '%s\n' 'tool message --to orchestrator --text "agreed, dispatch on your word"'
    printf '{"type":"text","text":"lead two agreed"}\n' ;;
  agent-orchestrator:1)
    case "$1" in *"from agent-orchestrator-lead2: agreed, dispatch on your word"*) ;;
      *) printf '{"type":"error","text":"lead one missed the reply in round two"}\n'; exit 1 ;;
    esac
    printf '{"type":"text","text":"lead one confirms the plan"}\n' ;;
  agent-orchestrator-lead2:1)
    printf '{"type":"text","text":"team agreed, nothing to dispatch"}\n' ;;
  *) printf '{"type":"text","text":"ok"}\n' ;;
esac`

	r, eng, ctx := newPromptHarness(t, script)
	startLead(t, r, ctx, 2)
	r.Mailbox().SetEnabled(true)

	if err := New(r).RunTurn(ctx, "t1", "plan the refactor with a team"); err != nil {
		for _, e := range r.Logs().Entries("t1") {
			t.Logf("%s %s: %s", e.AgentID, e.Kind, e.Text)
		}
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	for _, id := range []contracts.AgentID{"agent-orchestrator", leadAgentID(1)} {
		if got := state.Agents[id].Status; got != contracts.AgentSucceeded {
			t.Fatalf("%s status = %s, want succeeded", id, got)
		}
	}
	if len(state.Mailbox) != 2 {
		t.Fatalf("mailbox = %+v, want the two lead messages", state.Mailbox)
	}
	if state.Mailbox[0].To != leadAgentID(1) || state.Mailbox[1].To != "agent-orchestrator" {
		t.Fatalf("mailbox routing = %+v", state.Mailbox)
	}
	if state.LeadCount() != 2 {
		t.Fatalf("LeadCount() = %d, want 2", state.LeadCount())
	}
	// The final summary is the text of the last lead to speak in the last round.
	var final string
	for _, m := range state.Messages {
		if m.Author == "orchestrator" {
			final = m.Text
		}
	}
	if !strings.Contains(final, "team agreed") {
		t.Fatalf("final message = %q", final)
	}
}
