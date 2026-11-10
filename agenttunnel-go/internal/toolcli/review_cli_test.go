package toolcli

import (
	"context"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// The review flags reach the daemon: a reviewer's comment lands with its anchor,
// and an executor's resolve exits non-zero so the agent sees the refusal.
func TestReviewToolCLI(t *testing.T) {
	eng, runner := newCLIServer(t)
	ctx := context.Background()
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Dispatch(ctx, "t1", []contracts.DispatchTask{
		{AgentID: "agent-hub", Name: "hub", Role: contracts.RoleReviewer, Task: "review"},
		{AgentID: "agent-exec", Name: "exec", Role: contracts.RoleExecutor, Task: "fix"},
	}); err != nil {
		t.Fatal(err)
	}

	if code := Run([]string{"review-comment", "--file", "a.go", "--line", "4", "--end-line", "6", "--side", "old", "--text", "dead code"}); code != 0 {
		t.Fatalf("review-comment exit = %d, want 0", code)
	}
	state, _ := eng.State(ctx, "t1")
	if len(state.ReviewThreads) != 1 {
		t.Fatalf("threads = %+v", state.ReviewThreads)
	}
	th := state.ReviewThreads[0]
	if th.File != "a.go" || th.Line != 4 || th.EndLine != 6 || th.Side != "old" || th.Body != "dead code" || th.AuthorID != "agent-hub" {
		t.Fatalf("thread = %+v", th)
	}

	t.Setenv("AT_AGENT_ID", "agent-exec")
	if code := Run([]string{"review-resolve", "--thread", th.ID}); code != 1 {
		t.Fatalf("executor resolve exit = %d, want 1", code)
	}
	if code := Run([]string{"review-reply", "--thread", th.ID, "--text", "removed it"}); code != 0 {
		t.Fatalf("executor reply exit = %d, want 0", code)
	}
	state, _ = eng.State(ctx, "t1")
	if got := state.ReviewThreads[0]; got.Status != contracts.ReviewStatusOpen || len(got.Replies) != 1 {
		t.Fatalf("after reply thread = %+v", got)
	}
}
