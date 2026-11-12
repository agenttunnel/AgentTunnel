package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesParentsAndStaysInsideWorkdir(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, "notes/deep/a.txt", []byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "notes", "deep", "a.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("on disk = %q, %v; want hello", got, err)
	}

	// resolve clamps ".." at the workdir root, so the write lands inside it.
	if err := Write(root, "../escape.txt", []byte("x")); err != nil {
		t.Fatalf("clamped write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); err != nil {
		t.Fatalf("clamped write did not land in the workdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Fatal("write created a file outside the workdir")
	}
}

// A symlink inside the workdir that points outside it must not carry a write
// or a read out of the workdir, even though its lexical path is inside.
func TestSymlinksOutsideTheWorkdirAreRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "filelink.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing.txt"), filepath.Join(root, "dangling.txt")); err != nil {
		t.Fatal(err)
	}

	if err := Write(root, "dirlink/new.txt", []byte("pwned")); err == nil {
		t.Fatal("write through a directory symlink was allowed")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Fatal("a write through a directory symlink reached the outside directory")
	}
	if err := Write(root, "filelink.txt", []byte("pwned")); err == nil {
		t.Fatal("write through a file symlink was allowed")
	}
	if err := Write(root, "dangling.txt", []byte("pwned")); err == nil {
		t.Fatal("write through a dangling symlink was allowed")
	}
	if got, _ := os.ReadFile(secret); string(got) != "outside" {
		t.Fatalf("outside file = %q, want it untouched", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "missing.txt")); err == nil {
		t.Fatal("a dangling symlink write created the outside target")
	}

	if _, err := Read(root, "dirlink/secret.txt"); err == nil {
		t.Fatal("read through a directory symlink was allowed")
	}
	if _, err := Read(root, "filelink.txt"); err == nil {
		t.Fatal("read through a file symlink was allowed")
	}
}

// A symlink that stays inside the workdir is an ordinary path.
func TestSymlinksInsideTheWorkdirAreAllowed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := Write(root, "alias/a.txt", []byte("hi")); err != nil {
		t.Fatalf("write through an inside symlink: %v", err)
	}
	res, err := Read(root, "real/a.txt")
	if err != nil || res.Text != "hi" {
		t.Fatalf("read = %+v, %v; want hi", res, err)
	}
}
