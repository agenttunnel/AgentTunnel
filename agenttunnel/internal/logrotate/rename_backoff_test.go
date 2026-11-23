package logrotate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A rename that keeps failing (read-only log dir) must not be retried on every
// write past the cap: that closes, renames and reopens the file per line. The
// writer backs off until the file has grown by another cap's worth of bytes.
func TestFailedRenameIsNotRetriedOnEveryWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atd.log")
	const capBytes = 100
	w, err := New(path, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	attempts := 0
	w.rename = func(oldpath, newpath string) error {
		attempts++
		return errors.New("permission denied")
	}

	const writes, chunk = 50, 10 // 500 bytes: five caps' worth
	line := strings.Repeat("z", chunk-1) + "\n"
	for i := 0; i < writes; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	// Without backoff this is ~40 attempts (every write once past the cap). With
	// it, one attempt per cap's worth of growth, plus the first crossing.
	if max := writes*chunk/capBytes + 1; attempts == 0 || attempts > max {
		t.Fatalf("rename attempted %d times for %d bytes at a %d-byte cap, want 1..%d",
			attempts, writes*chunk, capBytes, max)
	}
	// Losing lines is worse than an unbounded file: every byte is still there.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != strings.Repeat(line, writes) {
		t.Fatalf("active file lost or reordered bytes: %d bytes, want %d", len(got), writes*chunk)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("backup exists although every rename failed: %v", err)
	}
}

// Once renames succeed again, rotation resumes at the next eligible size and the
// active file is bounded again.
func TestRotationResumesAfterRenameRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atd.log")
	const capBytes = 100
	w, err := New(path, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	failures := 1
	w.rename = func(oldpath, newpath string) error {
		if failures > 0 {
			failures--
			return errors.New("transient")
		}
		return os.Rename(oldpath, newpath)
	}

	line := strings.Repeat("q", 9) + "\n" // 10 bytes
	for i := 0; i < 40; i++ {             // 400 bytes
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no backup after rename recovered: %v", err)
	}
	if got := fileSize(t, path); got > capBytes+10 {
		t.Fatalf("active file is %d bytes after recovery, want about <= %d", got, capBytes)
	}
}
