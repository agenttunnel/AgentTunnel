package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

func TestRunnerRefusesDisabledRole(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.settings.SetRoleEnabled(contracts.RolePlanner, false)

	tasks := []contracts.DispatchTask{
		{AgentID: "agent-a", Name: "a", Role: contracts.RolePlanner, Task: "x"},
	}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}

	state, _ := eng.State(ctx, "t1")
	if state.Agents["agent-a"].Status != contracts.AgentFailed {
		t.Fatalf("status = %s, want failed", state.Agents["agent-a"].Status)
	}
}

func TestRequiredRoleRunsEvenIfDisabledInSettings(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	// A hand-edited settings file can disable a required role; it must still run.
	r.settings.SetRoleEnabled(contracts.RoleExecutor, false)

	tasks := []contracts.DispatchTask{
		{AgentID: "agent-a", Name: "a", Role: contracts.RoleExecutor, Task: "x"},
	}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatal(err)
	}

	state, _ := eng.State(ctx, "t1")
	if state.Agents["agent-a"].Status != contracts.AgentSucceeded {
		t.Fatalf("status = %s, want succeeded", state.Agents["agent-a"].Status)
	}
}

func TestLeadDispatchOfDisabledRoleRefused(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.settings.SetRoleEnabled(contracts.RolePlanner, false)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindDispatch, AgentID: "agent-orchestrator",
		Role: contracts.RolePlanner, Name: "plan", Task: "plan the work",
	})

	if tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil); len(tasks) != 0 {
		t.Fatalf("a disabled role must not become a task: %+v", tasks)
	}
	var logged bool
	for _, e := range r.logs.Entries("t1") {
		if e.Kind == contracts.LogError && strings.Contains(e.Text, "disabled") {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("lead log has no refusal line: %+v", r.logs.Entries("t1"))
	}
}

func TestSubagentSpawnOfDisabledRoleRefused(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.settings.SetRoleEnabled(contracts.RolePlanner, false)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Role: contracts.RolePlanner, Name: "planner-2", Task: "plan more",
	})

	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("a disabled role must not pend: %+v", got)
	}
	if notes := r.notebookFor("t1").Get("notes"); len(notes) != 1 || !strings.Contains(notes[0].Value, "Refused") {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestCouncilRefusesDisabledMemberRole(t *testing.T) {
	r, _, ctx := newHarness(t, councilScript)
	r.settings.SetRoleEnabled(contracts.RolePlanner, false)

	_, err := New(r).RunCouncil(ctx, "t1", CouncilSpec{
		Topic: "should we split the parser?",
		Members: []CouncilMember{
			{Name: "planner", Role: contracts.RolePlanner},
			{Name: "reviewer", Role: contracts.RoleReviewer},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v, want a disabled-role refusal", err)
	}
}

// The lead is told which roles it may dispatch and which non-required roles are
// off. Its own role and the council-internal facilitator are never listed, and
// experimental roles follow the experimental setting.
func TestRoleChoicesFollowSettings(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)

	usable, disabled := r.roleChoices()
	if usable != "planner|executor|tester|reviewer" || disabled != "context-manager, wildcard" {
		t.Fatalf("defaults: usable %q, disabled %q", usable, disabled)
	}

	r.settings.SetRoleEnabled(contracts.RolePlanner, false)
	r.settings.SetRoleEnabled(contracts.RoleTester, false)
	r.settings.SetExperimentalRoles(true)
	usable, disabled = r.roleChoices()
	if usable != "executor|reviewer|context-manager|wildcard" || disabled != "planner, tester" {
		t.Fatalf("after disabling planner/tester and enabling experimental: usable %q, disabled %q", usable, disabled)
	}
}

// The wildcard role is experimental: a lead's dispatch is refused and a run
// fails while experimental roles are off, and both succeed once they are on.
func TestWildcardRequiresExperimentalRoles(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	off := []contracts.DispatchTask{
		{AgentID: "agent-w1", Name: "w1", Role: contracts.RoleWildcard, Brief: "You count.", Task: "count"},
	}
	if err := r.Dispatch(ctx, "t1", off); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", off); err != nil {
		t.Fatal(err)
	}
	state, _ := eng.State(ctx, "t1")
	if state.Agents["agent-w1"].Status != contracts.AgentFailed {
		t.Fatalf("wildcard with experimental off: status = %s, want failed", state.Agents["agent-w1"].Status)
	}

	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindDispatch, AgentID: "agent-orchestrator",
		Role: contracts.RoleWildcard, Name: "w2", Brief: "You count.", Task: "count",
	})
	if tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil); len(tasks) != 0 {
		t.Fatalf("lead dispatch of wildcard with experimental off became a task: %+v", tasks)
	}

	r.settings.SetExperimentalRoles(true)
	on := []contracts.DispatchTask{
		{AgentID: "agent-w3", Name: "w3", Role: contracts.RoleWildcard, Brief: "You count.", Task: "count"},
	}
	if err := r.Dispatch(ctx, "t1", on); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", on); err != nil {
		t.Fatal(err)
	}
	state, _ = eng.State(ctx, "t1")
	if state.Agents["agent-w3"].Status != contracts.AgentSucceeded {
		t.Fatalf("wildcard with experimental on: status = %s, want succeeded", state.Agents["agent-w3"].Status)
	}
}

// A wildcard dispatch with no brief is refused in the lead's log and mints no
// task, and a run reached without one fails rather than running a blank agent.
func TestWildcardWithoutBriefIsRefused(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.settings.SetExperimentalRoles(true)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindDispatch, AgentID: "agent-orchestrator",
		Role: contracts.RoleWildcard, Name: "w", Task: "count",
	})

	if tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil); len(tasks) != 0 {
		t.Fatalf("a wildcard without a brief must not become a task: %+v", tasks)
	}
	var logged bool
	for _, e := range r.logs.Entries("t1") {
		if e.Kind == contracts.LogError && strings.Contains(e.Text, "--brief") {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("lead log has no brief refusal: %+v", r.logs.Entries("t1"))
	}

	bare := []contracts.DispatchTask{{AgentID: "agent-w", Name: "w", Role: contracts.RoleWildcard, Brief: "  ", Task: "count"}}
	if err := r.Dispatch(ctx, "t1", bare); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, "t1", bare); err != nil {
		t.Fatal(err)
	}
	state, _ := eng.State(ctx, "t1")
	if state.Agents["agent-w"].Status != contracts.AgentFailed {
		t.Fatalf("status = %s, want failed", state.Agents["agent-w"].Status)
	}
}

// Only the team lead defines wildcard agents: a subagent's proposal is refused
// outright, so it never reaches the lead's pending list.
func TestSubagentSpawnOfWildcardRefused(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.settings.SetExperimentalRoles(true)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Role: contracts.RoleWildcard, Name: "custom", Brief: "You count.", Task: "count",
	})

	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("a subagent's wildcard must not pend: %+v", got)
	}
	if notes := r.notebookFor("t1").Get("notes"); len(notes) != 1 || !strings.Contains(notes[0].Value, "only the team lead may define wildcard agents") {
		t.Fatalf("notes = %+v", notes)
	}
}
