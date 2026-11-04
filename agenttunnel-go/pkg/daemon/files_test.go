package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func gitPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	return p
}

func initRepo(t *testing.T, git string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
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
	run("init", "-q")
	run("add", ".")
	// A repo with no commits yet exercises the empty-diff fallback.
	return dir
}

func TestFilesTreeAndReadRoundTrip(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.md"), []byte("# b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var proj struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	json.Unmarshal(call(t, c, "1", "project.create", map[string]any{
		"name": "P", "path": dir,
	}), &proj)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "2", "thread.create", map[string]any{
		"title": "T", "projectId": proj.Project.ID,
	}), &created)

	var tree struct {
		Root    string `json:"root"`
		Entries []struct {
			Name     string `json:"name"`
			Path     string `json:"path"`
			Dir      bool   `json:"dir"`
			Children []struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"children"`
		} `json:"entries"`
		Status map[string]string `json:"status"`
	}
	json.Unmarshal(call(t, c, "3", "files.tree", map[string]any{
		"threadId": created.ThreadID,
	}), &tree)
	if tree.Root != dir {
		t.Fatalf("root = %q, want %q", tree.Root, dir)
	}
	if len(tree.Entries) != 2 || !tree.Entries[0].Dir || tree.Entries[0].Name != "sub" {
		t.Fatalf("entries = %+v, want dir sub first", tree.Entries)
	}

	var read struct {
		Path      string `json:"path"`
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
		Binary    bool   `json:"binary"`
	}
	json.Unmarshal(call(t, c, "4", "files.read", map[string]any{
		"threadId": created.ThreadID, "path": "sub/b.md",
	}), &read)
	if read.Text != "# b\n" || read.Truncated || read.Binary {
		t.Fatalf("read = %+v", read)
	}

	// Escapes are rejected.
	if _, errResp := rawCall(t, c, "5", "files.read", map[string]any{
		"threadId": created.ThreadID, "path": "../x",
	}); errResp == nil {
		t.Fatal("path escape should fail")
	}
}

// TestReviewDiffUsesThreadProject verifies the diff resolves the thread's
// project directory rather than the global fallback workdir.
func TestReviewDiffUsesThreadProject(t *testing.T) {
	git := gitPath(t)
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	dir := initRepo(t, git)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(git, "-C", dir, "add", ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command(git, "-C", dir, "commit", "-qm", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	// A working-tree modification plus an untracked file.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\n// hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("todo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var proj struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	json.Unmarshal(call(t, c, "1", "project.create", map[string]any{
		"name": "P", "path": dir,
	}), &proj)
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "2", "thread.create", map[string]any{
		"title": "T", "projectId": proj.Project.ID,
	}), &created)

	var diff struct {
		Files []struct {
			Path      string `json:"path"`
			Additions int    `json:"additions"`
		} `json:"files"`
		Empty bool `json:"empty"`
	}
	json.Unmarshal(call(t, c, "3", "review.diff", map[string]any{
		"threadId": created.ThreadID,
	}), &diff)
	if diff.Empty || len(diff.Files) != 1 || diff.Files[0].Path != "main.go" {
		t.Fatalf("diff = %+v, want the modified main.go", diff)
	}
	if diff.Files[0].Additions != 2 {
		t.Fatalf("additions = %d, want 2", diff.Files[0].Additions)
	}

	// The files.tree status overlay marks the modification and the untracked file.
	var tree struct {
		Status map[string]string `json:"status"`
	}
	json.Unmarshal(call(t, c, "4", "files.tree", map[string]any{
		"threadId": created.ThreadID,
	}), &tree)
	if tree.Status["main.go"] != "modified" || tree.Status["notes.txt"] != "untracked" {
		t.Fatalf("status = %+v, want main.go modified + notes.txt untracked", tree.Status)
	}
}
