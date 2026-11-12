package fileedit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixture returns a root with one text file, plus a sibling directory outside
// the root that tests use as an escape target.
func fixture(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "work")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(root, "src")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "src", "main.go"), "package main\n")
	writeFile(t, filepath.Join(outside, "secret.txt"), "classified\n")
	return root, outside
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
}

func TestResolveRejectsTraversalAndAbsolutePaths(t *testing.T) {
	root, _ := fixture(t)
	for _, rel := range []string{
		"../outside/secret.txt",
		"src/../../outside/secret.txt",
		"src/../../../etc/passwd",
		"/etc/passwd",
		`\windows\system.ini`,
	} {
		if _, err := Read(root, rel); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("Read(%q) err = %v, want ErrOutsideRoot", rel, err)
		}
		if _, err := Write(root, rel, "x", "h"); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("Write(%q) err = %v, want ErrOutsideRoot", rel, err)
		}
		if _, err := List(root, rel); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("List(%q) err = %v, want ErrOutsideRoot", rel, err)
		}
	}
}

func TestSymlinkEscapeIsRejected(t *testing.T) {
	skipWithoutSymlinks(t)
	root, outside := fixture(t)
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}

	if _, err := Read(root, "leak.txt"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("Read through escaping symlink err = %v, want ErrOutsideRoot", err)
	}
	if _, err := Read(root, "linkdir/secret.txt"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("Read through escaping dir symlink err = %v, want ErrOutsideRoot", err)
	}
	if _, err := Write(root, "leak.txt", "pwned\n", hashOf([]byte("classified\n"))); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("Write through escaping symlink err = %v, want ErrOutsideRoot", err)
	}
	got, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "classified\n" {
		t.Fatalf("outside file was modified: %q", got)
	}

	listing, err := List(root, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range listing.Entries {
		if e.Name == "leak.txt" || e.Name == "linkdir" {
			t.Errorf("List exposed escaping symlink %q", e.Name)
		}
	}
}

func TestSymlinkInsideRootIsFollowed(t *testing.T) {
	skipWithoutSymlinks(t)
	root, _ := fixture(t)
	if err := os.Symlink(filepath.Join(root, "src", "main.go"), filepath.Join(root, "alias.go")); err != nil {
		t.Fatal(err)
	}
	doc, err := Read(root, "alias.go")
	if err != nil {
		t.Fatalf("Read through in-root symlink: %v", err)
	}
	if doc.Text != "package main\n" {
		t.Fatalf("Read text = %q", doc.Text)
	}
}

func TestReadRejectsOversizedFile(t *testing.T) {
	root, _ := fixture(t)
	writeFile(t, filepath.Join(root, "big.txt"), strings.Repeat("a", MaxFileBytes+1))
	if _, err := Read(root, "big.txt"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Read oversized err = %v, want ErrTooLarge", err)
	}
}

func TestWriteRejectsOversizedText(t *testing.T) {
	root, _ := fixture(t)
	doc, err := Read(root, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, "src/main.go", strings.Repeat("a", MaxFileBytes+1), doc.Hash); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Write oversized err = %v, want ErrTooLarge", err)
	}
}

func TestReadRejectsBinary(t *testing.T) {
	root, _ := fixture(t)
	writeFile(t, filepath.Join(root, "blob.bin"), "head\x00tail")
	if _, err := Read(root, "blob.bin"); !errors.Is(err, ErrBinary) {
		t.Fatalf("Read NUL file err = %v, want ErrBinary", err)
	}
	if err := os.WriteFile(filepath.Join(root, "latin1.txt"), []byte{0xff, 0xfe, 'a'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "latin1.txt"); !errors.Is(err, ErrBinary) {
		t.Fatalf("Read invalid UTF-8 err = %v, want ErrBinary", err)
	}
}

func TestWriteRejectsNulText(t *testing.T) {
	root, _ := fixture(t)
	doc, err := Read(root, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, "src/main.go", "a\x00b", doc.Hash); !errors.Is(err, ErrBinary) {
		t.Fatalf("Write NUL err = %v, want ErrBinary", err)
	}
}

func TestWriteRoundTripIsAtomicAndDetectsConflicts(t *testing.T) {
	root, _ := fixture(t)
	first, err := Read(root, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != "src/main.go" || first.Size != int64(len("package main\n")) {
		t.Fatalf("Read metadata = %+v", first)
	}

	saved, err := Write(root, "src/main.go", "package main\n\nfunc main() {}\n", first.Hash)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	again, err := Read(root, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if again.Text != "package main\n\nfunc main() {}\n" || again.Hash != saved.Hash {
		t.Fatalf("round-trip mismatch: text=%q hash=%s want hash=%s", again.Text, again.Hash, saved.Hash)
	}

	// The first hash is now stale: a second save must not clobber the first.
	if _, err := Write(root, "src/main.go", "stale overwrite\n", first.Hash); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Write err = %v, want ErrConflict", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp files left behind: %v", names)
	}
}

func TestWriteRefusesMissingFile(t *testing.T) {
	root, _ := fixture(t)
	if _, err := Write(root, "src/new.go", "x", hashOf(nil)); err == nil {
		t.Fatal("Write to a missing file succeeded; the editor must not create files")
	}
}

func TestListIsOneLevelDirsFirstSkipsGit(t *testing.T) {
	root, _ := fixture(t)
	if err := os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "README.md"), "hi\n")
	writeFile(t, filepath.Join(root, "Zeta.txt"), "z")

	top, err := List(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range top.Entries {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, ","); got != "src,README.md,Zeta.txt" {
		t.Fatalf("top-level order = %s", got)
	}
	if !top.Entries[0].Dir || top.Entries[1].Dir {
		t.Fatalf("dir flags wrong: %+v", top.Entries)
	}
	if top.Entries[0].Path != "src" {
		t.Fatalf("dir path = %q", top.Entries[0].Path)
	}

	sub, err := List(root, "src")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Entries) != 1 || sub.Entries[0].Path != "src/main.go" || sub.Entries[0].Size == 0 {
		t.Fatalf("lazy child listing = %+v", sub.Entries)
	}
}

func TestListRejectsFileAsDirectory(t *testing.T) {
	root, _ := fixture(t)
	if _, err := List(root, "src/main.go"); !errors.Is(err, ErrNotDir) {
		t.Fatalf("List(file) err = %v, want ErrNotDir", err)
	}
}

func TestOpenRejectsEmptyRoot(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\") succeeded; an empty root must never fall back to the process cwd")
	}
}

func TestCRLFRoundTripIsByteIdentical(t *testing.T) {
	root, _ := fixture(t)
	const original = "first\r\nsecond\r\n\r\nlast\r\n"
	writeFile(t, filepath.Join(root, "win.txt"), original)

	doc, err := Read(root, "win.txt")
	if err != nil {
		t.Fatalf("Read CRLF: %v", err)
	}
	if doc.Text != "first\nsecond\n\nlast\n" {
		t.Fatalf("Read text = %q, want LF-folded buffer", doc.Text)
	}

	// Saving the unchanged buffer must reproduce the file byte for byte.
	if _, err := Write(root, "win.txt", doc.Text, doc.Hash); err != nil {
		t.Fatalf("Write unchanged: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "win.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("unchanged save rewrote the file: %q", got)
	}

	// A one-line edit keeps every other break CRLF and adds no bare LF.
	doc, err = Read(root, "win.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, "win.txt", strings.Replace(doc.Text, "second", "SECOND", 1), doc.Hash); err != nil {
		t.Fatalf("Write edit: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(root, "win.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "first\r\nSECOND\r\n\r\nlast\r\n"; string(got) != want {
		t.Fatalf("edited CRLF file = %q, want %q", got, want)
	}
}

func TestLFRoundTripStaysLF(t *testing.T) {
	root, _ := fixture(t)
	doc, err := Read(root, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, "src/main.go", "package main\n\n// edited\n", doc.Hash); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "src", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package main\n\n// edited\n" {
		t.Fatalf("LF file = %q", got)
	}
}

func TestMixedLineEndingsAreRefused(t *testing.T) {
	root, _ := fixture(t)
	for name, content := range map[string]string{
		"mixed.txt":  "a\r\nb\n",
		"barecr.txt": "a\rb\n",
		"oldmac.txt": "a\rb\rc",
	} {
		writeFile(t, filepath.Join(root, name), content)
		if _, err := Read(root, name); !errors.Is(err, ErrMixedLineEndings) {
			t.Errorf("Read(%s) err = %v, want ErrMixedLineEndings", name, err)
		}
		if _, err := Write(root, name, "a\nb\n", hashOf([]byte(content))); !errors.Is(err, ErrMixedLineEndings) {
			t.Errorf("Write(%s) err = %v, want ErrMixedLineEndings", name, err)
		}
	}
}

func TestWriteRejectsBareCarriageReturnInText(t *testing.T) {
	root, _ := fixture(t)
	doc, err := Read(root, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, "src/main.go", "a\rb", doc.Hash); !errors.Is(err, ErrMixedLineEndings) {
		t.Fatalf("Write bare CR err = %v, want ErrMixedLineEndings", err)
	}
}

func TestGitDirIsNotReachable(t *testing.T) {
	root, _ := fixture(t)
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	const hookBody = "#!/bin/sh\nexit 0\n"
	writeFile(t, hook, hookBody)
	writeFile(t, filepath.Join(root, ".git", "config"), "[core]\n")

	for _, rel := range []string{".git/hooks/pre-commit", "./.git/hooks/pre-commit", ".GIT/config", ".git"} {
		if _, err := Read(root, rel); !errors.Is(err, ErrGitDir) {
			t.Errorf("Read(%q) err = %v, want ErrGitDir", rel, err)
		}
		if _, err := Write(root, rel, "evil\n", hashOf([]byte(hookBody))); !errors.Is(err, ErrGitDir) {
			t.Errorf("Write(%q) err = %v, want ErrGitDir", rel, err)
		}
	}
	for _, rel := range []string{".git", ".git/hooks"} {
		if _, err := List(root, rel); !errors.Is(err, ErrGitDir) {
			t.Errorf("List(%q) err = %v, want ErrGitDir", rel, err)
		}
	}
	got, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != hookBody {
		t.Fatalf("hook was modified: %q", got)
	}
}

func TestSymlinkIntoGitDirIsNotReachable(t *testing.T) {
	skipWithoutSymlinks(t)
	root, _ := fixture(t)
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, hook, "#!/bin/sh\n")
	if err := os.Symlink(hook, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "alias"); !errors.Is(err, ErrGitDir) {
		t.Fatalf("Read through symlink into .git err = %v, want ErrGitDir", err)
	}
	if _, err := Write(root, "alias", "evil\n", hashOf([]byte("#!/bin/sh\n"))); !errors.Is(err, ErrGitDir) {
		t.Fatalf("Write through symlink into .git err = %v, want ErrGitDir", err)
	}
}

func TestListTruncatesAtMaxListEntries(t *testing.T) {
	root, _ := fixture(t)
	big := filepath.Join(root, "big")
	if err := os.MkdirAll(big, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxListEntries+1; i++ {
		writeFile(t, filepath.Join(big, fmt.Sprintf("f%04d.txt", i)), "x")
	}

	listing, err := List(root, "big")
	if err != nil {
		t.Fatal(err)
	}
	if !listing.Truncated {
		t.Fatal("listing over MaxListEntries not marked truncated")
	}
	if len(listing.Entries) != MaxListEntries {
		t.Fatalf("listed %d entries, want exactly %d", len(listing.Entries), MaxListEntries)
	}

	small, err := List(root, "src")
	if err != nil {
		t.Fatal(err)
	}
	if small.Truncated {
		t.Fatal("small listing marked truncated")
	}
}
