package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreReturnsModifiedAndDeletedFiles(t *testing.T) {
	repo, store := initRepo(t)
	ctx := context.Background()
	writeFile(t, repo, "kept.txt", "turn\n")
	target, err := store.Capture(ctx, repo, mustRef(t, "th_1", "ck_a", "baseline"))
	if err != nil {
		t.Fatal(err)
	}
	// Changes after the target: an edit and a new file.
	writeFile(t, repo, "kept.txt", "edited later\n")
	writeFile(t, repo, "added/after.txt", "new\n")

	rep, err := store.Restore(ctx, repo, target.Tree)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, repo, "kept.txt"); got != "turn\n" {
		t.Fatalf("kept.txt = %q, want the target content", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "added")); !os.IsNotExist(err) {
		t.Fatalf("file added after the target survived (stat err %v)", err)
	}
	if len(rep.Deleted) != 1 || rep.Deleted[0] != "added/after.txt" {
		t.Fatalf("deleted = %v, want [added/after.txt]", rep.Deleted)
	}
	if len(rep.Restored) != 1 || rep.Restored[0] != "kept.txt" {
		t.Fatalf("restored = %v, want [kept.txt]", rep.Restored)
	}
}

func TestRestoreLeavesIgnoredFilesAndGitAlone(t *testing.T) {
	repo, store := initRepo(t)
	ctx := context.Background()
	target, err := store.Capture(ctx, repo, mustRef(t, "th_1", "ck_a", "baseline"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "build/out.bin", "ignored\n")
	writeFile(t, repo, "new.txt", "gone\n")
	headBefore := runGit(t, repo, "rev-parse", "HEAD")

	if _, err := store.Restore(ctx, repo, target.Tree); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, repo, "build/out.bin"); got != "ignored\n" {
		t.Fatalf("ignored file was touched: %q", got)
	}
	if runGit(t, repo, "rev-parse", "HEAD") != headBefore {
		t.Fatal("HEAD moved")
	}
}

func TestRestoreRecreatesDeletedFile(t *testing.T) {
	repo, store := initRepo(t)
	ctx := context.Background()
	target, err := store.Capture(ctx, repo, mustRef(t, "th_1", "ck_a", "baseline"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "kept.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(ctx, repo, target.Tree); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readFile(t, repo, "kept.txt"); got != "one\n" {
		t.Fatalf("kept.txt = %q", got)
	}
}

func TestRestoreRoundTripsThroughSafetySnapshot(t *testing.T) {
	repo, store := initRepo(t)
	ctx := context.Background()
	target, err := store.Capture(ctx, repo, mustRef(t, "th_1", "ck_a", "baseline"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "kept.txt", "work in progress\n")
	safety, err := SafetyRef("th_1", "rw_1")
	if err != nil {
		t.Fatal(err)
	}
	safe, err := store.Capture(ctx, repo, safety)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(ctx, repo, target.Tree); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(ctx, repo, safe.Tree); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, repo, "kept.txt"); got != "work in progress\n" {
		t.Fatalf("safety restore gave %q", got)
	}
}

func TestRestoreRefusesAbsoluteAndEscapingPaths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"/etc/passwd", "../escape.txt", "", "a/../../escape"} {
		if err := removeFile(root, name); err == nil {
			t.Fatalf("removeFile(%q) accepted a path outside the worktree", name)
		}
	}
}
