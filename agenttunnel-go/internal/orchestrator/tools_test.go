package orchestrator

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

func TestToolDispatchFromInboxBecomesATask(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindDispatch,
		Name: "explore",
		Role: contracts.RolePlanner,
		Task: "map the repository",
	})

	tasks := New(r).toolTasks("t1", "agent-orchestrator")
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	got := tasks[0]
	if got.Name != "explore" || got.Role != contracts.RolePlanner || got.Task != "map the repository" {
		t.Fatalf("task = %+v", got)
	}
	if got.ParentAgentID != "agent-orchestrator" {
		t.Fatalf("parent = %q", got.ParentAgentID)
	}
	// The inbox is drained, so a second pass sees nothing.
	if again := New(r).toolTasks("t1", "agent-orchestrator"); len(again) != 0 {
		t.Fatalf("inbox was not drained: %+v", again)
	}
}

func TestToolDispatchDefaultsRoleToExecutor(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindDispatch, Task: "do it"})

	tasks := New(r).toolTasks("t1", "agent-orchestrator")
	if len(tasks) != 1 || tasks[0].Role != contracts.RoleExecutor {
		t.Fatalf("tasks = %+v", tasks)
	}
	if tasks[0].Name == "" || tasks[0].AgentID == "" {
		t.Fatalf("task should carry a generated name and id: %+v", tasks[0])
	}
}

func TestToolNoteLandsInTheNotebook(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindNote, AgentID: "agent-x", Text: "the config lives in ~/.agenttunnel"})

	New(r).toolTasks("t1", "agent-orchestrator")

	notes := r.notebook.Get("notes")
	if len(notes) != 1 || notes[0].Value != "the config lives in ~/.agenttunnel" {
		t.Fatalf("notes = %+v", notes)
	}
}
