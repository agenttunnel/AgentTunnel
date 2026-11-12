package toolcli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestToolThreadCLIListsAndDeletes(t *testing.T) {
	eng, _ := newCLIServer(t)
	ctx := context.Background()
	// The lead's own thread is t1 (AT_THREAD_ID); t2 is the one it may delete.
	t.Setenv("AT_AGENT_ID", "agent-orchestrator")
	for _, id := range []contracts.ThreadID{"t1", "t2"} {
		if err := eng.Dispatch(ctx, contracts.Command{ID: "c-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id}); err != nil {
			t.Fatal(err)
		}
		// A rename materializes the thread in the log, as a first real turn would.
		if err := eng.Dispatch(ctx, contracts.Command{ID: "r-" + string(id), Type: contracts.CmdThreadRename, ThreadID: id, Title: string(id)}); err != nil {
			t.Fatal(err)
		}
	}

	var listed struct {
		Threads []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"threads"`
	}
	var code int
	out := captureStdout(t, func() { code = Run([]string{"thread-list"}) })
	if code != 0 {
		t.Fatalf("thread-list exit = %d, want 0", code)
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("thread-list output is not JSON: %v\n%s", err, out)
	}
	if len(listed.Threads) != 2 {
		t.Fatalf("listed %+v, want both threads", listed.Threads)
	}

	// The lead cannot delete its own thread; the refusal exits non-zero.
	if code := Run([]string{"thread-delete", "--id", "t1"}); code != 1 {
		t.Fatalf("self delete exit = %d, want 1", code)
	}
	if code := Run([]string{"thread-delete"}); code != 1 {
		t.Fatalf("delete with no --id exit = %d, want 1", code)
	}
	if code := Run([]string{"thread-delete", "--id", "t2", "--remove-worktree"}); code != 0 {
		t.Fatalf("delete exit = %d, want 0", code)
	}
	st, err := eng.State(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Deleted {
		t.Fatal("thread-delete did not tombstone t2")
	}

	out = captureStdout(t, func() { code = Run([]string{"thread-list"}) })
	if code != 0 {
		t.Fatalf("second thread-list exit = %d, want 0", code)
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("second thread-list output is not JSON: %v\n%s", err, out)
	}
	if len(listed.Threads) != 1 || listed.Threads[0].ID != "t1" {
		t.Fatalf("after delete listed %+v, want only t1", listed.Threads)
	}
}
