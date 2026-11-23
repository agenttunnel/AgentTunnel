package checkpoint

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// initRepo makes a repository with one committed file and a .gitignore that
// hides build/. The state dir sits outside it, as in production.
func initRepo(t *testing.T) (repo string, store *Store) {
	t.Helper()
	repo = t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "dev@example.com")
	runGit(t, repo, "config", "user.name", "Dev")
	writeFile(t, repo, "kept.txt", "one\n")
	writeFile(t, repo, ".gitignore", "build/\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "init")
	return repo, New(t.TempDir())
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustRef(t *testing.T, threadID, checkpointID, phase string) string {
	t.Helper()
	ref, err := CheckpointRef(threadID, checkpointID, phase)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestCaptureRecordsTrackedUntrackedAndDeletions(t *testing.T) {
	repo, store := initRepo(t)
	writeFile(t, repo, "kept.txt", "two\n")          // tracked modification
	writeFile(t, repo, "new.txt", "hello\n")         // untracked file
	writeFile(t, repo, "build/out.bin", "ignored\n") // ignored, must not be captured
	ctx := context.Background()

	snap, err := store.Capture(ctx, repo, mustRef(t, "th_1", "ck_a", "baseline"))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	files := strings.Fields(runGit(t, repo, "ls-tree", "-r", "--name-only", snap.Tree))
	if !slices.Contains(files, "new.txt") || !slices.Contains(files, "kept.txt") {
		t.Fatalf("tree is missing the changed files: %v", files)
	}
	if slices.Contains(files, "build/out.bin") {
		t.Fatalf("ignored file entered the tree: %v", files)
	}
	if got := strings.TrimSpace(runGit(t, repo, "rev-parse", mustRef(t, "th_1", "ck_a", "baseline"))); got != snap.Ref {
		t.Fatalf("ref points at %s, want %s", got, snap.Ref)
	}
}

func TestCaptureLeavesIndexAndStatusUnchanged(t *testing.T) {
	repo, store := initRepo(t)
	writeFile(t, repo, "kept.txt", "two\n")
	writeFile(t, repo, "new.txt", "hello\n")
	before := runGit(t, repo, "status", "--porcelain")
	indexBefore := runGit(t, repo, "ls-files", "--stage")

	if _, err := store.Capture(context.Background(), repo, mustRef(t, "th_1", "ck_a", "baseline")); err != nil {
		t.Fatal(err)
	}
	if after := runGit(t, repo, "status", "--porcelain"); after != before {
		t.Fatalf("status changed:\nbefore %q\nafter  %q", before, after)
	}
	if after := runGit(t, repo, "ls-files", "--stage"); after != indexBefore {
		t.Fatalf("index changed")
	}
}

func TestCaptureWorksBeforeFirstCommit(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	writeFile(t, repo, "a.txt", "a\n")
	store := New(t.TempDir())
	snap, err := store.Capture(context.Background(), repo, mustRef(t, "th_1", "ck_a", "baseline"))
	if err != nil {
		t.Fatalf("capture without commits: %v", err)
	}
	if snap.Tree == "" {
		t.Fatal("no tree")
	}
}

func TestCaptureOutsideRepoIsNotGit(t *testing.T) {
	store := New(t.TempDir())
	_, err := store.Capture(context.Background(), t.TempDir(), mustRef(t, "th_1", "ck_a", "baseline"))
	if !errors.Is(err, ErrNotGitRepo) {
		t.Fatalf("err = %v, want ErrNotGitRepo", err)
	}
}

func TestStatsCountsFilesAndLines(t *testing.T) {
	repo, store := initRepo(t)
	ctx := context.Background()
	base, err := store.Capture(ctx, repo, mustRef(t, "th_1", "ck_a", "baseline"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "kept.txt", "one\ntwo\n")                         // +1 line
	writeFile(t, repo, "new.txt", "x\ny\n")                              // new file, +2
	if err := os.Remove(filepath.Join(repo, ".gitignore")); err != nil { // -1 line
		t.Fatal(err)
	}
	result, err := store.Capture(ctx, repo, mustRef(t, "th_1", "ck_a", "result"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Stats(ctx, repo, base.Tree, result.Tree)
	if err != nil {
		t.Fatal(err)
	}
	want := Stats{Files: 3, Additions: 3, Deletions: 1}
	if st != want {
		t.Fatalf("stats = %+v, want %+v", st, want)
	}
}

func TestBinaryFileCountsAsFileWithoutLines(t *testing.T) {
	repo, store := initRepo(t)
	ctx := context.Background()
	base, err := store.Capture(ctx, repo, mustRef(t, "th_1", "ck_a", "baseline"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "img.bin"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := store.Capture(ctx, repo, mustRef(t, "th_1", "ck_a", "result"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Stats(ctx, repo, base.Tree, result.Tree)
	if err != nil {
		t.Fatal(err)
	}
	if st != (Stats{Files: 1}) {
		t.Fatalf("binary stats = %+v, want one file with no lines", st)
	}
}

func TestParseNumstatCountsBinaryAsFile(t *testing.T) {
	got := ParseNumstat("3\t1\ta.go\x00-\t-\timg.png\x00")
	want := Stats{Files: 2, Additions: 3, Deletions: 1}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestRefLayoutRejectsBadIDs(t *testing.T) {
	if _, err := CheckpointRef("../evil", "ck_a", "baseline"); err == nil {
		t.Fatal("path traversal id accepted")
	}
	if _, err := SafetyRef("th_1", "rw one"); err == nil {
		t.Fatal("space in id accepted")
	}
	ref, err := CheckpointRef("th_1", "ck_a", "result")
	if err != nil || ref != "refs/agenttunnel/checkpoints/th_1/ck_a/result" {
		t.Fatalf("ref = %q, %v", ref, err)
	}
}

func TestDeleteThreadRemovesOnlyThatThreadsRefs(t *testing.T) {
	repo, store := initRepo(t)
	ctx := context.Background()
	mine := mustRef(t, "th_1", "ck_a", "baseline")
	other := mustRef(t, "th_2", "ck_a", "baseline")
	if _, err := store.Capture(ctx, repo, mine); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Capture(ctx, repo, other); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteThread(ctx, repo, "th_1"); err != nil {
		t.Fatal(err)
	}
	if out := runGit(t, repo, "for-each-ref", "refs/agenttunnel/"); strings.Contains(out, "th_1") || !strings.Contains(out, "th_2") {
		t.Fatalf("refs after delete:\n%s", out)
	}
}
