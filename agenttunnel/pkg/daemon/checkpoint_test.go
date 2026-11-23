package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// ckScript is a harness that adds one file to its worktree per run, numbered by
// a counter kept in $CK_DIR, outside the repository. Each turn therefore leaves
// exactly one new file behind, which the checkpoints must record.
const ckScript = `n=$(cat "$CK_DIR/n" 2>/dev/null || echo 0)
n=$((n+1))
echo $n > "$CK_DIR/n"
echo "run $n" > "agent-$n.txt"
printf '{"type":"text","text":"done"}\n'`

// newCheckpointDaemon starts a daemon over stateDir whose agents write one file
// each. One pass per turn keeps a turn to one harness run.
func newCheckpointDaemon(t *testing.T, stateDir string) *Daemon {
	t.Helper()
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", ckScript}, PromptVia: "stdin"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: stateDir,
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		Config:   orchestrator.Config{MaxPasses: 1, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	return d
}

// newCheckpointEnv starts a checkpoint daemon with its own counter directory.
// The counter directory is set before the daemon so harness children inherit it.
func newCheckpointEnv(t *testing.T) (*Daemon, *websocket.Conn, string) {
	t.Helper()
	ckDir := t.TempDir()
	t.Setenv("CK_DIR", ckDir)
	stateDir := t.TempDir()
	d := newCheckpointDaemon(t, stateDir)
	t.Cleanup(d.Close)
	c, _ := dialWS(t, d)
	return d, c, stateDir
}

// userTurns returns the turn-opening user messages of a thread, oldest first.
func userTurns(t *testing.T, d *Daemon, id contracts.ThreadID) []struct {
	ID   string
	Text string
	Turn int
} {
	t.Helper()
	st, err := d.Engine.State(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []struct {
		ID   string
		Text string
		Turn int
	}
	for _, m := range st.Messages {
		if m.Author == "user" {
			out = append(out, struct {
				ID   string
				Text string
				Turn int
			}{m.ID, m.Text, m.Turn})
		}
	}
	return out
}

// callErr sends one request and returns the RPC error, if any. It is call for
// requests that are expected to be refused.
func callErr(t *testing.T, c *websocket.Conn, id, method string, params any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, _ := json.Marshal(transport.Request{ID: id, Method: method, Params: mustJSON(t, params)})
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read %s: %v", method, err)
		}
		var resp transport.Response
		if err := json.Unmarshal(msg, &resp); err != nil || resp.Method == "thread.event" || resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return errors.New(resp.Error.Message)
		}
		return nil
	}
}

type ckRow struct {
	ID              string `json:"id"`
	Turn            int    `json:"turn"`
	AnchorMessageID string `json:"anchorMessageId"`
	Files           int    `json:"files"`
	Additions       int    `json:"additions"`
	Deletions       int    `json:"deletions"`
	Ready           bool   `json:"ready"`
	Tree            string `json:"tree"`
}

func checkpointRows(t *testing.T, c *websocket.Conn, id contracts.ThreadID) []ckRow {
	t.Helper()
	var rows []ckRow
	if err := json.Unmarshal(call(t, c, "ck", "thread.checkpoints", map[string]any{"threadId": id}), &rows); err != nil {
		t.Fatalf("thread.checkpoints: %v", err)
	}
	return rows
}

// Every turn in a worktree leaves a checkpoint with its own diff, and the
// checkpoints are the same after a daemon restart.
func TestTurnsRecordCheckpointsAndSurviveRestart(t *testing.T) {
	d, c, stateDir := newCheckpointEnv(t)
	repo := newWorktreeRepo(t)
	_, id := setupGitThread(t, c, repo)

	call(t, c, "t1", "thread.turn.start", map[string]any{"threadId": id, "text": "first"})
	call(t, c, "t2", "thread.turn.start", map[string]any{"threadId": id, "text": "second"})

	rows := checkpointRows(t, c, id)
	if len(rows) != 2 {
		t.Fatalf("checkpoints = %+v, want one per turn", rows)
	}
	for i, r := range rows {
		if r.Turn != i+1 || !r.Ready || r.Files != 1 || r.Additions != 1 || r.Deletions != 0 {
			t.Fatalf("checkpoint %d = %+v", i, r)
		}
	}

	var diff struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	json.Unmarshal(call(t, c, "d", "thread.checkpoint.diff", map[string]any{"threadId": id, "checkpointId": rows[1].ID}), &diff)
	if len(diff.Files) != 1 || diff.Files[0].Path != "agent-2.txt" {
		t.Fatalf("turn 2 diff = %+v, want only agent-2.txt", diff.Files)
	}

	// The user's checkout keeps no trace of the snapshots.
	assertProjectUntouched(t, repo)

	before, _ := json.Marshal(rows)
	d.Close()
	d2 := newCheckpointDaemon(t, stateDir)
	defer d2.Close()
	c2, _ := dialWS(t, d2)
	after, _ := json.Marshal(checkpointRows(t, c2, id))
	if string(before) != string(after) {
		t.Fatalf("checkpoints changed across restart:\n before %s\n after  %s", before, after)
	}
}

// Rewinding files restores the turn's baseline, removes what later turns added,
// and keeps a safety snapshot the restore can be undone from.
func TestRewindRestoresFilesAndKeepsSafetySnapshot(t *testing.T) {
	d, c, _ := newCheckpointEnv(t)
	repo := newWorktreeRepo(t)
	_, id := setupGitThread(t, c, repo)
	call(t, c, "t1", "thread.turn.start", map[string]any{"threadId": id, "text": "first"})
	call(t, c, "t2", "thread.turn.start", map[string]any{"threadId": id, "text": "second"})
	turns := userTurns(t, d, id)
	ref := threadRef(t, d, id)

	var res struct {
		OK        bool   `json:"ok"`
		SafetyRef string `json:"safetyRef"`
		Prompt    struct {
			Text string `json:"text"`
		} `json:"prompt"`
	}
	json.Unmarshal(call(t, c, "rw", "thread.rewind", map[string]any{
		"threadId": id, "toMessageId": turns[1].ID, "restoreFiles": true,
	}), &res)
	if !res.OK || res.Prompt.Text != "second" {
		t.Fatalf("rewind result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(ref.Path, "agent-2.txt")); !os.IsNotExist(err) {
		t.Fatalf("agent-2.txt survived the file restore (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(ref.Path, "agent-1.txt")); err != nil {
		t.Fatalf("agent-1.txt from the earlier turn is gone: %v", err)
	}
	if res.SafetyRef == "" {
		t.Fatal("no safety ref returned")
	}
	out, err := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname)", "refs/agenttunnel/rewind-safety/").CombinedOutput()
	if err != nil || !strings.Contains(string(out), res.SafetyRef) {
		t.Fatalf("safety ref %s missing: %v %s", res.SafetyRef, err, out)
	}

	// The rewound thread keeps only turn 1, and its prompt is the removed one.
	if rows := checkpointRows(t, c, id); len(rows) != 1 || rows[0].Turn != 1 {
		t.Fatalf("checkpoints after rewind = %+v", rows)
	}
	if got := userTurns(t, d, id); len(got) != 1 || got[0].Text != "first" {
		t.Fatalf("user turns after rewind = %+v", got)
	}
}

// Conversation-only rewind works on a thread with no worktree, and file restore
// is refused there with the reason.
func TestNonGitThreadRewindIsConversationOnly(t *testing.T) {
	d, c, _ := newCheckpointEnv(t)
	plain := t.TempDir()
	_, id := setupGitThread(t, c, plain)
	call(t, c, "t1", "thread.turn.start", map[string]any{"threadId": id, "text": "first"})
	call(t, c, "t2", "thread.turn.start", map[string]any{"threadId": id, "text": "second"})
	turns := userTurns(t, d, id)

	if rows := checkpointRows(t, c, id); len(rows) != 0 {
		t.Fatalf("non-git thread has checkpoints: %+v", rows)
	}
	err := callErr(t, c, "rw", "thread.rewind", map[string]any{"threadId": id, "toMessageId": turns[1].ID, "restoreFiles": true})
	if err == nil || !strings.Contains(err.Error(), "rewind the conversation only") {
		t.Fatalf("file restore on a non-git thread: err = %v", err)
	}
	call(t, c, "rw2", "thread.rewind", map[string]any{"threadId": id, "toMessageId": turns[1].ID})
	if got := userTurns(t, d, id); len(got) != 1 {
		t.Fatalf("conversation rewind left %d user turns, want 1", len(got))
	}
}

// A worktree another thread also uses cannot be restored under it. The
// conversation-only rewind stays available, and the refusal changes nothing.
func TestRewindRefusesFileRestoreOnSharedWorktree(t *testing.T) {
	d, c, _ := newCheckpointEnv(t)
	repo := newWorktreeRepo(t)
	projectID, id := setupGitThread(t, c, repo)
	call(t, c, "t1", "thread.turn.start", map[string]any{"threadId": id, "text": "first"})
	call(t, c, "t2", "thread.turn.start", map[string]any{"threadId": id, "text": "second"})
	turns := userTurns(t, d, id)
	shared := threadRef(t, d, id)

	other := contracts.ThreadID("th_sharedother")
	if err := d.Engine.Dispatch(context.Background(), contracts.Command{
		ID: "mk-other", Type: contracts.CmdThreadCreate, ThreadID: other,
		Title: "Other", ProjectID: projectID, Worktree: &shared,
	}); err != nil {
		t.Fatalf("second thread on the worktree: %v", err)
	}

	err := callErr(t, c, "rw", "thread.rewind", map[string]any{"threadId": id, "toMessageId": turns[1].ID, "restoreFiles": true})
	if err == nil || !strings.Contains(err.Error(), "shared") {
		t.Fatalf("shared worktree: err = %v, want a shared-worktree refusal", err)
	}
	if got := userTurns(t, d, id); len(got) != 2 {
		t.Fatalf("refused rewind changed the transcript: %d user turns", len(got))
	}
	if _, err := os.Stat(filepath.Join(shared.Path, "agent-2.txt")); err != nil {
		t.Fatalf("refused rewind touched files: %v", err)
	}
	call(t, c, "rw2", "thread.rewind", map[string]any{"threadId": id, "toMessageId": turns[1].ID})
	if got := userTurns(t, d, id); len(got) != 1 {
		t.Fatalf("conversation-only rewind left %d user turns, want 1", len(got))
	}
}

// Removing a thread's worktree also removes its checkpoint refs; the refs live in
// the repository, so they would otherwise outlive the thread.
func TestDeleteWithWorktreeDropsCheckpointRefs(t *testing.T) {
	d, c, _ := newCheckpointEnv(t)
	repo := newWorktreeRepo(t)
	_, id := setupGitThread(t, c, repo)
	call(t, c, "t1", "thread.turn.start", map[string]any{"threadId": id, "text": "first"})
	if len(checkpointRows(t, c, id)) != 1 {
		t.Fatal("turn wrote no checkpoint")
	}
	// Commit the agent's file so the worktree is clean enough to remove.
	ref := threadRef(t, d, id)
	wtGit(t, ref.Path, "add", "-A")
	wtGit(t, ref.Path, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "work")

	call(t, c, "del", "thread.delete", map[string]any{"threadId": id, "removeWorktree": true})

	out, err := exec.Command("git", "-C", repo, "for-each-ref", "refs/agenttunnel/").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), string(id)) {
		t.Fatalf("checkpoint refs survived the thread delete:\n%s", out)
	}
}
