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
	for i := 0; i < 8; i++ {
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

// TestTeamLeadsSpeakAtOnceAndDiscussAcrossRounds runs a two-lead team whose
// leads both post a message in round one. The round-one passes rendezvous on
// files: each announces itself, then waits for the other, so the turn only
// finishes if both passes are in flight at once. Each lead's round-two prompt
// must carry the other's message, and the turn ends in round two once the
// team has nothing left to say.
func TestTeamLeadsSpeakAtOnceAndDiscussAcrossRounds(t *testing.T) {
	dir := t.TempDir()
	script := strings.ReplaceAll(`rendezvous() {
  touch "@DIR@/$1"
  n=0
  while [ ! -f "@DIR@/$2" ]; do
    n=$((n+1)); [ $n -gt 1000 ] && { printf '{"type":"error","text":"peer lead never started its pass"}\n'; exit 1; }
    sleep 0.01
  done
}
case "$AT_AGENT_ID:$AT_PASS" in
  agent-orchestrator:0)
    rendezvous lead1 lead2
    printf '%s\n' 'tool message --to orchestrator-2 --text "agree on the plan"'
    printf '{"type":"text","text":"lead one proposed a plan"}\n' ;;
  agent-orchestrator-lead2:0)
    rendezvous lead2 lead1
    printf '%s\n' 'tool message --to orchestrator --text "agreed, dispatch on your word"'
    printf '{"type":"text","text":"lead two agreed"}\n' ;;
  agent-orchestrator:1)
    case "$1" in *"from agent-orchestrator-lead2: agreed, dispatch on your word"*) ;;
      *) printf '{"type":"error","text":"lead one missed the round-one reply"}\n'; exit 1 ;;
    esac
    printf '{"type":"text","text":"lead one confirms the plan"}\n' ;;
  agent-orchestrator-lead2:1)
    case "$1" in *"from agent-orchestrator: agree on the plan"*) ;;
      *) printf '{"type":"error","text":"lead two missed the round-one proposal"}\n'; exit 1 ;;
    esac
    case "$1" in *"lead 2 of 2"*) ;;
      *) printf '{"type":"error","text":"lead two prompt lacks its team brief"}\n'; exit 1 ;;
    esac
    printf '{"type":"text","text":"team agreed, nothing to dispatch"}\n' ;;
  *) printf '{"type":"text","text":"ok"}\n' ;;
esac`, "@DIR@", dir)

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
	routed := map[contracts.AgentID]bool{}
	for _, m := range state.Mailbox {
		routed[m.To] = true
	}
	if !routed[leadAgentID(1)] || !routed["agent-orchestrator"] {
		t.Fatalf("mailbox routing = %+v, want one message to each lead", state.Mailbox)
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

// TestFiveLeadTeamDispatchesEveryLead sets a five-lead team, past the size the
// team once capped at. Every lead must get its own id, a roster name, and a team
// brief that counts five, and each must dispatch and finish its pass.
func TestFiveLeadTeamDispatchesEveryLead(t *testing.T) {
	script := `case "$1" in
  *"of 5 on this thread's orchestrator team"*) ;;
  *) printf '{"type":"error","text":"lead prompt lacks the five-lead brief"}\n'; exit 1 ;;
esac
printf '{"type":"text","text":"%s agrees"}\n' "$AT_AGENT_ID"`

	r, eng, ctx := newPromptHarness(t, script)
	startLead(t, r, ctx, 5)

	if err := New(r).RunTurn(ctx, "t1", "plan with a five-lead team"); err != nil {
		for _, e := range r.Logs().Entries("t1") {
			t.Logf("%s %s: %s", e.AgentID, e.Kind, e.Text)
		}
		t.Fatalf("run turn: %v", err)
	}

	state, _ := eng.State(ctx, "t1")
	if state.LeadCount() != 5 {
		t.Fatalf("LeadCount() = %d, want 5", state.LeadCount())
	}
	for i := 0; i < 5; i++ {
		id := leadAgentID(i)
		if !IsLead(id) {
			t.Fatalf("lead %d id %q not recognized as a lead", i+1, id)
		}
		if got := state.Agents[id].Status; got != contracts.AgentSucceeded {
			t.Fatalf("lead %d (%s) status = %s, want succeeded", i+1, id, got)
		}
	}
	if got := state.Agents[leadAgentID(4)].Name; got != "orchestrator-5" {
		t.Fatalf("lead 5 roster name = %q, want orchestrator-5", got)
	}
	if IsLead(leadAgentID(4) + "-1") {
		t.Fatal("a subagent id under a lead must not be treated as a lead")
	}
}
