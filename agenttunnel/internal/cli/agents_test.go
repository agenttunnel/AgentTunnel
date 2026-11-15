package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

var testRoster = []contracts.Agent{
	{ID: "agent-orchestrator", Role: "lead", Status: contracts.AgentRunning, Harness: "claude"},
	{ID: "agent-exec-1", Name: "exec one", Role: "executor", Status: contracts.AgentSucceeded, Harness: "codex"},
	{ID: "agent-exec-2", Name: "exec two", Role: "executor", Status: contracts.AgentFailed, Harness: "codex"},
	{ID: "agent-review", Name: "reviewer", Role: "reviewer", Status: contracts.AgentPending, Harness: "opencode"},
}

func TestMatchAgentResolvesIdNameAndPrefix(t *testing.T) {
	cases := map[string]contracts.AgentID{
		"agent-orchestrator": "agent-orchestrator",
		"orchestrator":       "agent-orchestrator", // id without its "agent-" prefix
		"exec one":           "agent-exec-1",       // exact name
		"exec-1":             "agent-exec-1",
		"agent-rev":          "agent-review", // unique id prefix
		"rev":                "agent-review",
	}
	for ref, want := range cases {
		got, err := matchAgent(testRoster, ref)
		if err != nil || got.ID != want {
			t.Fatalf("matchAgent(%q) = %q, %v; want %q", ref, got.ID, err, want)
		}
	}
}

func TestMatchAgentAmbiguityListsCandidates(t *testing.T) {
	_, err := matchAgent(testRoster, "agent-exec")
	if err == nil {
		t.Fatal("two executors share the prefix, so it must be ambiguous")
	}
	for _, want := range []string{"ambiguous", "agent-exec-1", "agent-exec-2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguity error %q missing %q", err, want)
		}
	}
}

func TestMatchAgentRejectsEmptyAndUnknown(t *testing.T) {
	if _, err := matchAgent(testRoster, "  "); err == nil {
		t.Fatal("an empty agent ref must fail")
	} else if ee, ok := err.(exitError); ok && ee.code != 2 {
		t.Fatalf("empty ref exit code = %d, want usage (2)", ee.code)
	}
	_, err := matchAgent(testRoster, "nobody")
	if err == nil || !strings.Contains(err.Error(), "no agent matches") {
		t.Fatalf("unknown agent error = %v", err)
	}
}

func TestAgentsTableRendersRolesAndStatus(t *testing.T) {
	raw, _ := json.Marshal(agentViews(testRoster))
	fake := &fakeCLICaller{results: map[string]string{"agents.list": string(raw)}}
	env, out := newUsageTestEnv()
	if err := renderAgents(context.Background(), env, fake, "th_1"); err != nil {
		t.Fatalf("renderAgents: %v", err)
	}
	got := out.String()
	for _, want := range []string{"ID", "NAME", "ROLE", "STATUS", "HARNESS", "agent-exec-2", "exec two", "executor", "failed", "codex", "agent-orchestrator"} {
		if !strings.Contains(got, want) {
			t.Fatalf("agents table missing %q:\n%s", want, got)
		}
	}
	// The unnamed lead shows a dash, not a blank that shifts the columns.
	if !strings.Contains(got, "agent-orchestrator  -") {
		t.Fatalf("unnamed agent should show -:\n%s", got)
	}
}

func TestAgentsJSONIsTheRosterArray(t *testing.T) {
	raw, _ := json.Marshal(agentViews(testRoster))
	fake := &fakeCLICaller{results: map[string]string{"agents.list": string(raw)}}
	env, out := newUsageTestEnv()
	env.JSON = true
	if err := renderAgents(context.Background(), env, fake, "th_1"); err != nil {
		t.Fatalf("renderAgents --json: %v", err)
	}
	var got []contracts.Agent
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json output is not an agent array: %v\n%s", err, out.String())
	}
	if len(got) != len(testRoster) || got[1].ID != "agent-exec-1" || got[1].Role != "executor" {
		t.Fatalf("json roster = %+v", got)
	}
}

func TestAgentsEmptyRosterIsAnEmptyJSONArray(t *testing.T) {
	fake := &fakeCLICaller{results: map[string]string{"agents.list": `[]`}}
	env, out := newUsageTestEnv()
	env.JSON = true
	if err := renderAgents(context.Background(), env, fake, "th_1"); err != nil {
		t.Fatalf("renderAgents: %v", err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("empty roster --json = %q, want []", out.String())
	}
}

// agentViews wraps agents the way agents.list does.
func agentViews(agents []contracts.Agent) []map[string]any {
	views := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		views = append(views, map[string]any{"agent": a, "usage": contracts.TokenUsage{}})
	}
	return views
}
