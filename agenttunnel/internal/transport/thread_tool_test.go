package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// createThreadForTool makes a thread through the engine, so the tool handlers
// see it as a real durable thread.
func createThreadForTool(t *testing.T, eng *domain.Engine, id contracts.ThreadID, wt *contracts.WorktreeRef) {
	t.Helper()
	if err := eng.Dispatch(context.Background(), contracts.Command{
		ID: "create-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id, Worktree: wt,
	}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	// A rename is the first durable command, so the thread is materialized in
	// the log; deleting a thread that was never written just forgets it.
	if err := eng.Dispatch(context.Background(), contracts.Command{
		ID: "title-" + string(id), Type: contracts.CmdThreadRename, ThreadID: id, Title: string(id),
	}); err != nil {
		t.Fatalf("title %s: %v", id, err)
	}
}

// leaveAgentRunning dispatches an agent that has not finished, which is what
// makes a thread count as running.
func leaveAgentRunning(t *testing.T, eng *domain.Engine, id contracts.ThreadID) {
	t.Helper()
	if err := eng.Dispatch(context.Background(), contracts.Command{
		ID: "dispatch-" + string(id), Type: contracts.CmdTaskDispatch, ThreadID: id,
		Dispatch: &contracts.DispatchTask{
			AgentID: "agent-hub", Name: "hub", Role: contracts.RoleExecutor, Task: "hub work", Harness: "claude",
		},
	}); err != nil {
		t.Fatalf("dispatch on %s: %v", id, err)
	}
}

func TestThreadToolCallsAreRefusedForSubagents(t *testing.T) {
	ctx := context.Background()
	eng, _, srv := newMessageFixture(t)
	createThreadForTool(t, eng, "t1", nil)
	createThreadForTool(t, eng, "t2", nil)

	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-hub","kind":"thread-list"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("subagent thread-list = %d, want 403", rec.Code)
	}
	rec = postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-hub","kind":"thread-delete","target":"t2"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("subagent thread-delete = %d, want 403", rec.Code)
	}
	// A call with no agent id is not an agent session, so it is refused too.
	rec = postLocalToolCall(t, srv, `{"threadId":"t1","kind":"thread-delete","target":"t2"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous thread-delete = %d, want 403", rec.Code)
	}

	st, err := eng.State(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.Deleted {
		t.Fatal("a refused subagent call deleted the thread")
	}
}

func TestThreadToolListReportsStatusAndBranch(t *testing.T) {
	eng, _, srv := newMessageFixture(t)
	createThreadForTool(t, eng, "t1", nil)
	createThreadForTool(t, eng, "t2", &contracts.WorktreeRef{Path: "/work/t2", Branch: "agenttunnel/t2", Base: "main"})
	leaveAgentRunning(t, eng, "t2")

	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"thread-list"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("thread-list = %d: %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Threads []toolThread `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("thread-list body: %v\n%s", err, rec.Body.String())
	}
	byID := map[contracts.ThreadID]toolThread{}
	for _, th := range listed.Threads {
		byID[th.ID] = th
	}
	if len(byID) != 2 {
		t.Fatalf("listed %d threads, want 2: %+v", len(byID), listed.Threads)
	}
	if got := byID["t1"]; got.Status != "idle" || got.Branch != "" {
		t.Fatalf("t1 = %+v, want idle with no branch", got)
	}
	if got := byID["t2"]; got.Status != "running" || got.Branch != "agenttunnel/t2" || got.Worktree != "/work/t2" {
		t.Fatalf("t2 = %+v, want running on agenttunnel/t2 at /work/t2", got)
	}
}

func TestThreadToolDeleteRefusesSelfUnknownAndRunning(t *testing.T) {
	ctx := context.Background()
	eng, _, srv := newMessageFixture(t)
	createThreadForTool(t, eng, "t1", nil)
	createThreadForTool(t, eng, "t2", nil)
	leaveAgentRunning(t, eng, "t2")

	// A lead cannot delete the thread it is running in.
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"thread-delete","target":"t1"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("self delete = %d, want 400", rec.Code)
	}
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"thread-delete"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("delete with no target = %d, want 400", rec.Code)
	}
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"thread-delete","target":"ghost"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("delete unknown thread = %d, want 400", rec.Code)
	}

	// A thread with an agent still running is refused, and stays whole.
	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"thread-delete","target":"t2"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "running") {
		t.Fatalf("delete running thread = %d %q, want 400 naming running", rec.Code, rec.Body.String())
	}
	st, err := eng.State(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.Deleted {
		t.Fatal("refused delete tombstoned a running thread")
	}
}

func TestThreadToolDeleteRefusesAThreadWithATurnInFlight(t *testing.T) {
	ctx := context.Background()
	eng, _, srv := newMessageFixture(t)
	createThreadForTool(t, eng, "t1", nil)
	createThreadForTool(t, eng, "t3", nil)

	// A turn holds its slot before any agent is recorded; deleting then is
	// refused just as it is for a running agent.
	_, endTurn, err := srv.disp.beginTurn(ctx, "t3")
	if err != nil {
		t.Fatal(err)
	}
	if rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"thread-delete","target":"t3"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("delete during a turn = %d, want 400", rec.Code)
	}
	endTurn()

	rec := postLocalToolCall(t, srv, `{"threadId":"t1","agentId":"agent-orchestrator","kind":"thread-delete","target":"t3"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete after the turn ended = %d: %s", rec.Code, rec.Body.String())
	}
	st, err := eng.State(ctx, "t3")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Deleted {
		t.Fatal("delete after the turn ended did not tombstone the thread")
	}
}

// The thread.delete RPC shares the same refusal: a running thread is not
// tombstoned by a client that asks for it directly.
func TestThreadDeleteRPCRefusesRunningThread(t *testing.T) {
	ctx := context.Background()
	eng, _, srv := newMessageFixture(t)
	createThreadForTool(t, eng, "t2", nil)
	leaveAgentRunning(t, eng, "t2")

	if _, err := srv.disp.Handle(ctx, Request{Method: "thread.delete", Params: json.RawMessage(`{"threadId":"t2"}`)}); err == nil {
		t.Fatal("thread.delete on a running thread was not refused")
	}
	st, err := eng.State(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.Deleted {
		t.Fatal("refused thread.delete tombstoned the running thread")
	}
}

// A remote peer cannot list or delete threads, even when its body names the lead.
func TestThreadToolCallsAreRefusedFromARemotePeer(t *testing.T) {
	ctx := context.Background()
	eng, _, srv := newMessageFixture(t)
	createThreadForTool(t, eng, "t1", nil)
	createThreadForTool(t, eng, "t2", nil)
	for _, body := range []string{
		`{"threadId":"t1","agentId":"agent-orchestrator","kind":"thread-list"}`,
		`{"threadId":"t1","agentId":"agent-orchestrator","kind":"thread-delete","target":"t2"}`,
	} {
		if rec := postToolCall(t, srv, body); rec.Code != http.StatusForbidden {
			t.Fatalf("remote %s = %d, want 403", body, rec.Code)
		}
	}
	st, err := eng.State(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.Deleted {
		t.Fatal("a remote call deleted the thread")
	}
}
