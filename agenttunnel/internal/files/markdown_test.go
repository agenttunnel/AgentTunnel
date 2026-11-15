package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadMarkdownRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/plan.md", "# Plan\n")
	writeFile(t, root, "NOTES.MARKDOWN", "notes")

	for _, rel := range []string{"docs/plan.md", "./docs/../docs/plan.md", filepath.Join(root, "docs", "plan.md"), "NOTES.MARKDOWN"} {
		doc, err := ReadMarkdown(root, rel)
		if err != nil {
			t.Fatalf("ReadMarkdown(%q): %v", rel, err)
		}
		if doc.Text == "" || strings.Contains(doc.Path, "..") || filepath.IsAbs(doc.Path) {
			t.Fatalf("ReadMarkdown(%q) = %+v, want root-relative text", rel, doc)
		}
	}
	doc, _ := ReadMarkdown(root, "docs/plan.md")
	if doc.Path != "docs/plan.md" || doc.Text != "# Plan\n" || doc.Size != 7 {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestReadMarkdownRefusals(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, root, "code.go", "package x")
	writeFile(t, root, "docs/ok.md", "ok")
	writeFile(t, outside, "secret.md", "secret")
	writeFile(t, outside, "secret.txt", "secret")
	writeFile(t, root, "big.md", strings.Repeat("a", MaxMarkdownBytes+1))
	if err := os.Mkdir(filepath.Join(root, "dir.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"non-markdown":              "code.go",
		"no extension":              "docs/ok",
		"parent traversal":          "../" + filepath.Base(outside) + "/secret.md",
		"deep traversal":            "docs/../../secret.md",
		"absolute outside":          filepath.Join(outside, "secret.md"),
		"oversize":                  "big.md",
		"directory":                 "dir.md",
		"missing":                   "nope.md",
		"empty root path":           "",
		"no markdown extension dir": "docs",
	}
	for name, rel := range cases {
		if _, err := ReadMarkdown(root, rel); err == nil {
			t.Fatalf("%s: ReadMarkdown(%q) succeeded, want refusal", name, rel)
		}
	}
	if _, err := ReadMarkdown("", "docs/ok.md"); err == nil {
		t.Fatal("empty root must be refused, not resolved to the process cwd")
	}
}

func TestReadMarkdownSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "secret.md", "secret")
	writeFile(t, root, "docs/ok.md", "ok")
	writeFile(t, root, "plain.txt", "x")

	links := map[string]string{
		"escape.md":  filepath.Join(outside, "secret.md"),  // .md name, target outside root
		"to-text.md": filepath.Join(root, "plain.txt"),     // in-root, but target is not markdown
		"alias.md":   filepath.Join(root, "docs", "ok.md"), // in-root markdown target: allowed
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	for _, name := range []string{"escape.md", "to-text.md"} {
		if _, err := ReadMarkdown(root, name); err == nil {
			t.Fatalf("symlink %s read succeeded, want refusal", name)
		}
	}
	if doc, err := ReadMarkdown(root, "alias.md"); err != nil || doc.Text != "ok" {
		t.Fatalf("in-root alias = %+v, %v; want ok", doc, err)
	}
}
