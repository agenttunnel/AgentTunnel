package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/worktree"
)

// A subthread shares its parent's worktree. When the parent's directory has
// vanished, the spawn re-cuts it first, so the subthread's agents run in a live
// checkout rather than a dead path.
func TestSubthreadSpawnRecutsVanishedParentWorktree(t *testing.T) {
	d, c, logPath := newWorktreeDaemon(t)
	repo := newWorktreeRepo(t)
	_, parent := setupGitThread(t, c, repo)

	call(t, c, "turn", "thread.turn.start", map[string]any{"threadId": parent, "text": "first"})
	old := threadRef(t, d, parent)
	if err := os.RemoveAll(old.Path); err != nil {
		t.Fatal(err)
	}

	events, unsub := d.Engine.Subscribe(parent)
	defer unsub()
	_, base := dialWS(t, d)
	status, body := postToolKind(t, base, map[string]any{
		"threadId": parent, "agentId": "agent-orchestrator", "kind": "subthread", "task": "side task",
	})
	if status != http.StatusOK {
		t.Fatalf("subthread call: status %d: %s", status, body)
	}
	var started struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(body, &started)
	child := started.ThreadID

	waitForReport(t, events, "subthread")

	fresh := threadRef(t, d, parent)
	if fresh.Path == old.Path {
		t.Fatalf("parent kept the vanished worktree %q", old.Path)
	}
	if fi, err := os.Stat(fresh.Path); err != nil || !fi.IsDir() {
		t.Fatalf("re-cut worktree %q is not a live directory: %v", fresh.Path, err)
	}
	st, err := d.Engine.State(context.Background(), child)
	if err != nil {
		t.Fatal(err)
	}
	if st.Worktree == nil || !st.Worktree.Inherited || st.Worktree.Path != fresh.Path {
		t.Fatalf("subthread ref = %+v, want inherited %q", st.Worktree, fresh.Path)
	}
	cwds := recordedCwds(t, logPath)
	if last := cwds[len(cwds)-1]; !worktree.SamePath(last, fresh.Path) {
		t.Fatalf("subthread ran in %q, want live parent worktree %q", last, fresh.Path)
	}
	assertProjectUntouched(t, repo)
}
