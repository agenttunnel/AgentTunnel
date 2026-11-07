package orchestrator

import (
	"context"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

func TestSafeNameStripsUnsafeCharacters(t *testing.T) {
	cases := map[string]string{
		"packages-docs": "packages-docs",
		"../etc/passwd": "etc-passwd",
		"a b/c":         "a-b-c",
		"":              "task",
		"---":           "task",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A model-supplied task name must not be able to name an agent (and therefore a
// log file) outside the safe character set.
func TestToolDispatchUsesOnlySafeAgentIDs(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindDispatch,
		Name: "../../evil",
		Task: "do it",
	})

	tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator")
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	if tasks[0].Name != "evil" {
		t.Fatalf("name = %q, want evil", tasks[0].Name)
	}
	if tasks[0].AgentID != contracts.AgentID("agent-evil-1") {
		t.Fatalf("agent id = %q", tasks[0].AgentID)
	}
}
