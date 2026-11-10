package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// postReviewCall sends one /tool/call body and returns the status and raw reply.
func postReviewCall(t *testing.T, url string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url+"/tool/call", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func TestReviewToolCallsApplyByRole(t *testing.T) {
	eng, disp := newCommandFixture(t)
	srv := httptest.NewServer(NewServer(disp, eng, disp.runner, "", toolbox.New()).Handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	if err := disp.runner.Dispatch(ctx, "t1", []contracts.DispatchTask{
		{AgentID: "agent-rev", Name: "rev", Role: contracts.RoleReviewer, Task: "review"},
		{AgentID: "agent-exec", Name: "exec", Role: contracts.RoleExecutor, Task: "fix"},
	}); err != nil {
		t.Fatal(err)
	}

	status, reply := postReviewCall(t, srv.URL, map[string]any{
		"threadId": "t1", "agentId": "agent-rev", "kind": "review-comment",
		"file": "a.go", "line": 3, "side": "new", "text": "handle the nil case",
	})
	if status != http.StatusOK {
		t.Fatalf("reviewer comment status = %d", status)
	}
	id, _ := reply["id"].(string)
	if id == "" || reply["ok"] != true {
		t.Fatalf("reply = %+v, want ok with a thread id", reply)
	}

	state, _ := eng.State(ctx, "t1")
	if len(state.ReviewThreads) != 1 || state.ReviewThreads[0].ID != id || state.ReviewThreads[0].File != "a.go" {
		t.Fatalf("review threads = %+v", state.ReviewThreads)
	}

	// Any agent may answer on a thread.
	if status, _ := postReviewCall(t, srv.URL, map[string]any{
		"threadId": "t1", "agentId": "agent-exec", "kind": "review-reply", "thread": id, "text": "added the guard",
	}); status != http.StatusOK {
		t.Fatalf("executor reply status = %d, want 200", status)
	}

	// An executor may not close a thread it did not open.
	if status, _ := postReviewCall(t, srv.URL, map[string]any{
		"threadId": "t1", "agentId": "agent-exec", "kind": "review-resolve", "thread": id,
	}); status != http.StatusBadRequest {
		t.Fatalf("executor resolve status = %d, want 400", status)
	}

	// The reviewer that opened the thread closes it.
	if status, _ := postReviewCall(t, srv.URL, map[string]any{
		"threadId": "t1", "agentId": "agent-rev", "kind": "review-resolve", "thread": id,
	}); status != http.StatusOK {
		t.Fatalf("reviewer resolve status = %d, want 200", status)
	}
	state, _ = eng.State(ctx, "t1")
	if got := state.ReviewThreads[0].Status; got != contracts.ReviewStatusResolved {
		t.Fatalf("status = %q, want resolved", got)
	}
	if n := len(state.ReviewThreads[0].Replies); n != 1 {
		t.Fatalf("replies = %d, want the executor's one", n)
	}

	// A reply to a thread that does not exist is refused, not dropped.
	if status, _ := postReviewCall(t, srv.URL, map[string]any{
		"threadId": "t1", "agentId": "agent-exec", "kind": "review-reply", "thread": "review-missing", "text": "hi",
	}); status != http.StatusBadRequest {
		t.Fatalf("unknown thread status = %d, want 400", status)
	}
}

func TestReviewCommentFromExecutorIsRefused(t *testing.T) {
	eng, disp := newCommandFixture(t)
	srv := httptest.NewServer(NewServer(disp, eng, disp.runner, "", toolbox.New()).Handler())
	t.Cleanup(srv.Close)
	if err := disp.runner.Dispatch(context.Background(), "t1", []contracts.DispatchTask{
		{AgentID: "agent-exec", Name: "exec", Role: contracts.RoleExecutor, Task: "fix"},
	}); err != nil {
		t.Fatal(err)
	}
	if status, _ := postReviewCall(t, srv.URL, map[string]any{
		"threadId": "t1", "agentId": "agent-exec", "kind": "review-comment",
		"file": "a.go", "line": 1, "text": "not mine to open",
	}); status != http.StatusBadRequest {
		t.Fatalf("executor comment status = %d, want 400", status)
	}
}
