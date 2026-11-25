package deliverables

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteNamesFileByTurnTimeAndMessage(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 25, 1, 15, 52, 0, time.UTC)

	path, err := New(dir).Write("thread-1", at, "cmd-7", "the answer\n\n")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(dir, "thread-1", "turn-20261125-011552-cmd-7.md")
	if path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "the answer\n" {
		t.Fatalf("contents = %q, want the answer with one trailing newline", got)
	}
}

func TestWriteNeverOverwritesAClashingName(t *testing.T) {
	store := New(t.TempDir())
	at := time.Date(2026, 11, 25, 1, 15, 52, 0, time.UTC)

	first, err := store.Write("t", at, "cmd-1", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Write("t", at, "cmd-1", "second")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("second write reused %s", first)
	}
	if got, _ := os.ReadFile(first); string(got) != "first\n" {
		t.Fatalf("first deliverable was overwritten: %q", got)
	}
}

func TestWriteKeepsIDsInsideTheStore(t *testing.T) {
	dir := t.TempDir()
	path, err := New(dir).Write("../../escape", time.Now(), "a/b", "x")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.Contains(rel, "..") {
		t.Fatalf("deliverable escaped the store: %s", path)
	}
}
