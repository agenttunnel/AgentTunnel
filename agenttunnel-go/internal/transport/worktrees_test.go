package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/worktree"
)

type wtFixture struct {
	t         *testing.T
	ctx       context.Context
	eng       *domain.Engine
	store     *domain.MemoryStore
	disp      *Dispatcher
	projectID string
	repo      string
}

// newWorktreeFixture wires a dispatcher to a real git repository registered as a
// project, the way a user's checkout reaches the daemon.
func newWorktreeFixture(t *testing.T) *wtFixture {
	t.Helper()
	ctx := context.Background()
	repo := initTransportRepo(t)

	store := domain.NewMemoryStore()
	eng := domain.NewEngine(store, nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	st := settings.New(t.TempDir())
	st.AddProject(settings.Project{ID: "p1", Name: "proj", Path: repo})
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Logs: logstore.New(t.TempDir()), Settings: st})
	return &wtFixture{t: t, ctx: ctx, eng: eng, store: store, disp: disp, projectID: "p1", repo: repo}
}

// initTransportRepo makes a repository with a committed README on main.
func initTransportRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "init")
	return repo
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// call runs one RPC method and returns its result as generic JSON.
func (f *wtFixture) call(method string, params any) (any, error) {
	f.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.disp.Handle(f.ctx, Request{Method: method, Params: raw})
}

func (f *wtFixture) mustCall(method string, params any) any {
	f.t.Helper()
	out, err := f.call(method, params)
	if err != nil {
		f.t.Fatalf("%s: %v", method, err)
	}
	return out
}

// createThread makes a thread through thread.create and materializes it with a
// durable rename, so its events are on disk and visible to the store.
func (f *wtFixture) createThread(title string, worktree bool) contracts.ThreadID {
	f.t.Helper()
	out := f.mustCall("thread.create", map[string]any{"title": title, "projectId": f.projectID, "worktree": worktree})
	id := out.(map[string]any)["threadId"].(contracts.ThreadID)
	f.mustCall("thread.rename", map[string]any{"threadId": id, "title": title})
	return id
}

func (f *wtFixture) state(id contracts.ThreadID) *domain.ThreadState {
	f.t.Helper()
	st, err := f.eng.State(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *wtFixture) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestThreadOpenCreatesNoWorktreeUntilFirstTurn(t *testing.T) {
	f := newWorktreeFixture(t)
	out := f.mustCall("thread.open", map[string]any{"dir": f.repo})
	id := out.(map[string]any)["threadId"].(contracts.ThreadID)

	if st := f.state(id); st.Worktree != nil {
		t.Fatalf("thread.open attached worktree %+v", st.Worktree)
	}
	if f.exists(worktree.Dir(f.repo)) {
		t.Fatal("thread.open created a worktree directory")
	}

	// The first turn's hook cuts it; a second call does not cut another.
	f.disp.ensureWorktree(f.ctx, id)
	first := f.state(id).Worktree
	if first == nil || !f.exists(first.Path) {
		t.Fatalf("first turn did not cut a worktree: %+v", first)
	}
	f.disp.ensureWorktree(f.ctx, id)
	if again := f.state(id).Worktree; again == nil || again.Path != first.Path {
		t.Fatalf("second ensure changed the worktree: %+v", again)
	}
}

func TestThreadCreateWorktreeRidesCreationEvent(t *testing.T) {
	f := newWorktreeFixture(t)
	id := f.createThread("Fix login", true)

	st := f.state(id)
	if st.Worktree == nil || !f.exists(st.Worktree.Path) {
		t.Fatalf("worktree = %+v, want a cut directory", st.Worktree)
	}
	if !strings.HasPrefix(st.Worktree.Branch, "agenttunnel/fix-login-") {
		t.Fatalf("branch = %q", st.Worktree.Branch)
	}

	events, err := f.store.Load(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	created, ok := events[0].Payload.(contracts.ThreadCreated)
	if !ok || created.Worktree == nil || created.Worktree.Path != st.Worktree.Path {
		t.Fatalf("creation event = %#v, want the worktree ref", events[0].Payload)
	}
}

func TestThreadCreateFallsBackOnNonGitProject(t *testing.T) {
	f := newWorktreeFixture(t)
	plain := t.TempDir()
	f.disp.settings.AddProject(settings.Project{ID: "plain", Name: "plain", Path: plain})

	out := f.mustCall("thread.create", map[string]any{"title": "x", "projectId": "plain", "worktree": true})
	id := out.(map[string]any)["threadId"].(contracts.ThreadID)
	if st := f.state(id); st.Worktree != nil {
		t.Fatalf("non-git project got worktree %+v", st.Worktree)
	}
	f.disp.ensureWorktree(f.ctx, id)
	if st := f.state(id); st.Worktree != nil {
		t.Fatalf("ensure on non-git project got worktree %+v", st.Worktree)
	}
}

func TestSubthreadNeverCutsItsOwnWorktree(t *testing.T) {
	f := newWorktreeFixture(t)
	parent := f.createThread("parent", true)
	child := contracts.ThreadID("th_child")
	if err := f.eng.Dispatch(f.ctx, contracts.Command{
		ID: "c", Type: contracts.CmdThreadCreate, ThreadID: child, ProjectID: f.projectID,
		ParentThreadID: parent, Worktree: inheritWorktree(f.state(parent).Worktree),
	}); err != nil {
		t.Fatal(err)
	}
	f.disp.ensureWorktree(f.ctx, child)

	cst := f.state(child)
	if cst.Worktree == nil || cst.Worktree.Path != f.state(parent).Worktree.Path || !cst.Worktree.Inherited {
		t.Fatalf("subthread worktree = %+v, want inherited parent path", cst.Worktree)
	}
}

// The agents' cwd is proven in the orchestrator package. Here the RPCs an agent's
// work surfaces through (diff, file tree, file read, shell) all resolve to the
// thread's worktree in a real repository, and not to the project checkout.
func TestRPCsResolveInWorktreeOfRealRepo(t *testing.T) {
	f := newWorktreeFixture(t)
	id := f.createThread("Feature", true)
	ref := *f.state(id).Worktree

	if err := os.WriteFile(filepath.Join(ref.Path, "README.md"), []byte("agent edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ref.Path, "agent-only.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Untracked in the project checkout: visible there, never in the worktree.
	if err := os.WriteFile(filepath.Join(f.repo, "main-only.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := f.disp.threadWorkdir(f.ctx, id); !worktree.SamePath(got, ref.Path) {
		t.Fatalf("threadWorkdir = %q, want worktree %q", got, ref.Path)
	}

	diff, err := json.Marshal(f.mustCall("review.diff", map[string]any{"threadId": id}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diff), "agent edit") || strings.Contains(string(diff), "main-only") {
		t.Fatalf("review.diff did not read the worktree: %s", diff)
	}

	tree, err := json.Marshal(f.mustCall("files.tree", map[string]any{"threadId": id}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tree), "agent-only.txt") || strings.Contains(string(tree), "main-only.txt") {
		t.Fatalf("files.tree did not read the worktree: %s", tree)
	}

	read, err := json.Marshal(f.mustCall("files.read", map[string]any{"threadId": id, "path": "agent-only.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(read), "hi") {
		t.Fatalf("files.read missed the worktree file: %s", read)
	}

	skipWithoutPOSIXShell(t)
	if _, err := runCommandRPC(t, f.disp, string(id), "pwd"); err != nil {
		t.Fatalf("thread.runCommand: %v", err)
	}
	// The command's output is recorded as the thread's shell message.
	msgs := f.state(id).Messages
	pwd := msgs[len(msgs)-1].Text
	if !strings.Contains(pwd, filepath.Base(ref.Path)) {
		t.Fatalf("shell cwd is not the worktree: %q", pwd)
	}
}

func TestThreadDeleteKeepsWorktreeUnlessAsked(t *testing.T) {
	f := newWorktreeFixture(t)
	kept := f.createThread("keep me", true)
	keptPath := f.state(kept).Worktree.Path
	f.mustCall("thread.delete", map[string]any{"threadId": kept})
	if !f.exists(keptPath) {
		t.Fatal("thread.delete removed the worktree without the flag")
	}

	dirty := f.createThread("dirty", true)
	dirtyPath := f.state(dirty).Worktree.Path
	if err := os.WriteFile(filepath.Join(dirtyPath, "scratch.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call("thread.delete", map[string]any{"threadId": dirty, "removeWorktree": true}); err == nil {
		t.Fatal("delete with a dirty worktree was not refused")
	}
	if !f.exists(dirtyPath) || !f.state(dirty).Exists {
		t.Fatal("refused delete still removed the thread or its worktree")
	}

	clean := f.createThread("clean", true)
	cleanPath := f.state(clean).Worktree.Path
	f.mustCall("thread.delete", map[string]any{"threadId": clean, "removeWorktree": true})
	if f.exists(cleanPath) {
		t.Fatal("delete with removeWorktree left a clean worktree behind")
	}
}

func TestSubthreadDeleteNeverRemovesInheritedWorktree(t *testing.T) {
	f := newWorktreeFixture(t)
	parent := f.createThread("parent", true)
	parentPath := f.state(parent).Worktree.Path
	child := contracts.ThreadID("th_child")
	if err := f.eng.Dispatch(f.ctx, contracts.Command{
		ID: "c", Type: contracts.CmdThreadCreate, ThreadID: child, ProjectID: f.projectID,
		ParentThreadID: parent, Worktree: inheritWorktree(f.state(parent).Worktree),
	}); err != nil {
		t.Fatal(err)
	}
	f.mustCall("thread.delete", map[string]any{"threadId": child, "removeWorktree": true})
	if !f.exists(parentPath) {
		t.Fatal("deleting a subthread removed its parent's worktree")
	}
}

func TestWorktreeListShowsOwnerAndLiveFacts(t *testing.T) {
	f := newWorktreeFixture(t)
	id := f.createThread("listed", true)
	if err := os.WriteFile(filepath.Join(f.state(id).Worktree.Path, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := f.mustCall("worktree.list", map[string]any{"projectId": f.projectID})
	views, ok := out.([]WorktreeView)
	if !ok || len(views) != 1 {
		t.Fatalf("worktree.list = %#v, want one view", out)
	}
	v := views[0]
	if !v.Exists || !v.Dirty || v.Prunable || v.Base != "main" {
		t.Fatalf("view = %+v", v)
	}
	if len(v.ThreadIDs) != 1 || v.ThreadIDs[0] != id {
		t.Fatalf("owner threads = %v, want [%s]", v.ThreadIDs, id)
	}
}

func TestWorktreeListEmptyForNonGitProject(t *testing.T) {
	f := newWorktreeFixture(t)
	f.disp.settings.AddProject(settings.Project{ID: "plain", Name: "plain", Path: t.TempDir()})
	out := f.mustCall("worktree.list", map[string]any{"projectId": "plain"})
	if views, ok := out.([]WorktreeView); !ok || len(views) != 0 {
		t.Fatalf("worktree.list = %#v, want empty", out)
	}
}

func TestWorktreeCreateIsExplicitAndOnce(t *testing.T) {
	f := newWorktreeFixture(t)
	out := f.mustCall("thread.create", map[string]any{"title": "later", "projectId": f.projectID})
	id := out.(map[string]any)["threadId"].(contracts.ThreadID)

	f.mustCall("worktree.create", map[string]any{"threadId": id})
	if st := f.state(id); st.Worktree == nil || !f.exists(st.Worktree.Path) {
		t.Fatalf("worktree.create did not attach: %+v", st.Worktree)
	}
	if _, err := f.call("worktree.create", map[string]any{"threadId": id}); err == nil {
		t.Fatal("second worktree.create succeeded")
	}
}

func TestWorktreeCreateReportsFailureInsteadOfFallingBack(t *testing.T) {
	f := newWorktreeFixture(t)
	f.disp.settings.AddProject(settings.Project{ID: "plain", Name: "plain", Path: t.TempDir()})
	out := f.mustCall("thread.create", map[string]any{"title": "x", "projectId": "plain"})
	id := out.(map[string]any)["threadId"].(contracts.ThreadID)
	_, err := f.call("worktree.create", map[string]any{"threadId": id})
	if !errors.Is(err, worktree.ErrNotRepoRoot) {
		t.Fatalf("err = %v, want ErrNotRepoRoot", err)
	}
}

func TestWorktreeRemoveRefusedWhileTurnRuns(t *testing.T) {
	f := newWorktreeFixture(t)
	id := f.createThread("busy", true)
	path := f.state(id).Worktree.Path

	f.disp.turnsMu.Lock()
	f.disp.turns = map[contracts.ThreadID]context.CancelFunc{id: func() {}}
	f.disp.turnsMu.Unlock()

	if _, err := f.call("worktree.remove", map[string]any{"projectId": f.projectID, "path": path, "force": true}); err == nil {
		t.Fatal("remove succeeded while a turn ran in the worktree")
	}
	if !f.exists(path) {
		t.Fatal("refused remove deleted the worktree")
	}

	f.disp.turnsMu.Lock()
	delete(f.disp.turns, id)
	f.disp.turnsMu.Unlock()
	f.mustCall("worktree.remove", map[string]any{"projectId": f.projectID, "path": path})
	if f.exists(path) {
		t.Fatal("remove after the turn ended left the worktree")
	}
}

func TestWorktreePruneDropsVanishedDirectory(t *testing.T) {
	f := newWorktreeFixture(t)
	id := f.createThread("vanish", true)
	if err := os.RemoveAll(f.state(id).Worktree.Path); err != nil {
		t.Fatal(err)
	}

	out := f.mustCall("worktree.list", map[string]any{"projectId": f.projectID})
	if v := out.([]WorktreeView); len(v) != 1 || !v[0].Prunable || v[0].Exists {
		t.Fatalf("missing worktree not reported prunable: %+v", v)
	}
	f.mustCall("worktree.prune", map[string]any{"projectId": f.projectID})
	out = f.mustCall("worktree.list", map[string]any{"projectId": f.projectID})
	if v := out.([]WorktreeView); len(v) != 0 {
		t.Fatalf("prune left %d worktrees", len(v))
	}
}

// commitIn commits one file in dir, so a worktree's branch carries work and
// survives its removal: the next cut then gets a new branch and path.
func commitIn(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "work")
}

// A worktree removed through worktree.remove leaves the thread with no ref, so
// its next turn cuts a new one. The new worktree is where the agents' work
// resolves; the project checkout is never used.
func TestRemovedWorktreeIsReplacedOnNextTurn(t *testing.T) {
	f := newWorktreeFixture(t)
	id := f.createThread("Feature", true)
	old := *f.state(id).Worktree
	commitIn(t, old.Path, "agent.txt")
	f.mustCall("worktree.remove", map[string]any{"projectId": f.projectID, "path": old.Path})

	if err := f.disp.ensureWorktree(f.ctx, id); err != nil {
		t.Fatalf("next turn gate: %v", err)
	}
	fresh := f.state(id).Worktree
	if fresh == nil || fresh.Inherited || fresh.Path == old.Path || fresh.Branch == old.Branch {
		t.Fatalf("ref after removal = %+v, want a freshly cut worktree (old %+v)", fresh, old)
	}
	if !f.exists(fresh.Path) {
		t.Fatalf("fresh worktree %q does not exist", fresh.Path)
	}
	if got := f.disp.threadWorkdir(f.ctx, id); !worktree.SamePath(got, fresh.Path) {
		t.Fatalf("threadWorkdir = %q, want the fresh worktree %q", got, fresh.Path)
	}

	// The agent's files, shell and tree all land in the new worktree.
	if err := os.WriteFile(filepath.Join(fresh.Path, "fresh.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "main-only.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := json.Marshal(f.mustCall("files.tree", map[string]any{"threadId": id}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tree), "fresh.txt") || strings.Contains(string(tree), "main-only.txt") {
		t.Fatalf("files.tree did not read the fresh worktree: %s", tree)
	}
	skipWithoutPOSIXShell(t)
	if _, err := runCommandRPC(t, f.disp, string(id), "pwd"); err != nil {
		t.Fatalf("thread.runCommand: %v", err)
	}
	msgs := f.state(id).Messages
	if pwd := msgs[len(msgs)-1].Text; !strings.Contains(pwd, filepath.Base(fresh.Path)) {
		t.Fatalf("shell cwd is not the fresh worktree: %q", pwd)
	}

	// The project checkout is untouched by the whole sequence.
	if f.exists(filepath.Join(f.repo, "agent.txt")) {
		t.Fatal("agent work leaked into the project checkout")
	}
}

// A worktree whose directory was deleted by hand is re-cut by the gate, never
// replaced with the project path.
func TestHandDeletedWorktreeIsRecutNeverProjectPath(t *testing.T) {
	f := newWorktreeFixture(t)
	id := f.createThread("Vanished", true)
	lost := f.state(id).Worktree.Path
	if err := os.RemoveAll(lost); err != nil {
		t.Fatal(err)
	}

	if err := f.disp.ensureWorktree(f.ctx, id); err != nil {
		t.Fatalf("gate: %v", err)
	}
	fresh := f.state(id).Worktree
	if fresh == nil || fresh.Path == lost || !f.exists(fresh.Path) {
		t.Fatalf("ref = %+v, want a re-cut directory", fresh)
	}
	if got := f.disp.threadWorkdir(f.ctx, id); worktree.SamePath(got, f.repo) {
		t.Fatalf("threadWorkdir = project checkout %q", got)
	}
}

// A failed re-cut fails the turn and keeps the thread's ref, so the next turn
// retries. It must not degrade to the project path.
func TestFailedRecutFailsTurnAndKeepsRef(t *testing.T) {
	f := newWorktreeFixture(t)
	id := f.createThread("Blocked", true)
	lost := f.state(id).Worktree.Path
	if err := os.RemoveAll(lost); err != nil {
		t.Fatal(err)
	}

	// git records each worktree under .git/worktrees; with that directory read-only
	// no new worktree can be recorded.
	gitDir := filepath.Join(f.repo, ".git", "worktrees")
	if !f.exists(gitDir) {
		t.Fatalf("%s missing; the first cut should have created it", gitDir)
	}
	if err := os.Chmod(gitDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

	if err := f.disp.ensureWorktree(f.ctx, id); err == nil {
		t.Fatal("gate passed although no worktree could be cut")
	}
	if ref := f.state(id).Worktree; ref == nil || ref.Path != lost {
		t.Fatalf("failed re-cut changed the ref: %+v", ref)
	}
	if got := f.disp.threadWorkdir(f.ctx, id); worktree.SamePath(got, f.repo) {
		t.Fatalf("failed re-cut resolved to the project checkout %q", got)
	}

	if err := os.Chmod(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.disp.ensureWorktree(f.ctx, id); err != nil {
		t.Fatalf("retry after repair: %v", err)
	}
	if fresh := f.state(id).Worktree; fresh == nil || fresh.Path == lost || !f.exists(fresh.Path) {
		t.Fatalf("retry did not re-cut: %+v", fresh)
	}
}

// A subthread follows its parent: once the parent's worktree is removed, the
// subthread's turn fails naming the parent until the parent's own turn cuts a
// new one, and then the subthread re-points at it.
func TestSubthreadFollowsParentsNewWorktree(t *testing.T) {
	f := newWorktreeFixture(t)
	parent := f.createThread("parent", true)
	old := *f.state(parent).Worktree
	child := contracts.ThreadID("th_child")
	if err := f.eng.Dispatch(f.ctx, contracts.Command{
		ID: "c", Type: contracts.CmdThreadCreate, ThreadID: child, ProjectID: f.projectID,
		ParentThreadID: parent, Worktree: inheritWorktree(&old),
	}); err != nil {
		t.Fatal(err)
	}
	commitIn(t, old.Path, "parent.txt")
	f.mustCall("worktree.remove", map[string]any{"projectId": f.projectID, "path": old.Path})

	err := f.disp.ensureWorktree(f.ctx, child)
	if err == nil || !strings.Contains(err.Error(), string(parent)) {
		t.Fatalf("subthread gate err = %v, want a failure naming parent %s", err, parent)
	}

	if err := f.disp.ensureWorktree(f.ctx, parent); err != nil {
		t.Fatalf("parent gate: %v", err)
	}
	fresh := f.state(parent).Worktree
	if fresh == nil || fresh.Path == old.Path {
		t.Fatalf("parent ref = %+v, want a new worktree", fresh)
	}

	if err := f.disp.ensureWorktree(f.ctx, child); err != nil {
		t.Fatalf("subthread gate after parent recut: %v", err)
	}
	cst := f.state(child).Worktree
	if cst == nil || cst.Path != fresh.Path || !cst.Inherited {
		t.Fatalf("subthread ref = %+v, want inherited %q", cst, fresh.Path)
	}
}

// The fallback for a project that cannot have a worktree is audited once, however
// many turns run; the thread keeps running on the project path.
func TestFailedCutAuditsOnceAcrossTurns(t *testing.T) {
	f := newWorktreeFixture(t)
	f.disp.settings.AddProject(settings.Project{ID: "plain", Name: "plain", Path: t.TempDir()})
	out := f.mustCall("thread.create", map[string]any{"title": "x", "projectId": "plain"})
	id := out.(map[string]any)["threadId"].(contracts.ThreadID)

	var fallbacks int
	f.disp.audit = func(kind string, _ map[string]any) {
		if kind == "worktree.fallback" {
			fallbacks++
		}
	}
	for turn := 0; turn < 2; turn++ {
		if err := f.disp.ensureWorktree(f.ctx, id); err != nil {
			t.Fatalf("turn %d gate: %v", turn, err)
		}
	}
	if fallbacks != 1 {
		t.Fatalf("worktree.fallback audited %d times, want 1", fallbacks)
	}
	if st := f.state(id); st.Worktree != nil {
		t.Fatalf("non-git project got a worktree %+v", st.Worktree)
	}
}

// A plain folder that later becomes a repository gets its worktree on the next
// turn, with no restart. The fallback stays audited once across the change: the
// retry is silent while the reason is unchanged.
func TestPlainFolderBecomesRepoAndGetsWorktreeOnNextTurn(t *testing.T) {
	f := newWorktreeFixture(t)
	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "notes.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.disp.settings.AddProject(settings.Project{ID: "plain", Name: "plain", Path: plain})
	out := f.mustCall("thread.create", map[string]any{"title": "later", "projectId": "plain"})
	id := out.(map[string]any)["threadId"].(contracts.ThreadID)

	var fallbacks int
	f.disp.audit = func(kind string, _ map[string]any) {
		if kind == "worktree.fallback" {
			fallbacks++
		}
	}
	for turn := 0; turn < 2; turn++ {
		if err := f.disp.ensureWorktree(f.ctx, id); err != nil {
			t.Fatalf("plain turn %d: %v", turn, err)
		}
	}
	if st := f.state(id); st.Worktree != nil {
		t.Fatalf("plain folder got a worktree %+v", st.Worktree)
	}

	gitRun(t, plain, "init", "-q", "-b", "main")
	gitRun(t, plain, "add", "notes.txt")
	gitRun(t, plain, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "init")

	if err := f.disp.ensureWorktree(f.ctx, id); err != nil {
		t.Fatalf("turn after git init: %v", err)
	}
	ref := f.state(id).Worktree
	if ref == nil || ref.Inherited || !worktree.SamePath(filepath.Dir(ref.Path), worktree.Dir(plain)) {
		t.Fatalf("worktree after git init = %+v, want a cut beside the project", ref)
	}
	if fallbacks != 1 {
		t.Fatalf("worktree.fallback audited %d times across the change, want 1", fallbacks)
	}
}

// A provisional thread (created with worktree:true, never turned yet) still owns
// its worktree in memory. Removing that worktree must clear the ref on it too.
func TestRemoveDetachesProvisionalOwner(t *testing.T) {
	f := newWorktreeFixture(t)
	out := f.mustCall("thread.create", map[string]any{"title": "eager", "projectId": f.projectID, "worktree": true})
	id := out.(map[string]any)["threadId"].(contracts.ThreadID)
	ref := f.state(id).Worktree
	if ref == nil {
		t.Fatal("eager create cut no worktree")
	}
	if st := f.state(id); !st.Provisional {
		t.Fatalf("thread is durable (%+v); the test needs a provisional owner", st)
	}

	f.mustCall("worktree.remove", map[string]any{"projectId": f.projectID, "path": ref.Path})
	if st := f.state(id); st.Worktree != nil {
		t.Fatalf("provisional owner still holds %+v after its worktree was removed", st.Worktree)
	}
}

// Re-cutting a vanished worktree is one replace event in the durable log. A
// detach-then-attach pair would leave a moment with no ref, so neither event
// type may appear for the recut.
func TestRecutWritesOneReplaceEventAndNoDetach(t *testing.T) {
	f := newWorktreeFixture(t)
	id := f.createThread("Window", true)
	lost := f.state(id).Worktree.Path
	if err := os.RemoveAll(lost); err != nil {
		t.Fatal(err)
	}
	// Make the thread durable first, so the log holds every event the recut writes.
	if err := f.disp.engine.Dispatch(f.ctx, contracts.Command{ID: "t1", Type: contracts.CmdTurnStart, ThreadID: id, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.Load(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.disp.ensureWorktree(f.ctx, id); err != nil {
		t.Fatalf("gate: %v", err)
	}
	after, err := f.store.Load(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var detached, replaced int
	for _, ev := range after[len(before):] {
		switch ev.Payload.(type) {
		case contracts.WorktreeDetached:
			detached++
		case contracts.WorktreeReplaced:
			replaced++
		}
	}
	if detached != 0 || replaced != 1 {
		t.Fatalf("recut wrote %d detach and %d replace events, want 0 and 1", detached, replaced)
	}
}
