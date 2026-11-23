package gitdiff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// RangeDiff compares two commits and ignores uncommitted work: the diff is what
// the checkpoint recorded, not whatever the worktree holds now.
func TestRangeDiffParsesCommitsAndIgnoresUncommittedChanges(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-qam", "change")
	head := git("rev-parse", "HEAD")
	// Uncommitted edit after the range: must not appear in the range diff.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := RangeDiff(context.Background(), repo, base, head)
	if err != nil {
		t.Fatalf("range diff: %v", err)
	}
	if len(d.Files) != 1 || d.Files[0].Path != "a.txt" || d.Files[0].Additions != 1 || d.Files[0].Deletions != 0 {
		t.Fatalf("range diff = %+v", d.Files)
	}
	if strings.Contains(d.Raw, "three") {
		t.Fatalf("range diff includes uncommitted change:\n%s", d.Raw)
	}
}
