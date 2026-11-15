package files

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTreeDirsFirstSorted(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "b.ts", "b")
	writeFile(t, dir, "a.go", "a")
	writeFile(t, dir, "sub/z.md", "z")
	writeFile(t, dir, "sub/a.md", "a")

	entries, err := Tree(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("top-level = %+v, want 3", entries)
	}
	// Dirs first, then alpha.
	if !entries[0].Dir || entries[0].Name != "sub" {
		t.Fatalf("first = %+v, want dir sub", entries[0])
	}
	if entries[1].Name != "a.go" || entries[2].Name != "b.ts" {
		t.Fatalf("files = %v %v, want a.go b.ts", entries[1].Name, entries[2].Name)
	}
	kids := entries[0].Children
	if len(kids) != 2 || kids[0].Name != "a.md" || kids[1].Name != "z.md" {
		t.Fatalf("children = %+v", kids)
	}
}

func TestTreeSkipsNoise(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keep.go", "x")
	writeFile(t, dir, "node_modules/dep/index.js", "x")
	writeFile(t, dir, ".git/HEAD", "ref: x")

	entries, err := Tree(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "keep.go" {
		t.Fatalf("entries = %+v, want only keep.go", entries)
	}
}

func TestReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/a.txt", "hello\nworld\n")

	res, err := Read(dir, "sub/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hello\nworld\n" || res.Size != 12 || res.Truncated || res.Binary {
		t.Fatalf("read = %+v", res)
	}
}

func TestReadRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"../x", "/etc/hosts", "..", ".", "sub/../../x"} {
		if _, err := Read(dir, p); err == nil {
			t.Fatalf("path %q should be rejected", p)
		}
	}
}

func TestReadMissingAndBinary(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(dir, "nope.txt"); err == nil {
		t.Fatal("missing file should error")
	}
	writeFile(t, dir, "img.bin", "ab\x00cd")
	res, err := Read(dir, "img.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Binary || res.Text != "" {
		t.Fatalf("binary = %+v", res)
	}
}
