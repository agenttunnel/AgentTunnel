package gitguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setupShim writes the shim next to a real repo with one commit, so refused
// writes can be checked for side effects and allowed reads have something to read.
func setupShim(t *testing.T) (shim, realGit, repo string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shim is a POSIX script")
	}
	var err error
	realGit, err = exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	if err := writeShim(dir, realGit); err != nil {
		t.Fatal(err)
	}
	repo = t.TempDir()
	mustGit(t, realGit, "-C", repo, "init", "-q")
	mustGit(t, realGit, "-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	return filepath.Join(dir, "git"), realGit, repo
}

func mustGit(t *testing.T, realGit string, args ...string) {
	t.Helper()
	if out, err := exec.Command(realGit, args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// runShim runs the shim with AT_GIT_POLICY set or unset, regardless of the
// test process's own environment.
func runShim(shim string, policy bool, args ...string) (string, error) {
	cmd := exec.Command(shim, args...)
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AT_GIT_POLICY=") {
			env = append(env, kv)
		}
	}
	if policy {
		env = append(env, "AT_GIT_POLICY=readonly")
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestShimRefusesStateChangesForAgents(t *testing.T) {
	shim, realGit, repo := setupShim(t)
	refused := [][]string{
		{"-C", repo, "commit", "--allow-empty", "-m", "agent"},
		{"-C", repo, "-c", "user.name=t", "commit", "--allow-empty", "-m", "agent"},
		{"-C", repo, "branch", "agent-made"},
		{"-C", repo, "checkout", "-b", "agent-made"},
		{"-C", repo, "reset", "--hard"},
		{"-C", repo, "stash"},
		{"-C", repo, "worktree", "add", filepath.Join(t.TempDir(), "wt")},
		{"--git-dir=" + filepath.Join(repo, ".git"), "add", "."},
		{"-C", repo, "push"},
	}
	for _, args := range refused {
		out, err := runShim(shim, true, args...)
		if err == nil {
			t.Fatalf("git %v should be refused for agents, got:\n%s", args, out)
		}
		if !strings.Contains(out, "reserved for the team lead") {
			t.Fatalf("git %v refusal should name the lead:\n%s", args, out)
		}
	}
	// A refused branch must not exist: the guard blocks before real git runs.
	if err := exec.Command(realGit, "-C", repo, "rev-parse", "--verify", "-q", "agent-made").Run(); err == nil {
		t.Fatal("refused git branch still created agent-made")
	}
}

func TestShimAllowsReadOnlyGitForAgents(t *testing.T) {
	shim, _, repo := setupShim(t)
	allowed := [][]string{
		{"-C", repo, "status", "--short"},
		{"-C", repo, "diff"},
		{"-C", repo, "log", "--oneline"},
		{"-C", repo, "show", "--stat", "HEAD"},
		{"-C", repo, "rev-parse", "--abbrev-ref", "HEAD"},
		{"-C", repo, "-c", "core.quotepath=off", "status"},
	}
	for _, args := range allowed {
		if out, err := runShim(shim, true, args...); err != nil {
			t.Fatalf("read-only git %v should pass, got %v:\n%s", args, err, out)
		}
	}
}

func TestShimPassesEverythingThroughWithoutPolicy(t *testing.T) {
	shim, realGit, repo := setupShim(t)
	if out, err := runShim(shim, false, "-C", repo, "branch", "lead-made"); err != nil {
		t.Fatalf("without the policy the shim must pass through, got %v:\n%s", err, out)
	}
	if err := exec.Command(realGit, "-C", repo, "rev-parse", "--verify", "-q", "lead-made").Run(); err != nil {
		t.Fatal("pass-through branch was not created")
	}
}

func TestEnvPutsShimFirstOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shim is a POSIX script")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	env := Env()
	if len(env) != 2 || env[0] != "AT_GIT_POLICY=readonly" || !strings.HasPrefix(env[1], "PATH=") {
		t.Fatalf("Env() = %v, want policy plus PATH", env)
	}
	first := strings.Split(strings.TrimPrefix(env[1], "PATH="), string(os.PathListSeparator))[0]
	if _, err := os.Stat(filepath.Join(first, "git")); err != nil {
		t.Fatalf("first PATH entry %q has no git shim: %v", first, err)
	}
}
