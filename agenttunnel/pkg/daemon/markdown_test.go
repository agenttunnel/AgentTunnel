package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// TestMarkdownReadIsThreadScoped pins the wire contract of markdown.read: a
// document resolves against the thread's project, and a client-supplied
// workdir is ignored, so the viewer cannot be pointed at another directory.
func TestMarkdownReadIsThreadScoped(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "docs", "plan.md"), []byte("# Plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "other.md"), []byte("# Other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var proj struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	json.Unmarshal(call(t, c, "1", "project.create", map[string]any{"name": "P", "path": project}), &proj)
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "2", "thread.create", map[string]any{"title": "T", "projectId": proj.Project.ID}), &created)

	var doc struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	json.Unmarshal(call(t, c, "3", "markdown.read", map[string]any{
		"threadId": created.ThreadID, "path": "docs/plan.md",
	}), &doc)
	if doc.Path != "docs/plan.md" || doc.Text != "# Plan\n" {
		t.Fatalf("doc = %+v, want docs/plan.md from the thread project", doc)
	}

	// A workdir param is not part of the contract: the read stays in the project.
	if _, errResp := rawCall(t, c, "4", "markdown.read", map[string]any{
		"threadId": created.ThreadID, "workdir": elsewhere, "path": "other.md",
	}); errResp == nil {
		t.Fatal("markdown.read must not honour a client workdir")
	}
	if _, errResp := rawCall(t, c, "5", "markdown.read", map[string]any{
		"threadId": created.ThreadID, "path": filepath.Join(elsewhere, "other.md"),
	}); errResp == nil {
		t.Fatal("an absolute path outside the thread project must be refused")
	}
}
