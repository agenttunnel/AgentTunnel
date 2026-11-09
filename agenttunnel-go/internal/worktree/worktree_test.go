package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// initRepo makes a real repository with one commit on main, the shape a
// project checkout has.
func initRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-q", "-m", "init")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestCreateCutsFreshBranchOutsideRepo(t *testing.T) {
	repo := initRepo(t)
	ref, err := Create(context.Background(), repo, "Fix the login bug!", "th_0123456789abcdef")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ref.Base != "main" {
		t.Fatalf("base = %q, want main", ref.Base)
	}
	if ref.Branch != "agenttunnel/fix-the-login-bug-01234567" {
		t.Fatalf("branch = %q", ref.Branch)
	}
	if ref.Branch == ref.Base {
		t.Fatal("worktree reused the base branch")
	}
	if want := Dir(repo); filepath.Dir(ref.Path) != want {
		t.Fatalf("worktree %q is not inside %q", ref.Path, want)
	}
	if !strings.HasPrefix(ref.Path, repo+".worktrees") {
		t.Fatalf("worktree %q is not a sibling of the repo", ref.Path)
	}
	if got := strings.TrimSpace(runGit(t, ref.Path, "rev-parse", "--abbrev-ref", "HEAD")); got != ref.Branch {
		t.Fatalf("worktree HEAD = %q, want %q", got, ref.Branch)
	}
	// The project checkout keeps its own branch and sees no nested checkout.
	if got := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Fatalf("project HEAD moved to %q", got)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("project status is dirty: %q", status)
	}
}

func TestCreateFallsBackOnNonRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(context.Background(), dir, "x", "th_aa"); !errors.Is(err, ErrNotRepoRoot) {
		t.Fatalf("err = %v, want ErrNotRepoRoot", err)
	}
}

func TestCreateRefusesSubdirectoryOfRepo(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), sub, "x", "th_aa"); !errors.Is(err, ErrNotRepoRoot) {
		t.Fatalf("err = %v, want ErrNotRepoRoot", err)
	}
}

func TestCreateFallsBackOnEmptyRepo(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	if _, err := Create(context.Background(), repo, "x", "th_aa"); !errors.Is(err, ErrNoBase) {
		t.Fatalf("err = %v, want ErrNoBase", err)
	}
}

func TestCreateFallsBackWhenParentReadOnly(t *testing.T) {
	repo := initRepo(t)
	parent := filepath.Dir(repo)
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if _, err := Create(context.Background(), repo, "x", "th_aa"); err == nil {
		t.Fatal("Create succeeded in a read-only parent")
	}
}

func TestListReportsLiveFacts(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	ref, err := Create(ctx, repo, "feature", "th_11111111")
	if err != nil {
		t.Fatal(err)
	}
	// One commit ahead of main, then a dirty file.
	if err := os.WriteFile(filepath.Join(ref.Path, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ref.Path, "add", "a.txt")
	runGit(t, ref.Path, "commit", "-q", "-m", "work")
	if err := os.WriteFile(filepath.Join(ref.Path, "dirty.txt"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}

	infos, err := List(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d worktrees, want 1", len(infos))
	}
	got := infos[0]
	if got.Branch != ref.Branch || !got.Exists || got.Prunable {
		t.Fatalf("info = %+v", got)
	}
	if !got.Dirty || got.Ahead != 1 || got.Behind != 0 {
		t.Fatalf("dirty/ahead/behind = %v/%d/%d, want true/1/0", got.Dirty, got.Ahead, got.Behind)
	}
}

func TestListMarksMissingDirectoryPrunable(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	ref, err := Create(ctx, repo, "gone", "th_22222222")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ref.Path); err != nil {
		t.Fatal(err)
	}
	infos, err := List(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Exists || !infos[0].Prunable {
		t.Fatalf("infos = %+v, want one missing prunable worktree", infos)
	}
}

func TestRemoveRefusesDirtyUnlessForced(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	ref, err := Create(ctx, repo, "dirty", "th_33333333")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ref.Path, "scratch.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, repo, ref.Path, false); !errors.Is(err, ErrDirty) {
		t.Fatalf("err = %v, want ErrDirty", err)
	}
	if _, err := os.Stat(ref.Path); err != nil {
		t.Fatal("refused remove still deleted the worktree")
	}
	if err := Remove(ctx, repo, ref.Path, true); err != nil {
		t.Fatalf("forced Remove: %v", err)
	}
	if _, err := os.Stat(ref.Path); !os.IsNotExist(err) {
		t.Fatal("forced remove left the directory behind")
	}
}

func TestRemoveDeletesMergedBranchOnly(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	ref, err := Create(ctx, repo, "clean", "th_44444444")
	if err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, repo, ref.Path, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// A fresh branch with no commits is merged, so git deletes it.
	if exists(ctx, repo, ref.Branch) {
		t.Fatal("merged branch survived removal")
	}
}

func TestRemoveRejectsPathOutsideManagedDir(t *testing.T) {
	repo := initRepo(t)
	if err := Remove(context.Background(), repo, repo, true); !errors.Is(err, ErrUnmanaged) {
		t.Fatalf("err = %v, want ErrUnmanaged", err)
	}
}

func TestPruneDropsMissingWorktree(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	ref, err := Create(ctx, repo, "stale", "th_55555555")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ref.Path); err != nil {
		t.Fatal(err)
	}
	if err := Prune(ctx, repo); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	infos, err := List(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("prune left %d managed worktrees", len(infos))
	}
}

func TestResolvePrefersExistingWorktree(t *testing.T) {
	wt := t.TempDir()
	fallback := "/project"

	if got := Resolve(&contracts.WorktreeRef{Path: wt}, fallback); got != wt {
		t.Fatalf("Resolve = %q, want worktree", got)
	}
	if got := Resolve(nil, fallback); got != fallback {
		t.Fatalf("Resolve(nil) = %q, want fallback", got)
	}
	missing := &contracts.WorktreeRef{Path: filepath.Join(wt, "gone")}
	if got := Resolve(missing, fallback); got != fallback {
		t.Fatalf("Resolve(missing) = %q, want fallback", got)
	}
}
