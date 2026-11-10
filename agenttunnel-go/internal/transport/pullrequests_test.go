package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// pr.list reports the thread's tracked requests in a stable shape, and keeps
// them when the forge cannot be read: an unreadable forge never drops a PR.
func TestPRListKeepsTrackedRequestsWhenForgeIsUnreadable(t *testing.T) {
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	disp := NewDispatcher(DispatcherDeps{
		Engine:   eng,
		Logs:     logstore.New(t.TempDir()),
		Settings: settings.New(t.TempDir()),
	})

	// A plain directory is not a git checkout, so the forge read fails
	// deterministically without touching any real repository.
	dir := t.TempDir()
	mustDispatch(t, eng, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "t"})
	mustDispatch(t, eng, contracts.Command{ID: "c2", Type: contracts.CmdWorktreeAttach, ThreadID: "t1",
		Worktree: &contracts.WorktreeRef{Path: dir}})
	tracked := contracts.PullRequestRef{Number: 7, URL: "https://github.com/o/r/pull/7"}
	mustDispatch(t, eng, contracts.Command{ID: "c3", Type: contracts.CmdPRTrack, ThreadID: "t1", PullRequest: &tracked})

	params, _ := json.Marshal(map[string]any{"threadId": "t1"})
	raw, err := disp.Handle(ctx, Request{Method: "pr.list", Params: params})
	if err != nil {
		t.Fatalf("pr.list: %v", err)
	}
	body, _ := json.Marshal(raw)
	var got struct {
		Status   string `json:"status"`
		Requests []struct {
			Number int    `json:"number"`
			URL    string `json:"url"`
			Error  string `json:"error"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "none" {
		t.Fatalf("status = %q, want none (not a git checkout)", got.Status)
	}
	if len(got.Requests) != 1 || got.Requests[0].Number != 7 || got.Requests[0].URL != tracked.URL || got.Requests[0].Error == "" {
		t.Fatalf("requests = %s, want the tracked PR with an error reason", body)
	}
}

func mustDispatch(t *testing.T, eng *domain.Engine, cmd contracts.Command) {
	t.Helper()
	if err := eng.Dispatch(context.Background(), cmd); err != nil {
		t.Fatalf("%s: %v", cmd.Type, err)
	}
}
