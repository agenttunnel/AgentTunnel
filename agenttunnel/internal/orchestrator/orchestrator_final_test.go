package orchestrator

import (
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// orchestratorMessages returns the turn's final messages authored by the lead.
func orchestratorMessages(state *domain.ThreadState) []string {
	var out []string
	for _, m := range state.Messages {
		if m.Author == "orchestrator" {
			out = append(out, m.Text)
		}
	}
	return out
}

// runRecorded runs a turn against a script and records every lead prompt by
// pass number. Subagents run the same script, so scripts branch on AT_ROLE.
func runRecorded(t *testing.T, script string, leads int) (*domain.ThreadState, map[int]string) {
	t.Helper()
	var mu sync.Mutex
	prompts := map[int]string{}
	r, eng, ctx := newHarnessInvoking(t, func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		if spec.Role == contracts.RoleOrchestrator && spec.AgentID == orchestratorAgentID && passOf(spec) >= 0 {
			mu.Lock()
			prompts[passOf(spec)] = spec.Prompt
			mu.Unlock()
		}
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script}, PromptVia: "stdin"}, nil
	})
	if leads > 1 {
		startLead(t, r, ctx, leads)
	}
	if err := New(r).RunTurn(ctx, "t1", "please build the feature"); err != nil {
		t.Fatalf("run turn: %v", err)
	}
	state, _ := eng.State(ctx, "t1")
	return state, prompts
}

const dispatchEveryPass = `if [ "$AT_ROLE" = "orchestrator" ]; then
  printf '%s\n' "__AGENTTUNNEL_TOOL__ {\"name\":\"dispatch_task\",\"taskId\":\"impl$AT_PASS\",\"task\":\"work $AT_PASS\",\"role\":\"executor\"}"
  printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
else
  printf '{"type":"text","text":"done"}\n'
fi`

// The pass budget ends on a round that dispatched work, so no lead has read
// those results: one closing pass reads them and its text is the final message.
// What the closing pass prints is not dispatched.
func TestClosingPassReportsOnWorkDispatchedInLastRound(t *testing.T) {
	state, prompts := runRecorded(t, dispatchEveryPass, 1)

	closing, ok := prompts[2]
	if !ok {
		t.Fatalf("no closing pass ran; passes seen: %v", prompts)
	}
	if !strings.Contains(closing, "## Final report") || strings.Contains(closing, "## Dispatching work") || strings.Contains(closing, "Dispatch the work first") {
		t.Fatalf("closing prompt is not a final-report prompt:\n%s", closing)
	}
	if strings.Contains(prompts[1], "## Final report") {
		t.Fatal("a regular pass got the final-report section")
	}
	msgs := orchestratorMessages(state)
	if len(msgs) != 1 || msgs[0] != "report-2" {
		t.Fatalf("final messages = %q, want the closing pass's report-2", msgs)
	}
	if _, ran := state.Agents["agent-impl2"]; ran {
		t.Fatal("work printed by the closing pass was dispatched")
	}
	if state.Agents["agent-orchestrator"].Status != contracts.AgentSucceeded {
		t.Fatalf("lead status = %s", state.Agents["agent-orchestrator"].Status)
	}
}

// A last round that dispatched nothing means the lead already read every
// result, so no closing pass runs.
func TestNoClosingPassWhenLastRoundDispatchedNothing(t *testing.T) {
	script := `if [ "$AT_ROLE" = "orchestrator" ]; then
  if [ "$AT_PASS" = "0" ]; then
    printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"impl","task":"work","role":"executor"}'
  fi
  printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
else
  printf '{"type":"text","text":"done"}\n'
fi`
	state, prompts := runRecorded(t, script, 1)
	if len(prompts) != 2 {
		t.Fatalf("passes seen = %v, want exactly passes 0 and 1", prompts)
	}
	if msgs := orchestratorMessages(state); len(msgs) != 1 || msgs[0] != "report-1" {
		t.Fatalf("final messages = %q", msgs)
	}
}

// A later pass with nothing to say must not erase what an earlier pass wrote.
func TestEmptyLaterPassKeepsEarlierText(t *testing.T) {
	script := `if [ "$AT_ROLE" = "orchestrator" ]; then
  if [ "$AT_PASS" = "0" ]; then
    printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"impl","task":"work","role":"executor"}'
    printf '{"type":"text","text":"plan-text"}\n'
  fi
else
  printf '{"type":"text","text":"done"}\n'
fi`
	state, _ := runRecorded(t, script, 1)
	if msgs := orchestratorMessages(state); len(msgs) != 1 || msgs[0] != "plan-text" {
		t.Fatalf("final messages = %q, want the earlier pass's plan-text", msgs)
	}
}

// Every lead's summary reaches the final message, named, in lead order.
func TestTwoLeadsSummariesBothReported(t *testing.T) {
	script := `printf '{"type":"text","text":"summary-from-%s"}\n' "$AT_AGENT_ID"`
	state, _ := runRecorded(t, script, 2)
	msgs := orchestratorMessages(state)
	if len(msgs) != 1 {
		t.Fatalf("final messages = %q", msgs)
	}
	first := strings.Index(msgs[0], "orchestrator:\nsummary-from-agent-orchestrator")
	second := strings.Index(msgs[0], "orchestrator-2:\nsummary-from-agent-orchestrator-lead2")
	if first < 0 || second < first {
		t.Fatalf("final message does not carry both leads in order: %q", msgs[0])
	}
}

// A task that failed is said so in the final message instead of the lead's
// pre-result text standing alone.
func TestFinalMessageNotesFailedTasks(t *testing.T) {
	script := `if [ "$AT_ROLE" = "orchestrator" ]; then
  if [ "$AT_PASS" = "0" ]; then
    printf '%s\n' '__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"impl","task":"work","role":"executor"}'
  fi
  printf '{"type":"text","text":"report-%s"}\n' "$AT_PASS"
else
  printf '{"type":"error","text":"boom"}\n'
  exit 1
fi`
	state, _ := runRecorded(t, script, 1)
	msgs := orchestratorMessages(state)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "report-1") || !strings.Contains(msgs[0], "Note: 1 task(s) failed or were cancelled: impl") {
		t.Fatalf("final messages = %q", msgs)
	}
}

func TestFinalPromptAsksForVerificationNotDispatch(t *testing.T) {
	prompt := BuildOrchestratorPrompt(OrchestratorPrompt{
		Role: contracts.Role{SystemPrompt: "lead"}, UserMessage: "go", Pass: 2, Final: true,
		PeerMailbox: true, Steer: true, Revive: true, Subthreads: true, RuntimeChoices: "- x",
	})
	for _, banned := range []string{"## Dispatching work", "tool dispatch", "tool message", "tool steer", "tool revive", "tool subthread", "## Runtime choices", "Dispatch the work first", "This is pass"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("final prompt still carries %q", banned)
		}
	}
	for _, want := range []string{"## Final report", "not met or unverified"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("final prompt missing %q", want)
		}
	}
}
