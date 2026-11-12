package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// newFsFixture wires a dispatcher whose daemon default workdir is a project
// checkout, and a thread whose worktree is a different directory. A plain
// directory is enough: the worktree ref is only a path, so no git is needed.
func newFsFixture(t *testing.T) (*Dispatcher, string, string) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	set := settings.New(t.TempDir())
	project := t.TempDir()
	set.SetWorkdir(project)
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Logs: logstore.New(t.TempDir()), Settings: set})

	worktreeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "project.txt"), []byte("project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, "worktree.txt"), []byte("worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustDispatch(t, eng, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "t"})
	mustDispatch(t, eng, contracts.Command{ID: "c2", Type: contracts.CmdWorktreeAttach, ThreadID: "t1",
		Worktree: &contracts.WorktreeRef{Path: worktreeDir}})
	return disp, project, worktreeDir
}

// fsCall runs one fs.* method through the dispatcher the way the WS handler does.
func fsCall(t *testing.T, disp *Dispatcher, method string, params map[string]any) (json.RawMessage, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, err := disp.Handle(context.Background(), Request{Method: method, Params: raw})
	if err != nil {
		return nil, err
	}
	return json.Marshal(res)
}

func TestFsRPCRootIsTheThreadWorktree(t *testing.T) {
	disp, project, worktreeDir := newFsFixture(t)

	listed, err := fsCall(t, disp, "fs.list", map[string]any{"threadId": "t1", "path": ""})
	if err != nil {
		t.Fatalf("fs.list: %v", err)
	}
	if !strings.Contains(string(listed), "worktree.txt") || strings.Contains(string(listed), "project.txt") {
		t.Fatalf("fs.list did not list the worktree root: %s", listed)
	}

	readRaw, err := fsCall(t, disp, "fs.read", map[string]any{"threadId": "t1", "path": "worktree.txt"})
	if err != nil {
		t.Fatalf("fs.read: %v", err)
	}
	var doc struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(readRaw, &doc); err != nil {
		t.Fatal(err)
	}

	if _, err := fsCall(t, disp, "fs.write", map[string]any{
		"threadId": "t1", "path": "worktree.txt", "text": "edited\n", "hash": doc.Hash,
	}); err != nil {
		t.Fatalf("fs.write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(worktreeDir, "worktree.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "edited\n" {
		t.Fatalf("worktree file = %q, want the write", got)
	}
	untouched, err := os.ReadFile(filepath.Join(project, "project.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(untouched) != "project\n" {
		t.Fatalf("project checkout changed: %q", untouched)
	}
}

func TestFsRPCRejectsUnknownThread(t *testing.T) {
	disp, project, _ := newFsFixture(t)
	// The daemon default is the project checkout; an unknown thread must not
	// quietly operate there.
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"fs.list", map[string]any{"threadId": "nope", "path": ""}},
		{"fs.read", map[string]any{"threadId": "nope", "path": "project.txt"}},
		{"fs.write", map[string]any{"threadId": "nope", "path": "project.txt", "text": "x", "hash": "h"}},
	} {
		if _, err := fsCall(t, disp, tc.method, tc.params); err == nil {
			t.Errorf("%s on unknown thread succeeded; want an error", tc.method)
		}
	}
	got, err := os.ReadFile(filepath.Join(project, "project.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "project\n" {
		t.Fatalf("unknown-thread fs.write reached the project checkout: %q", got)
	}
}
