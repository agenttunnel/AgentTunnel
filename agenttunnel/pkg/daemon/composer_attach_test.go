package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// newPromptRecordingDaemon runs every agent as a script that writes the prompt
// it was given to promptFile, so a test can read exactly what the agent saw.
// thread.turn.start replies only once the turn finishes, so the file is
// complete by the time the call returns.
func newPromptRecordingDaemon(t *testing.T, promptFile string) *Daemon {
	t.Helper()
	script := `printf '%s' "$1" > '` + promptFile + `'
printf '{"type":"text","text":"ok"}\n'`
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{Binary: "sh", Args: []string{"-c", script, "sh", spec.Prompt}, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	d, err := New(context.Background(), Options{
		StateDir: t.TempDir(),
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// createProjectThread registers dir as a project and opens a thread in it.
func createProjectThread(t *testing.T, c *websocket.Conn, dir string) contracts.ThreadID {
	t.Helper()
	var proj struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	json.Unmarshal(call(t, c, "p", "project.create", map[string]any{"name": "P", "path": dir}), &proj)
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "t", "thread.create", map[string]any{
		"title": "T", "projectId": proj.Project.ID,
	}), &created)
	return created.ThreadID
}

func TestFilesSearchRPCMatchesWorkdirNames(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	dir := t.TempDir()
	writeTestFile(t, dir, "internal/composer/picker.go", "package composer\n")
	writeTestFile(t, dir, "docs/readme.md", "# docs\n")
	threadID := createProjectThread(t, c, dir)

	var res struct {
		Matches []struct {
			Path string `json:"path"`
			Dir  bool   `json:"dir"`
		} `json:"matches"`
	}
	json.Unmarshal(call(t, c, "s", "files.search", map[string]any{
		"threadId": threadID, "query": "picker", "limit": 10,
	}), &res)
	if len(res.Matches) == 0 || res.Matches[0].Path != "internal/composer/picker.go" || res.Matches[0].Dir {
		t.Fatalf("matches = %+v, want internal/composer/picker.go first", res.Matches)
	}
	for _, m := range res.Matches {
		if strings.HasPrefix(m.Path, "docs") {
			t.Fatalf("unrelated path matched: %+v", res.Matches)
		}
	}
}

func TestTurnAttachmentsReachAgentPrompt(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	d := newPromptRecordingDaemon(t, promptFile)
	c, _ := dialWS(t, d)

	dir := t.TempDir()
	writeTestFile(t, dir, "notes.md", "remember the migration\n")
	writeTestFile(t, dir, "src/main.go", "package main\n")
	threadID := createProjectThread(t, c, dir)

	call(t, c, "turn", "thread.turn.start", map[string]any{
		"threadId":    threadID,
		"text":        "summarise the attached notes",
		"attachments": []string{"notes.md", "src"},
	})

	raw, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("agent prompt was not recorded: %v", err)
	}
	prompt := string(raw)
	for _, want := range []string{
		"summarise the attached notes",
		"Attached context from the composer",
		"### notes.md",
		"remember the migration",
		"- src/ (folder)",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestTurnRejectsMissingAttachmentBeforeRunning(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	d := newPromptRecordingDaemon(t, promptFile)
	c, _ := dialWS(t, d)

	dir := t.TempDir()
	threadID := createProjectThread(t, c, dir)

	if _, err := tryCall(t, c, "turn", "thread.turn.start", map[string]any{
		"threadId":    threadID,
		"text":        "use the deleted file",
		"attachments": []string{"gone.go"},
	}); err == nil {
		t.Fatal("turn with a missing attachment was accepted")
	}
	if _, err := os.Stat(promptFile); err == nil {
		t.Fatal("agent ran despite the rejected attachment")
	}
}

func writeTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
