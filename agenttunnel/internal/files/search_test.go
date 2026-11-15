package files

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func paths(ms []Match) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Path
	}
	return out
}

func TestSearchRanksBaseNameMatchesFirst(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/auth/session.go", "x")
	writeFile(t, dir, "session/ui/util.go", "x")
	writeFile(t, dir, "docs/unrelated.md", "x")

	got, err := Search(t.Context(), dir, "sess", 10)
	if err != nil {
		t.Fatal(err)
	}
	// A base-name match beats a query that only lands in parent folders.
	rank := map[string]int{}
	for i, m := range got {
		rank[m.Path] = i
	}
	hit, pathOnly := rank["pkg/auth/session.go"], rank["session/ui/util.go"]
	if _, ok := rank["session/ui/util.go"]; !ok {
		t.Fatalf("results = %v, want session/ui/util.go as a path match", paths(got))
	}
	if hit >= pathOnly {
		t.Fatalf("results = %v, want pkg/auth/session.go ranked above session/ui/util.go", paths(got))
	}
	for _, m := range got {
		if strings.Contains(m.Path, "unrelated") {
			t.Fatalf("non-matching path returned: %v", paths(got))
		}
	}
}

func TestSearchReturnsFoldersAndFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "internal/files/search.go", "x")

	got, err := Search(t.Context(), dir, "files", 10)
	if err != nil {
		t.Fatal(err)
	}
	var sawDir, sawFile bool
	for _, m := range got {
		if m.Path == "internal/files" && m.Dir {
			sawDir = true
		}
		if m.Path == "internal/files/search.go" && !m.Dir {
			sawFile = true
		}
	}
	if !sawDir || !sawFile {
		t.Fatalf("results = %+v, want the folder and the file", got)
	}
}

func TestSearchHonoursGitignoreInRepo(t *testing.T) {
	git := gitBin(t)
	dir := t.TempDir()
	writeFile(t, dir, ".gitignore", "secret/\n")
	writeFile(t, dir, "secret/token.txt", "x")
	writeFile(t, dir, "src/token.go", "x")
	runGit(t, git, dir, "init", "-q")
	runGit(t, git, dir, "add", "src", ".gitignore")

	got, err := Search(t.Context(), dir, "token", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "src/token.go" {
		t.Fatalf("results = %v, want only src/token.go (secret/ is ignored)", paths(got))
	}
}

func TestSearchSkipsHeavyDirsOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node_modules/left-pad/index.js", "x")
	writeFile(t, dir, "index.js", "x")

	got, err := Search(t.Context(), dir, "index", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "index.js" {
		t.Fatalf("results = %v, want only index.js", paths(got))
	}
}

func TestSearchCapsResults(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 60; i++ {
		writeFile(t, dir, "f"+string(rune('a'+i%26))+strings.Repeat("x", i/26)+".txt", "x")
	}
	got, err := Search(t.Context(), dir, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxSearchResults {
		t.Fatalf("len = %d, want cap %d", len(got), MaxSearchResults)
	}
	got, err = Search(t.Context(), dir, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("len = %d, want the requested 5", len(got))
	}
}

func TestSearchEmptyQueryListsShallowFirst(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "deep/er/leaf.go", "x")
	writeFile(t, dir, "top.go", "x")

	got, err := Search(t.Context(), dir, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Path != "deep" || !got[0].Dir {
		// Depth 0 entries rank first: "deep" (dir) and "top.go" both have no slash.
		t.Fatalf("results = %v, want top-level entries first", paths(got))
	}
}

func TestAttachmentBlockInlinesTextAndListsFolders(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\n")
	writeFile(t, dir, "sub/b.md", "# b\n")

	block, err := AttachmentBlock(dir, []string{"a.go", "sub"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Attached context", "### a.go", "package a", "- sub/ (folder)"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
}

func TestAttachmentBlockListsOversizedAndBinaryWithoutContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "big.txt", strings.Repeat("x", MaxAttachFileBytes+1))
	writeFile(t, dir, "img.bin", "\x00\x01binary")

	block, err := AttachmentBlock(dir, []string{"big.txt", "img.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(block, strings.Repeat("x", 100)) {
		t.Fatal("oversized file content was inlined")
	}
	if !strings.Contains(block, "big.txt") || !strings.Contains(block, "not inlined") {
		t.Fatalf("oversized file not listed:\n%s", block)
	}
	if !strings.Contains(block, "img.bin (binary, not inlined)") {
		t.Fatalf("binary file not listed:\n%s", block)
	}
}

func TestAttachmentBlockRejectsEscapeMissingAndTooMany(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "x")

	for _, rel := range []string{"../outside", "/etc/hosts", "missing.go"} {
		if _, err := AttachmentBlock(dir, []string{rel}); err == nil {
			t.Fatalf("attachment %q should be rejected", rel)
		}
	}
	many := make([]string, MaxAttachments+1)
	for i := range many {
		many[i] = "a.go"
	}
	if _, err := AttachmentBlock(dir, many); err == nil {
		t.Fatal("too many attachments should be rejected")
	}
}

func TestAttachmentFenceOutlastsContentBackticks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "doc.md", "before\n```go\ncode\n```\nafter\n")

	block, err := AttachmentBlock(dir, []string{"doc.md"})
	if err != nil {
		t.Fatal(err)
	}
	// The file's own triple fence must not close the attachment's fence, so the
	// wrapper is at least four backticks.
	if !strings.Contains(block, "````\nbefore") {
		t.Fatalf("fence did not outlast content backticks:\n%s", block)
	}
}

func gitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	return p
}

func runGit(t *testing.T, git, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
