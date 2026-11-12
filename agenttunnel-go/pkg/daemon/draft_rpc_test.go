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
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// createDraft opens a draft in a fresh project directory. The directory is not
// a git repository, so the first turn needs no worktree.
func createDraft(t *testing.T, c *websocket.Conn, params map[string]any) contracts.ThreadID {
	t.Helper()
	var proj struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	json.Unmarshal(call(t, c, "p", "project.create", map[string]any{"name": "P", "path": t.TempDir()}), &proj)
	params["projectId"] = proj.Project.ID
	params["draft"] = true
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "t", "thread.create", params), &created)
	return created.ThreadID
}

// The first turn on a draft may omit its text: the title and description are
// the prompt, and the thread stops being a draft.
func TestDraftFirstTurnRunsItsDescription(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	d := newPromptRecordingDaemon(t, promptFile)
	c, _ := dialWS(t, d)

	id := createDraft(t, c, map[string]any{"title": "Login", "description": "the form resets on submit"})
	call(t, c, "2", "thread.turn.start", map[string]any{"threadId": id})

	raw, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("agent never ran: %v", err)
	}
	if !strings.Contains(string(raw), "Login\n\nthe form resets on submit") {
		t.Fatalf("prompt does not carry the draft's title and description:\n%s", raw)
	}

	var st struct {
		Draft    bool `json:"draft"`
		Messages []struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		} `json:"messages"`
	}
	json.Unmarshal(call(t, c, "3", "thread.get", map[string]any{"threadId": id}), &st)
	if st.Draft {
		t.Fatal("thread is still a draft after its first turn")
	}
	if len(st.Messages) == 0 || st.Messages[0].Author != "user" || st.Messages[0].Text != "Login\n\nthe form resets on submit" {
		t.Fatalf("transcript = %+v, want the draft prompt as the user's first message", st.Messages)
	}
}

// A draft with only the placeholder title and no description has nothing to
// run: the turn is refused, no agent starts, and the draft stays a draft.
func TestDraftWithNothingToRunIsRefused(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	d := newPromptRecordingDaemon(t, promptFile)
	c, _ := dialWS(t, d)

	id := createDraft(t, c, map[string]any{})
	if res := callRaw(t, c, "2", "thread.turn.start", map[string]any{"threadId": id}); res.Error == nil {
		t.Fatal("empty draft turn was accepted")
	}
	if _, err := os.Stat(promptFile); err == nil {
		t.Fatal("an agent ran for an empty draft")
	}

	var st struct {
		Draft bool `json:"draft"`
	}
	json.Unmarshal(call(t, c, "3", "thread.get", map[string]any{"threadId": id}), &st)
	if !st.Draft {
		t.Fatal("refused draft lost its draft flag")
	}
}

// Drafts are the user's unfinished work: they must survive a daemon restart
// with their description, the same as a started thread.
func TestDraftSurvivesDaemonRestart(t *testing.T) {
	stateDir := t.TempDir()
	open := func() *Daemon {
		t.Helper()
		driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(providers.SessionSpec) (providers.CommandSpec, error) {
			return providers.CommandSpec{Binary: "sh", Args: []string{"-c", e2eScript}, PromptVia: "stdin"}, nil
		}, providers.JSONLineTranslator{})
		d, err := New(context.Background(), Options{
			StateDir: stateDir,
			Token:    "secret",
			Registry: providers.NewRegistry(driver),
			Config:   orchestrator.Config{MaxPasses: 2, MaxConcurrency: 2},
		})
		if err != nil {
			t.Fatalf("daemon: %v", err)
		}
		return d
	}

	d1 := open()
	c1, _ := dialWS(t, d1)
	id := createDraft(t, c1, map[string]any{"title": "Later", "description": "write it down"})
	d1.Close()

	d2 := open()
	defer d2.Close()
	c2, _ := dialWS(t, d2)
	var st struct {
		Draft       bool   `json:"draft"`
		Description string `json:"description"`
	}
	json.Unmarshal(call(t, c2, "1", "thread.get", map[string]any{"threadId": id}), &st)
	if !st.Draft || st.Description != "write it down" {
		t.Fatalf("draft after restart = %+v", st)
	}
}
