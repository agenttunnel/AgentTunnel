package scm

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// recording wraps a scripted CLI and remembers each command it was asked to run,
// so a test can assert what ran and what was deliberately not run.
func recording(f fakeCLI) (Client, *[]string) {
	var calls []string
	c := f.client()
	run := c.Run
	c.Run = func(ctx context.Context, dir, name string, args ...string) ([]byte, string, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return run(ctx, dir, name, args...)
	}
	return c, &calls
}

func TestCommitRequiresAMessageAndRunsNothing(t *testing.T) {
	c, calls := recording(githubRepo(nil))
	if _, err := c.Commit(context.Background(), "/repo", "   ", true); err == nil {
		t.Fatal("blank message: want an error")
	}
	if len(*calls) != 0 {
		t.Fatalf("blank message ran %v, want nothing", *calls)
	}
}

func TestCommitAllStagesThenCommits(t *testing.T) {
	f := githubRepo(map[string]reply{
		"git add -A":           {},
		"git commit -m fix it": {stdout: "[feat/x 1a2b3c] fix it\n 2 files changed\n"},
	})
	c, calls := recording(f)
	msg, err := c.Commit(context.Background(), "/repo", " fix it ", true)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if msg != "[feat/x 1a2b3c] fix it" {
		t.Fatalf("message = %q", msg)
	}
	if !slices.Equal(*calls, []string{"git add -A", "git commit -m fix it"}) {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestCommitStagedOnlyLeavesIndexAlone(t *testing.T) {
	f := githubRepo(map[string]reply{"git commit -m fix it": {stdout: "[feat/x 1a2b3c] fix it\n"}})
	c, calls := recording(f)
	if _, err := c.Commit(context.Background(), "/repo", "fix it", false); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !slices.Equal(*calls, []string{"git commit -m fix it"}) {
		t.Fatalf("staged-only commit ran %v, want just the commit", *calls)
	}
}

func TestCommitNothingStagedSurfacesGitsReason(t *testing.T) {
	// git reports "nothing to commit" on stdout and exits 1, so stderr is empty.
	f := githubRepo(map[string]reply{
		"git commit -m fix it": {stdout: "On branch feat/x\nnothing to commit, working tree clean\n", err: errors.New("exit 1")},
	})
	c, _ := recording(f)
	_, err := c.Commit(context.Background(), "/repo", "fix it", false)
	if err == nil || !strings.Contains(err.Error(), "nothing to commit, working tree clean") {
		t.Fatalf("err = %v, want git's own reason", err)
	}
}

func TestPushSetsUpstreamWithoutForce(t *testing.T) {
	f := githubRepo(map[string]reply{
		"git symbolic-ref --short -q HEAD": {stdout: "feat/x\n"},
		"git push -u origin HEAD":          {stderr: "To github.com:x/y.git\n"},
	})
	c, calls := recording(f)
	msg, err := c.Push(context.Background(), "/repo")
	if err != nil || msg != "Pushed" {
		t.Fatalf("Push = %q, %v", msg, err)
	}
	if !slices.Equal(*calls, []string{"git symbolic-ref --short -q HEAD", "git push -u origin HEAD"}) {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestPushAndPullRefuseDetachedHEAD(t *testing.T) {
	// A detached HEAD prints nothing from symbolic-ref and exits 1.
	f := githubRepo(map[string]reply{
		"git symbolic-ref --short -q HEAD": {err: errors.New("exit 1")},
	})
	c, calls := recording(f)
	if _, err := c.Push(context.Background(), "/repo"); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("detached push err = %v", err)
	}
	if _, err := c.Pull(context.Background(), "/repo"); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("detached pull err = %v", err)
	}
	if slices.ContainsFunc(*calls, func(s string) bool { return strings.HasPrefix(s, "git push") || strings.HasPrefix(s, "git pull") }) {
		t.Fatalf("detached HEAD still ran %v", *calls)
	}
}

func TestPullFastForwardsOnly(t *testing.T) {
	f := githubRepo(map[string]reply{
		"git symbolic-ref --short -q HEAD": {stdout: "feat/x\n"},
		"git pull --ff-only":               {stdout: "Already up to date.\n"},
	})
	c, calls := recording(f)
	if _, err := c.Pull(context.Background(), "/repo"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !slices.Equal(*calls, []string{"git symbolic-ref --short -q HEAD", "git pull --ff-only"}) {
		t.Fatalf("calls = %v", *calls)
	}

	f = githubRepo(map[string]reply{
		"git symbolic-ref --short -q HEAD": {stdout: "feat/x\n"},
		"git pull --ff-only":               {stderr: "hint: diverged\nfatal: Not possible to fast-forward, aborting.\n", err: errors.New("exit 128")},
	})
	c, _ = recording(f)
	_, err := c.Pull(context.Background(), "/repo")
	if err == nil || !strings.Contains(err.Error(), "Not possible to fast-forward") {
		t.Fatalf("diverged pull err = %v", err)
	}
}

func TestCreateRequestRefusesNonGitHubAndMissingCLI(t *testing.T) {
	gitlab := fakeCLI{
		installed: map[string]bool{"gh": true},
		replies:   map[string]reply{gitOrigin: {stdout: "git@gitlab.com:x/y.git\n"}},
	}
	c, calls := recording(gitlab)
	if _, err := c.CreateRequest(context.Background(), "/repo", "t"); err == nil || !strings.Contains(err.Error(), "GitHub-only") {
		t.Fatalf("gitlab create err = %v", err)
	}
	if slices.Contains(*calls, "gh pr create --title t --body ") {
		t.Fatal("gitlab origin still ran gh pr create")
	}

	noGH := githubRepo(nil)
	noGH.installed = map[string]bool{}
	c, _ = recording(noGH)
	if _, err := c.MarkReady(context.Background(), "/repo"); err == nil || !strings.Contains(err.Error(), "gh is not installed") {
		t.Fatalf("missing gh err = %v", err)
	}
}

func TestCreateRequestFillsFromCommitsWhenTitleBlank(t *testing.T) {
	f := githubRepo(map[string]reply{
		"gh pr create --fill": {stdout: "https://github.com/agenttunnel/AgentTunnel/pull/101\n"},
	})
	c, calls := recording(f)
	url, err := c.CreateRequest(context.Background(), "/repo", "  ")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if url != "https://github.com/agenttunnel/AgentTunnel/pull/101" {
		t.Fatalf("url = %q", url)
	}
	if !slices.Equal(*calls, []string{"git remote get-url origin", "gh pr create --fill"}) {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestMergeSquashesAndReturnsGHReason(t *testing.T) {
	f := githubRepo(map[string]reply{"gh pr merge --squash": {}})
	c, calls := recording(f)
	if msg, err := c.Merge(context.Background(), "/repo"); err != nil || msg != "Merged" {
		t.Fatalf("Merge = %q, %v", msg, err)
	}
	if !slices.Contains(*calls, "gh pr merge --squash") {
		t.Fatalf("calls = %v", *calls)
	}

	f = githubRepo(map[string]reply{
		"gh pr merge --squash": {stderr: "X Pull request is not mergeable: the base branch policy prohibits the merge.\n", err: errors.New("exit 1")},
	})
	c, _ = recording(f)
	if _, err := c.Merge(context.Background(), "/repo"); err == nil || !strings.Contains(err.Error(), "not mergeable") {
		t.Fatalf("unmergeable err = %v", err)
	}
}
