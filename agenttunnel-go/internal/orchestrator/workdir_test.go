package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/worktree"
)

// initWorkdirRepo makes a real repository with one commit on main.
func initWorkdirRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

// The agents' cwd is the thread's worktree once it has one, and the project path
// before that or after the worktree is gone from disk.
func TestWorkdirForPrefersThreadWorktree(t *testing.T) {
	ctx := context.Background()
	repo := initWorkdirRepo(t)

	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	store := settings.New(t.TempDir())
	store.AddProject(settings.Project{ID: "p1", Name: "proj", Path: repo})
	r := NewRunner(Deps{Engine: eng, Settings: store})

	plain := contracts.ThreadID("th_plain")
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: plain, ProjectID: "p1"}); err != nil {
		t.Fatal(err)
	}
	if got := r.workdirFor(ctx, plain); got != repo {
		t.Fatalf("thread without worktree: workdir = %q, want project path %q", got, repo)
	}

	ref, err := worktree.Create(ctx, repo, "feature", "th_wtwtwtwt")
	if err != nil {
		t.Fatal(err)
	}
	wt := contracts.ThreadID("th_wt")
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c2", Type: contracts.CmdThreadCreate, ThreadID: wt, ProjectID: "p1", Worktree: &ref,
	}); err != nil {
		t.Fatal(err)
	}
	if got := r.workdirFor(ctx, wt); got != ref.Path {
		t.Fatalf("thread with worktree: workdir = %q, want %q", got, ref.Path)
	}

	if err := os.RemoveAll(ref.Path); err != nil {
		t.Fatal(err)
	}
	if got := r.workdirFor(ctx, wt); got != repo {
		t.Fatalf("worktree gone from disk: workdir = %q, want project path %q", got, repo)
	}
}
