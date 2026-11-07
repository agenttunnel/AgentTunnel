package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

func TestSpawnStorePartitionsByThread(t *testing.T) {
	s := newSpawnStore()
	s.add("t1", spawnRequest{RequesterID: "agent-a", Name: "inner", Task: "x"})

	if got := s.list("t2"); len(got) != 0 {
		t.Fatalf("thread t2 should see no requests: %+v", got)
	}
	got := s.list("t1")
	if len(got) != 1 || got[0].Name != "inner" {
		t.Fatalf("t1 requests = %+v", got)
	}
	s.clear("t1")
	if got := s.list("t1"); len(got) != 0 {
		t.Fatalf("clear left requests behind: %+v", got)
	}
}

func TestSpawnStoreSuffixesCollidingNames(t *testing.T) {
	s := newSpawnStore()
	s.add("t1", spawnRequest{RequesterID: "agent-a", Name: "inner", Task: "x"})
	if !s.add("t1", spawnRequest{RequesterID: "agent-b", Name: "inner", Task: "y"}) {
		t.Fatal("a request from another agent with the same name should still be recorded")
	}
	got := s.list("t1")
	if len(got) != 2 {
		t.Fatalf("requests = %+v", got)
	}
	if got[1].Name != "inner-2" {
		t.Fatalf("colliding name should be suffixed: %+v", got[1])
	}
}

func TestSpawnStoreDropsDuplicateRequest(t *testing.T) {
	s := newSpawnStore()
	s.add("t1", spawnRequest{RequesterID: "agent-a", Name: "inner", Task: "x"})
	if s.add("t1", spawnRequest{RequesterID: "agent-a", Name: "inner", Task: "x"}) {
		t.Fatal("an echoed request should not be recorded twice")
	}
	if got := s.list("t1"); len(got) != 1 {
		t.Fatalf("requests = %+v", got)
	}
}

func TestSubagentSpawnCallBecomesPendingRequest(t *testing.T) {
	var mu sync.Mutex
	var audit []string
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.audit = func(kind string, _ map[string]any) {
		mu.Lock()
		audit = append(audit, kind)
		mu.Unlock()
	}
	r.tools.Post("t1", toolbox.Call{
		Kind:    toolbox.KindSpawn,
		AgentID: "agent-outer-1",
		Name:    "inner",
		Task:    "write the inner tests",
	})

	tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 0 {
		t.Fatalf("an unapproved spawn must not become a task: %+v", tasks)
	}
	pending := r.spawns.list("t1")
	if len(pending) != 1 || pending[0].RequesterID != "agent-outer-1" ||
		pending[0].Name != "inner" || pending[0].Task != "write the inner tests" {
		t.Fatalf("pending = %+v", pending)
	}
	if pending[0].Role != contracts.RoleExecutor {
		t.Fatalf("role should default to executor: %+v", pending[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(audit) == 0 || audit[0] != "spawn.requested" {
		t.Fatalf("audit = %v", audit)
	}
}

// The pre-existing bypass: a subagent running `tool dispatch` used to create an
// unapproved sibling directly. It must now pend like any other proposal.
func TestSubagentDispatchCallBecomesPendingRequest(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind:    toolbox.KindDispatch,
		AgentID: "agent-outer-1",
		Name:    "sibling",
		Task:    "do the sibling work",
	})

	tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 0 {
		t.Fatalf("subagent dispatch must not run unapproved: %+v", tasks)
	}
	if got := r.spawns.list("t1"); len(got) != 1 || got[0].Name != "sibling" {
		t.Fatalf("pending = %+v", got)
	}
}

func TestLeadSpawnCallBehavesAsDispatch(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	// A printed lead line carries the lead's agent id, and a lead HTTP call
	// sends it explicitly; both shapes must dispatch directly.
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, Name: "direct", Task: "run me now",
	})
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-orchestrator",
		Name: "direct", Task: "run me again",
	})

	tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v", tasks)
	}
	for _, task := range tasks {
		if task.ParentAgentID != "agent-orchestrator" {
			t.Fatalf("lead spawn should parent on the lead: %+v", task)
		}
	}
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("nothing should pend: %+v", got)
	}
}

func TestSpawnRequestForOrchestratorRoleRefused(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Role: contracts.RoleOrchestrator, Name: "usurper", Task: "orchestrate everything",
	})

	if tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil); len(tasks) != 0 {
		t.Fatalf("tasks = %+v", tasks)
	}
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("an orchestrator request must not pend: %+v", got)
	}
	if notes := r.notebook.Get("notes"); len(notes) != 1 || !strings.Contains(notes[0].Value, "Refused") {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestSpawnRequestForUnknownRoleRefused(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Role: "wizard", Name: "gandalf", Task: "cast spells",
	})

	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("an unknown role must not pend: %+v", got)
	}
	if notes := r.notebook.Get("notes"); len(notes) != 1 || !strings.Contains(notes[0].Value, "Refused") {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestLeadApproveConvertsRequestToTaskWithRequesterParent(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Role: contracts.RoleTester, Task: "test the feature",
	})
	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)

	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindApprove, AgentID: "agent-orchestrator", Name: "inner"})
	tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	got := tasks[0]
	if got.ParentAgentID != "agent-outer-1" {
		t.Fatalf("approved task should parent on the requester: %+v", got)
	}
	if got.Name != "inner" || got.Role != contracts.RoleTester || got.Task != "test the feature" {
		t.Fatalf("request details not preserved: %+v", got)
	}
	if got.AgentID != "agent-inner-1" {
		t.Fatalf("agent id = %q", got.AgentID)
	}
	if remaining := r.spawns.list("t1"); len(remaining) != 0 {
		t.Fatalf("request should be consumed: %+v", remaining)
	}
}

func TestLeadApproveUnknownNameLeavesNote(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindApprove, AgentID: "agent-orchestrator", Name: "ghost"})

	if tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil); len(tasks) != 0 {
		t.Fatalf("tasks = %+v", tasks)
	}
	notes := r.notebook.Get("notes")
	if len(notes) != 1 || !strings.Contains(notes[0].Value, "ghost") {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestNonLeadDecisionIgnored(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Task: "x",
	})
	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)

	var mu sync.Mutex
	var audit []string
	r.audit = func(kind string, _ map[string]any) {
		mu.Lock()
		audit = append(audit, kind)
		mu.Unlock()
	}
	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindApprove, AgentID: "agent-outer-2", Name: "inner"})
	if tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil); len(tasks) != 0 {
		t.Fatalf("a non-lead approve must not run anything: %+v", tasks)
	}
	if got := r.spawns.list("t1"); len(got) != 1 {
		t.Fatalf("the request should survive a non-lead decision: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, kind := range audit {
		if kind == "spawn.decision-ignored" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit = %v", audit)
	}
}

func TestLeadRejectRemovesRequestAndNotes(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Task: "x",
	})
	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)

	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindReject, AgentID: "agent-orchestrator", Name: "inner"})
	if tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil); len(tasks) != 0 {
		t.Fatalf("tasks = %+v", tasks)
	}
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("rejected request should be gone: %+v", got)
	}
	notes := r.notebook.Get("notes")
	if len(notes) != 1 || !strings.Contains(notes[0].Value, "inner") || !strings.Contains(notes[0].Value, "agent-outer-1") {
		t.Fatalf("rejection note should name request and requester: %+v", notes)
	}
}

// A pending request in one thread must never surface in another thread's
// prompt: the runner is daemon-global and the lead's agent id is shared.
func TestPendingRequestsNeverLeakAcrossThreads(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Task: "t1 only",
	})
	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)

	t1State, _ := eng.State(ctx, "t1")
	prompt := renderPendingSpawns(t1State, r.spawns.list("t1"))
	if !strings.Contains(prompt, "inner") || !strings.Contains(prompt, "## Pending spawn requests") {
		t.Fatalf("t1 prompt section = %q", prompt)
	}
	t2State, _ := eng.State(ctx, "t2")
	if leaked := renderPendingSpawns(t2State, r.spawns.list("t2")); leaked != "" {
		t.Fatalf("t2 should have no pending section: %q", leaked)
	}
}

func TestPendingRequestsClearedWhenTurnEnds(t *testing.T) {
	// The lead never decides, so the undecided request must be dropped when
	// the turn ends — and another thread's requests must survive that.
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.cfg.MaxPasses = 2
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Task: "undecided",
	})
	r.tools.Post("t2", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-other-1",
		Name: "other", Task: "other thread",
	})
	New(r).toolTasks(context.Background(), "t2", "agent-orchestrator", nil, nil)

	if err := New(r).RunTurn(ctx, "t1", "do the thing"); err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if got := r.spawns.list("t1"); len(got) != 0 {
		t.Fatalf("t1 request outlived its turn: %+v", got)
	}
	if got := r.spawns.list("t2"); len(got) != 1 {
		t.Fatalf("clearing t1 must not touch t2: %+v", got)
	}

	// The undecided proposal never ran.
	state, _ := eng.State(ctx, "t1")
	for id := range state.Agents {
		if strings.HasPrefix(string(id), "agent-inner") {
			t.Fatalf("undecided proposal ran: %+v", state.Agents[id])
		}
	}
}

// renderPendingSpawns names the requester from the roster when it can.
func TestRenderPendingSpawnsUsesRequesterName(t *testing.T) {
	r, eng, ctx := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	if err := r.Dispatch(ctx, "t1", []contracts.DispatchTask{{
		AgentID: "agent-outer-1", Name: "outer", Role: contracts.RoleExecutor, Task: "outer work",
	}}); err != nil {
		t.Fatal(err)
	}
	state, _ := eng.State(ctx, "t1")

	section := renderPendingSpawns(state, []spawnRequest{{
		RequesterID: "agent-outer-1", Name: "inner",
		Role: contracts.RoleExecutor, Task: "inner work",
	}})
	for _, want := range []string{"## Pending spawn requests", "outer (agent-outer-1)", "inner work", "tool approve --name"} {
		if !strings.Contains(section, want) {
			t.Fatalf("section missing %q:\n%s", want, section)
		}
	}
	if renderPendingSpawns(&domain.ThreadState{}, nil) != "" {
		t.Fatal("no requests should render no section")
	}
}

// An approve must not silently collide with names the pass already used: wave
// bookkeeping (status, results, dependency resolution) is name-keyed, so an
// approved request is renamed beside the minted protocol dispatches and the
// carried tasks that already claimed its name.
func TestApprovedRequestCannotCollideWithMintedOrCarriedNames(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Task: "test the feature",
	})
	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)

	r.tools.Post("t1", toolbox.Call{Kind: toolbox.KindApprove, AgentID: "agent-orchestrator", Name: "inner"})
	minted := []contracts.DispatchTask{{AgentID: "agent-inner-9", Name: "inner", Role: contracts.RoleExecutor, Task: "protocol-dispatched"}}
	claimed := map[string]bool{"carried": true}
	tasks := New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", minted, claimed)
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v", tasks)
	}
	names := map[string]bool{}
	for _, task := range tasks {
		if names[task.Name] {
			t.Fatalf("task name %q appears twice: %+v", task.Name, tasks)
		}
		names[task.Name] = true
	}
	// The approved request is the newcomer: it takes the suffixed name.
	approved := tasks[1]
	if approved.Name != "inner-2" || approved.ParentAgentID != "agent-outer-1" || approved.Task != "test the feature" {
		t.Fatalf("approved task = %+v", approved)
	}
}

// A re-proposal identical to a pending request is dropped — an echoed command,
// not new work — but leaves the same audit trail the other swallow paths do.
func TestDroppedDuplicateRequestLeavesAuditTrail(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Task: "write the inner tests",
	})
	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)

	var mu sync.Mutex
	var audit []string
	r.audit = func(kind string, _ map[string]any) {
		mu.Lock()
		audit = append(audit, kind)
		mu.Unlock()
	}
	// The echo: same requester, name, role, and task.
	r.tools.Post("t1", toolbox.Call{
		Kind: toolbox.KindSpawn, AgentID: "agent-outer-1",
		Name: "inner", Task: "write the inner tests",
	})
	New(r).toolTasks(context.Background(), "t1", "agent-orchestrator", nil, nil)

	if got := r.spawns.list("t1"); len(got) != 1 {
		t.Fatalf("echoed request recorded twice: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, kind := range audit {
		if kind == "spawn.duplicate" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dropped duplicate left no audit trail: %v", audit)
	}
}
