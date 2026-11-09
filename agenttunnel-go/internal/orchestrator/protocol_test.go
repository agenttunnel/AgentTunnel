package orchestrator

import (
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestParseToolLine(t *testing.T) {
	line := `__AGENTTUNNEL_TOOL__ {"name":"dispatch_task","taskId":"impl","task":"do it","role":"executor","dependsOn":["x"]}`
	call, ok := ParseToolLine(line)
	if !ok {
		t.Fatal("expected a tool call")
	}
	if call.Name != "dispatch_task" || call.TaskID != "impl" || call.Role != "executor" || len(call.DependsOn) != 1 {
		t.Fatalf("parsed = %+v", call)
	}
}

func TestParseToolLineRejectsGarbage(t *testing.T) {
	for _, line := range []string{
		"",
		"no prefix here",
		"__AGENTTUNNEL_TOOL__ not-json",
		`__AGENTTUNNEL_TOOL__ {"task":"missing name"}`,
	} {
		if _, ok := ParseToolLine(line); ok {
			t.Fatalf("expected reject: %q", line)
		}
	}
}

func TestToDispatchTasks(t *testing.T) {
	calls := []ToolCall{
		{Name: "dispatch_task", TaskID: "impl", Task: "implement", Role: "executor"},
		{Name: "note_finding"},
		{Name: "dispatch_task", Task: "review", Role: "reviewer", DependsOn: []string{"impl"}},
	}
	got := ToDispatchTasks("orchestrator", calls, func(string) bool { return false })
	if len(got) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(got))
	}
	if got[0].AgentID != "agent-impl" || got[0].ParentAgentID != "orchestrator" {
		t.Fatalf("task 0 = %+v", got[0])
	}
	if got[1].Name == "" || got[1].Name == got[0].Name || got[1].AgentID != contracts.AgentID("agent-"+got[1].Name) {
		t.Fatalf("task 1 = %+v, want a generated name distinct from task 0", got[1])
	}
	if !strings.Contains(got[1].Name, "_") {
		t.Fatalf("task 1 name %q is not docker-style", got[1].Name)
	}
	if len(Dispatches(calls)) != 2 {
		t.Fatal("Dispatches should return only dispatch_task calls")
	}
}

func TestBuiltinRolesAreRegistered(t *testing.T) {
	roles := contracts.NewRoleRegistry(contracts.BuiltinRoles()...)
	for _, id := range []contracts.RoleID{
		contracts.RoleOrchestrator, contracts.RolePlanner,
		contracts.RoleExecutor, contracts.RoleTester, contracts.RoleReviewer,
	} {
		if _, ok := roles.Get(id); !ok {
			t.Errorf("role %q missing", id)
		}
	}
}

func TestCustomRoleCanBeRegistered(t *testing.T) {
	roles := contracts.NewRoleRegistry(contracts.BuiltinRoles()...)
	if err := roles.Register(contracts.Role{
		ID:             "security",
		Name:           "Security Reviewer",
		SystemPrompt:   "You audit for OWASP top 10 issues.",
		DefaultHarness: contracts.HarnessClaude,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := roles.Get("security"); !ok {
		t.Fatal("custom role not found")
	}
}
