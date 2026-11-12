package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

const (
	describeThreadID = "th_a1b2c3d4e5f60718"
	describeParentID = "th_0f1e2d3c4b5a6978"
	describeSubID    = "th_1111222233334444"
)

var describeT0 = time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)

// describeFixture is a thread with a parent, one subthread, a running lead and
// a finished executor in a pipeline, a task, and two events of which only the
// newest describeEventLimit would show.
func describeFixture(t *testing.T) *fakeCLICaller {
	t.Helper()
	st := domain.ThreadState{
		ID:             describeThreadID,
		Exists:         true,
		Title:          "Fix flaky test",
		ProjectID:      "proj_1",
		ParentThreadID: describeParentID,
		Worktree:       &contracts.WorktreeRef{Path: "/wt/fix", Branch: "agenttunnel/fix-a1b2", Base: "main"},
		Turn:           2,
		PeerMailbox:    true,
		UpdatedAt:      describeT0.Add(time.Hour),
		Messages:       []domain.Message{{ID: "m1", Author: "user", Text: "fix it", At: describeT0}},
		Tasks:          []domain.TaskItem{{Name: "t1", Title: "reproduce", Status: contracts.TaskDone}},
		Subthreads:     []contracts.ThreadID{describeSubID},
		Agents: map[contracts.AgentID]contracts.Agent{
			"agent-orchestrator": {
				ID: "agent-orchestrator", Role: "lead", Harness: "claude", Model: "sonnet",
				Status: contracts.AgentRunning, StartedAt: describeT0,
			},
			"agent-exec-1": {
				ID: "agent-exec-1", ParentAgentID: "agent-orchestrator", PipelineID: "pipe-a",
				Name: "fixer", Role: "executor", Harness: "codex", Model: "gpt-5",
				Status: contracts.AgentSucceeded, Task: "patch the race", DependsOn: []string{"t1"},
				StartedAt: describeT0, EndedAt: ptrTime(describeT0.Add(10 * time.Minute)),
			},
		},
		AgentOrder: []contracts.AgentID{"agent-orchestrator", "agent-exec-1"},
		Usage: map[contracts.AgentID]contracts.AgentUsage{
			"agent-orchestrator": {AgentID: "agent-orchestrator", Role: "lead", Usage: contracts.TokenUsage{InputTokens: 1200, OutputTokens: 300, CostUSD: 0.25, ToolUses: 2}},
			"agent-exec-1":       {AgentID: "agent-exec-1", Role: "executor", Usage: contracts.TokenUsage{InputTokens: 800, OutputTokens: 100, CostUSD: 0.05, ToolUses: 3}},
		},
	}
	stRaw, _ := json.Marshal(st)

	rows := []threadListRow{
		{ID: describeThreadID, Title: "Fix flaky test", ProjectID: "proj_1"},
		{ID: describeParentID, Title: "Parent plan"},
		{ID: describeSubID, Title: "Sub review", ParentThreadID: describeThreadID},
	}
	rowsRaw, _ := json.Marshal(rows)

	events := make([]describeEvent, 0, describeEventLimit+2)
	for i := 1; i <= describeEventLimit+2; i++ {
		payload, _ := json.Marshal(map[string]any{"agentId": "agent-exec-1", "status": "running", "n": i})
		events = append(events, describeEvent{
			Seq: int64(i), Type: string(contracts.EvtAgentStatusChanged),
			At: describeT0.Add(time.Duration(i) * time.Second), Payload: payload,
		})
	}
	eventsRaw, _ := json.Marshal(events)

	projectsRaw := `[{"id":"proj_1","name":"AgentTunnel","path":"/src/agenttunnel"}]`
	return &fakeCLICaller{results: map[string]string{
		"thread.list":   string(rowsRaw),
		"thread.get":    string(stRaw),
		"thread.events": string(eventsRaw),
		"project.list":  projectsRaw,
	}}
}

// normalizedLines collapses each line's runs of spaces so assertions do not
// depend on column padding, and drops blank lines.
func normalizedLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			lines = append(lines, strings.Join(f, " "))
		}
	}
	return lines
}

func assertHasLines(t *testing.T, out string, want ...string) {
	t.Helper()
	got := normalizedLines(out)
	set := make(map[string]bool, len(got))
	for _, line := range got {
		set[line] = true
	}
	for _, line := range want {
		if !set[line] {
			t.Fatalf("output missing line %q:\n%s", line, out)
		}
	}
}

func TestDescribeThreadTextShowsDefinition(t *testing.T) {
	env, out := newUsageTestEnv()
	if err := renderThreadDescribe(context.Background(), env, describeFixture(t), describeThreadID, "text"); err != nil {
		t.Fatalf("renderThreadDescribe: %v", err)
	}
	assertHasLines(t, out.String(),
		"Name: Fix flaky test",
		"ID: "+describeThreadID,
		`Parent: `+describeParentID+` ("Parent plan")`,
		"Project: AgentTunnel",
		"Worktree: /wt/fix (branch agenttunnel/fix-a1b2, base main)",
		"Status: running",
		"Created: "+describeT0.Local().Format(describeTimeLayout),
		"Turns: 2",
		"Roles: lead, executor",
		"Peer mailbox: enabled",
		`Subthreads: `+describeSubID+` ("Sub review")`,
		"Harness/model: claude/sonnet",
		"t1 done reproduce",
	)
	if !strings.Contains(out.String(), "cost $0.3000") || !strings.Contains(out.String(), "tools 5") {
		t.Fatalf("spend line should total both agents:\n%s", out.String())
	}
}

func TestDescribeThreadAgentsTableShowsParentAndPipeline(t *testing.T) {
	env, out := newUsageTestEnv()
	if err := renderThreadDescribe(context.Background(), env, describeFixture(t), describeThreadID, "text"); err != nil {
		t.Fatalf("renderThreadDescribe: %v", err)
	}
	assertHasLines(t, out.String(),
		"ID NAME ROLE STATUS HARNESS PARENT PIPELINE",
		"agent-orchestrator - lead running claude - -",
		"agent-exec-1 fixer executor succeeded codex agent-orchestrator pipe-a",
	)
}

func TestDescribeThreadEventsAreTheNewestRows(t *testing.T) {
	env, out := newUsageTestEnv()
	if err := renderThreadDescribe(context.Background(), env, describeFixture(t), describeThreadID, "text"); err != nil {
		t.Fatalf("renderThreadDescribe: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Events:") || strings.Contains(got, `"n":1,`) {
		t.Fatalf("events section should keep only the newest %d rows:\n%s", describeEventLimit, got)
	}
	if !strings.Contains(got, `"n":12,`) || strings.Count(got, string(contracts.EvtAgentStatusChanged)) != describeEventLimit {
		t.Fatalf("expected the last %d status events:\n%s", describeEventLimit, got)
	}
}

func TestDescribeThreadEmptySectionsReadNone(t *testing.T) {
	st := domain.ThreadState{ID: describeThreadID, Exists: true, Title: "Idle", Draft: true}
	stRaw, _ := json.Marshal(st)
	rowsRaw, _ := json.Marshal([]threadListRow{{ID: describeThreadID, Title: "Idle"}})
	fake := &fakeCLICaller{results: map[string]string{
		"thread.list": string(rowsRaw), "thread.get": string(stRaw),
		"thread.events": `[]`, "project.list": `[]`,
	}}
	env, out := newUsageTestEnv()
	if err := renderThreadDescribe(context.Background(), env, fake, "Idle", "text"); err != nil {
		t.Fatalf("renderThreadDescribe: %v", err)
	}
	assertHasLines(t, out.String(),
		"Parent: <none>",
		"Project: -",
		"Worktree: <project checkout>",
		"Status: draft",
		"Roles: <none>",
		"Subthreads: <none>",
		"Agents:",
		"Tasks:",
		"Events:",
	)
	if n := strings.Count(out.String(), "<none>"); n != 6 {
		t.Fatalf("empty sections should each read <none>, got %d:\n%s", n, out.String())
	}
}

func TestDescribeThreadJSONCarriesTheDefinition(t *testing.T) {
	env, out := newUsageTestEnv()
	env.JSON = true
	if err := renderThreadDescribe(context.Background(), env, describeFixture(t), describeThreadID, "json"); err != nil {
		t.Fatalf("renderThreadDescribe -o json: %v", err)
	}
	var got threadDescription
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output is not a thread description: %v\n%s", err, out.String())
	}
	if got.ID != describeThreadID || got.Status != "running" || got.Turns != 2 || !got.PeerMailbox {
		t.Fatalf("json description = %+v", got)
	}
	if got.Parent == nil || got.Parent.ID != describeParentID || len(got.Agents) != 2 || len(got.Events) != describeEventLimit {
		t.Fatalf("json relatives/agents/events = parent %+v, %d agents, %d events", got.Parent, len(got.Agents), len(got.Events))
	}
}

func TestDescribeYAMLishIsSortedAndNested(t *testing.T) {
	env, out := newUsageTestEnv()
	if err := renderThreadDescribe(context.Background(), env, describeFixture(t), describeThreadID, "yaml"); err != nil {
		t.Fatalf("renderThreadDescribe -o yaml: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		`id: "` + describeThreadID + `"`,
		`peerMailbox: true`,
		`turns: 2`,
		"roles:\n  - \"lead\"\n  - \"executor\"",
		"parent:\n  id: \"" + describeParentID + "\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("yaml output missing %q:\n%s", want, got)
		}
	}
	// Keys are sorted, so "agents" precedes "id" at the top level.
	if strings.Index(got, "agents:") > strings.Index(got, "\nid:") {
		t.Fatalf("top-level keys should be sorted:\n%s", got)
	}
}

func TestDescribeAgentShowsDefinitionAndSpend(t *testing.T) {
	env, out := newUsageTestEnv()
	st := describeStateFor(t)
	agent, err := matchAgent(rosterOf(st), "fixer")
	if err != nil {
		t.Fatalf("matchAgent: %v", err)
	}
	desc := describeAgent(st, agent)
	desc.writeText(env.Out)
	assertHasLines(t, out.String(),
		"Name: fixer",
		"ID: agent-exec-1",
		`Thread: `+describeThreadID+` ("Fix flaky test")`,
		"Parent: agent-orchestrator",
		"Pipeline: pipe-a",
		"Role: executor",
		"Harness/model: codex/gpt-5",
		"Status: succeeded",
		"Depends on: t1",
		"Task:",
		"patch the race",
	)
	if !strings.Contains(out.String(), "cost $0.0500") || !strings.Contains(out.String(), "tools 3") {
		t.Fatalf("agent spend should be its own usage:\n%s", out.String())
	}
}

func TestDescribeAgentThroughRPCResolvesBothRefs(t *testing.T) {
	env, out := newUsageTestEnv()
	env.JSON = true
	if err := renderAgentDescribe(context.Background(), env, describeFixture(t), "a1b2c3", "exec one", "json"); err == nil {
		t.Fatal("an unnamed-agent reference that matches nothing must fail")
	}
	fake := describeFixture(t)
	if err := renderAgentDescribe(context.Background(), env, fake, "a1b2c3", "fixer", "json"); err != nil {
		t.Fatalf("renderAgentDescribe: %v", err)
	}
	var got agentDescription
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output is not an agent description: %v\n%s", err, out.String())
	}
	if got.ID != "agent-exec-1" || got.Thread.ID != describeThreadID || got.Usage.CostUSD != 0.05 {
		t.Fatalf("agent json = %+v", got)
	}
	if got.Pipeline != "pipe-a" || got.Parent != "agent-orchestrator" || len(got.DependsOn) != 1 {
		t.Fatalf("agent relations = %+v", got)
	}
}

func TestDescribeThreadRefIsAmbiguousAndUnknown(t *testing.T) {
	env, _ := newUsageTestEnv()
	if err := renderThreadDescribe(context.Background(), env, describeFixture(t), "th_", "text"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("a shared prefix must be ambiguous, got %v", err)
	}
	if err := renderThreadDescribe(context.Background(), env, describeFixture(t), "nothing-like-this", "text"); err == nil || !strings.Contains(err.Error(), "no thread matches") {
		t.Fatalf("an unknown thread must fail, got %v", err)
	}
}

func TestParseDescribeArgs(t *testing.T) {
	env := &Env{}
	pos, format, err := parseDescribeArgs("u", []string{"th_1", "-o", "yaml"}, env, 1)
	if err != nil || len(pos) != 1 || format != "yaml" {
		t.Fatalf("-o yaml = %v %q %v", pos, format, err)
	}
	_, format, err = parseDescribeArgs("u", []string{"--output=json", "th_1"}, env, 1)
	if err != nil || format != "json" {
		t.Fatalf("--output=json = %q %v", format, err)
	}
	env.JSON = true
	_, format, _ = parseDescribeArgs("u", []string{"th_1"}, env, 1)
	if format != "json" {
		t.Fatalf("global --json should select json, got %q", format)
	}
	for _, args := range [][]string{
		{},                    // missing thread
		{"a", "b"},            // too many
		{"th_1", "-o", "xml"}, // bad format
		{"th_1", "-o"},        // -o without a value
		{"th_1", "--wide"},    // unknown flag
	} {
		if _, _, err := parseDescribeArgs("u", args, &Env{}, 1); err == nil {
			t.Fatalf("parseDescribeArgs(%v) should fail", args)
		} else if ee, ok := err.(exitError); !ok || ee.code != 2 {
			t.Fatalf("parseDescribeArgs(%v) exit = %v, want usage 2", args, err)
		}
	}
}

func TestInspectIsAnAliasOfDescribe(t *testing.T) {
	var describe, inspect *Command
	for i := range registry {
		switch registry[i].Name {
		case "describe":
			describe = &registry[i]
		case "inspect":
			inspect = &registry[i]
		}
	}
	if describe == nil || inspect == nil {
		t.Fatal("describe and inspect must both be registered")
	}
	for _, sub := range []string{"thread", "agent"} {
		if _, ok := findCommand(inspect.Subcommands, sub); !ok {
			t.Fatalf("inspect is missing the %q subcommand", sub)
		}
		if _, ok := findCommand(describe.Subcommands, sub); !ok {
			t.Fatalf("describe is missing the %q subcommand", sub)
		}
	}
}

// describeStateFor is the fixture's thread state, read back the way the
// command reads it.
func describeStateFor(t *testing.T) *domain.ThreadState {
	t.Helper()
	var st domain.ThreadState
	raw := describeFixture(t).results["thread.get"]
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("decode fixture state: %v", err)
	}
	return &st
}

func ptrTime(t time.Time) *time.Time { return &t }
