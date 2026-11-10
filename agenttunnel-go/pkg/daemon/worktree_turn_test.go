package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/worktree"
)

// wtScript is a harness that appends the directory it runs in to $CWD_LOG, then
// reports once. Its recorded cwd is the proof of where runTurn put the agents.
const wtScript = `pwd >> "$CWD_LOG"
printf '{"type":"text","text":"done"}\n'`

// newWorktreeDaemon starts a daemon whose agents record their cwd. It returns the
// daemon, a connection to it, and the cwd log path.
func newWorktreeDaemon(t *testing.T) (*Daemon, *websocket.Conn, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "cwd.log")
	t.Setenv("CWD_LOG", logPath) // set before any turn: harness children inherit it
	d := newWtDaemon(t, t.TempDir())
	t.Cleanup(d.Close)
	c, _ := dialWS(t, d)
	return d, c, logPath
}

func newWtDaemon(t *testing.T, stateDir string) *Daemon {
	t.Helper()
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", wtScript}, PromptVia: "stdin"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: stateDir,
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
		Config:   orchestrator.Config{MaxPasses: 2, MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	return d
}

// newWorktreeRepo makes a git repository with a committed README on main.
func newWorktreeRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	wtGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, repo, "add", "README.md")
	wtGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "init")
	return repo
}

func wtGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// recordedCwds returns the directories agents ran in, in order.
func recordedCwds(t *testing.T, logPath string) []string {
	t.Helper()
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("no agent ran: %v", err)
	}
	defer f.Close()
	var dirs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			dirs = append(dirs, line)
		}
	}
	return dirs
}

// setupGitThread registers repo as a project and creates one thread in it. No
// worktree exists until the thread's first turn.
func setupGitThread(t *testing.T, c *websocket.Conn, repo string) (string, contracts.ThreadID) {
	t.Helper()
	var proj struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	json.Unmarshal(call(t, c, "p", "project.create", map[string]any{"name": "Proj", "path": repo}), &proj)
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "t", "thread.create", map[string]any{
		"title": "Feature", "projectId": proj.Project.ID,
	}), &created)
	return proj.Project.ID, created.ThreadID
}

func threadRef(t *testing.T, d *Daemon, id contracts.ThreadID) contracts.WorktreeRef {
	t.Helper()
	st, err := d.Engine.State(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Worktree == nil {
		t.Fatal("thread has no worktree")
	}
	return *st.Worktree
}

// assertProjectUntouched checks the user's checkout holds no agent work and no
// uncommitted changes.
func assertProjectUntouched(t *testing.T, repo string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repo, "agent.txt")); err == nil {
		t.Fatal("agent work landed in the project checkout")
	}
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("project checkout is dirty: %v %q", err, out)
	}
}

// The first turn of a thread in a git project cuts its worktree, and the agents
// run inside it. This exercises the turn-start gate through runTurn.
func TestTurnCutsWorktreeAndAgentsRunInIt(t *testing.T) {
	d, c, logPath := newWorktreeDaemon(t)
	repo := newWorktreeRepo(t)
	_, id := setupGitThread(t, c, repo)

	if st, _ := d.Engine.State(context.Background(), id); st.Worktree != nil {
		t.Fatal("worktree cut before the first turn")
	}
	call(t, c, "turn", "thread.turn.start", map[string]any{"threadId": id, "text": "build the feature"})

	ref := threadRef(t, d, id)
	if ref.Inherited || !strings.HasPrefix(ref.Branch, "agenttunnel/feature-") {
		t.Fatalf("ref = %+v", ref)
	}
	if !strings.HasPrefix(ref.Path, worktree.Dir(repo)+string(filepath.Separator)) {
		t.Fatalf("worktree %q is not beside the project", ref.Path)
	}
	cwds := recordedCwds(t, logPath)
	if len(cwds) == 0 {
		t.Fatal("no agent recorded a cwd")
	}
	for _, dir := range cwds {
		if !worktree.SamePath(dir, ref.Path) {
			t.Fatalf("agent ran in %q, want worktree %q", dir, ref.Path)
		}
	}
	assertProjectUntouched(t, repo)
}

// A worktree removed through worktree.remove leaves the thread with no ref. Its
// next turn cuts a new worktree and the agents run there.
func TestTurnAfterWorktreeRemoveRunsInFreshWorktree(t *testing.T) {
	d, c, logPath := newWorktreeDaemon(t)
	repo := newWorktreeRepo(t)
	projectID, id := setupGitThread(t, c, repo)

	call(t, c, "turn", "thread.turn.start", map[string]any{"threadId": id, "text": "first"})
	old := threadRef(t, d, id)
	// The agent's commit keeps the branch, so the next cut gets a new name.
	if err := os.WriteFile(filepath.Join(old.Path, "agent.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, old.Path, "add", "agent.txt")
	wtGit(t, old.Path, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "work")

	call(t, c, "rm", "worktree.remove", map[string]any{"projectId": projectID, "path": old.Path})
	call(t, c, "turn2", "thread.turn.start", map[string]any{"threadId": id, "text": "second"})

	fresh := threadRef(t, d, id)
	if fresh.Path == old.Path || fresh.Branch == old.Branch {
		t.Fatalf("ref after remove = %+v, want a new worktree (old %+v)", fresh, old)
	}
	cwds := recordedCwds(t, logPath)
	if last := cwds[len(cwds)-1]; !worktree.SamePath(last, fresh.Path) {
		t.Fatalf("second turn ran in %q, want fresh worktree %q", last, fresh.Path)
	}
	assertProjectUntouched(t, repo)
}

// A worktree whose directory was deleted by hand is re-cut by the next turn.
func TestTurnAfterHandDeletedWorktreeRecutsIt(t *testing.T) {
	d, c, logPath := newWorktreeDaemon(t)
	repo := newWorktreeRepo(t)
	_, id := setupGitThread(t, c, repo)

	call(t, c, "turn", "thread.turn.start", map[string]any{"threadId": id, "text": "first"})
	lost := threadRef(t, d, id)
	if err := os.RemoveAll(lost.Path); err != nil {
		t.Fatal(err)
	}
	call(t, c, "turn2", "thread.turn.start", map[string]any{"threadId": id, "text": "second"})

	fresh := threadRef(t, d, id)
	if fresh.Path == lost.Path {
		t.Fatalf("ref unchanged after hand delete: %+v", fresh)
	}
	cwds := recordedCwds(t, logPath)
	if last := cwds[len(cwds)-1]; !worktree.SamePath(last, fresh.Path) {
		t.Fatalf("second turn ran in %q, want re-cut worktree %q", last, fresh.Path)
	}
	assertProjectUntouched(t, repo)
}

// A project that cannot have a worktree runs on its path, and the fallback is
// written to the action log once across turns, not once per turn.
func TestNonGitProjectAuditsFallbackOnce(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CWD_LOG", filepath.Join(t.TempDir(), "cwd.log"))
	d := newWtDaemon(t, stateDir)
	c, _ := dialWS(t, d)
	_, id := setupGitThread(t, c, t.TempDir())

	call(t, c, "turn", "thread.turn.start", map[string]any{"threadId": id, "text": "one"})
	call(t, c, "turn2", "thread.turn.start", map[string]any{"threadId": id, "text": "two"})
	if st, _ := d.Engine.State(context.Background(), id); st.Worktree != nil {
		t.Fatalf("non-git project got a worktree %+v", st.Worktree)
	}
	d.Close()

	data, err := os.ReadFile(filepath.Join(stateDir, "logs", "actions.jsonl"))
	if err != nil {
		t.Fatalf("read action log: %v", err)
	}
	if n := strings.Count(string(data), `"kind":"worktree.fallback"`); n != 1 {
		t.Fatalf("worktree.fallback logged %d times across two turns, want 1", n)
	}
}
