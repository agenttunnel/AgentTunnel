package orchestrator

import (
	"slices"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

func TestGenerateAgentNameIsDockerStyleAndDeterministic(t *testing.T) {
	never := func(string) bool { return false }
	name := generateAgentName("agent-1", never)
	if again := generateAgentName("agent-1", never); again != name {
		t.Fatalf("same seed gave %q then %q", name, again)
	}
	adj, sci, ok := strings.Cut(name, "_")
	if !ok || !slices.Contains(nameAdjectives[:], adj) || !slices.Contains(nameScientists[:], sci) {
		t.Fatalf("name %q is not adjective_scientist", name)
	}
}

func TestGenerateAgentNameSkipsTakenNames(t *testing.T) {
	first := generateAgentName("agent-1", func(string) bool { return false })
	got := generateAgentName("agent-1", func(name string) bool { return name == first })
	if got == first || got == "" {
		t.Fatalf("got %q with %q taken", got, first)
	}
}

func TestGenerateAgentNameSuffixesWhenEveryPairIsTaken(t *testing.T) {
	got := generateAgentName("agent-1", func(name string) bool { return !strings.Contains(name, "-") })
	if !strings.Contains(got, "_") || !strings.HasSuffix(got, "-2") {
		t.Fatalf("got %q, want a suffixed docker name", got)
	}
}

// Unnamed dispatches get names the thread does not already use. The first
// unnamed dispatch of a fresh orchestrator is seeded "agent-1", so an earlier
// pass's agent is recorded under exactly that name to force a collision that
// only the thread state can catch.
func TestUnnamedDispatchAvoidsNamesAlreadyInThread(t *testing.T) {
	r, _, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	clash := generateAgentName("agent-1", func(string) bool { return false })
	if err := r.Dispatch(ctx, "t1", []contracts.DispatchTask{
		{AgentID: "agent-earlier", Name: clash, Role: contracts.RoleExecutor, Task: "earlier"},
	}); err != nil {
		t.Fatal(err)
	}

	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindDispatch, Task: "later"})
	tasks := New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	if tasks[0].Name == clash {
		t.Fatalf("unnamed dispatch reused %q already held by an earlier agent", clash)
	}
}

// Two unnamed dispatches in one drain never share a name.
func TestUnnamedDispatchesInOneDrainDiffer(t *testing.T) {
	r, _, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindDispatch, Task: "first"})
	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindDispatch, Task: "second"})
	tasks := New(r).toolTasks(ctx, "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 2 || tasks[0].Name == tasks[1].Name {
		t.Fatalf("names = %+v, want two distinct names", tasks)
	}
}
