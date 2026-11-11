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
