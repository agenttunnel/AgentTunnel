package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// fakeProjects is a minimal project RPC server: it keeps the registry in
// memory and records every create so a test can assert deduplication.
type fakeProjects struct {
	mu       sync.Mutex
	projects []projectsRow
	nextID   int
	creates  []map[string]string
	deletes  []string
}

func (f *fakeProjects) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	for {
		_, data, err := ws.Read(r.Context())
		if err != nil {
			return
		}
		var req struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &req) != nil {
			continue
		}
		result, rpcErr := f.handle(req.Method, req.Params)
		var frame any
		if rpcErr != "" {
			frame = map[string]any{"id": req.ID, "error": map[string]any{"code": 1, "message": rpcErr}}
		} else {
			frame = map[string]any{"id": req.ID, "result": result}
		}
		out, err := json.Marshal(frame)
		if err != nil {
			return
		}
		if err := ws.Write(r.Context(), websocket.MessageText, out); err != nil {
			return
		}
	}
}

func (f *fakeProjects) handle(method string, params json.RawMessage) (any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "project.list":
		return f.listLocked(), ""
	case "project.create":
		var p struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err.Error()
		}
		f.nextID++
		row := projectsRow{ID: fmt.Sprintf("pr_%d", f.nextID), Name: p.Name, Path: p.Path}
		f.projects = append(f.projects, row)
		f.creates = append(f.creates, map[string]string{"name": p.Name, "path": p.Path})
		return map[string]any{"project": row, "projects": f.listLocked()}, ""
	case "project.delete":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err.Error()
		}
		f.deletes = append(f.deletes, p.ID)
		kept := make([]projectsRow, 0, len(f.projects))
		for _, row := range f.projects {
			if row.ID != p.ID {
				kept = append(kept, row)
			}
		}
		f.projects = kept
		return f.listLocked(), ""
	default:
		return nil, "unknown method " + method
	}
}

func (f *fakeProjects) listLocked() []projectsRow {
	out := make([]projectsRow, len(f.projects))
	copy(out, f.projects)
	return out
}

func newFakeProjectsEnv(t *testing.T, fake *fakeProjects) (*Env, func()) {
	t.Helper()
	srv := httptest.NewServer(fake)
	env := &Env{
		JSON:      false,
		DaemonURL: "ws" + strings.TrimPrefix(srv.URL, "http"),
		HomeDir:   t.TempDir(),
		Out:       &bytes.Buffer{},
		Err:       &bytes.Buffer{},
	}
	return env, srv.Close
}

func resolvedDir(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestProjectsAddDedupesByResolvedPath(t *testing.T) {
	dir := t.TempDir()
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	fake := &fakeProjects{}
	env, closeServer := newFakeProjectsEnv(t, fake)
	defer closeServer()

	if err := runProjectsAdd(context.Background(), env, []string{dir}); err != nil {
		t.Fatalf("add: %v", err)
	}
	// The same directory through a symlink must not create a second project.
	if err := runProjectsAdd(context.Background(), env, []string{"--name", "alias", link}); err != nil {
		t.Fatalf("add symlink: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.creates) != 1 {
		t.Fatalf("creates = %+v, want exactly one", fake.creates)
	}
	if len(fake.projects) != 1 {
		t.Fatalf("projects = %+v, want exactly one", fake.projects)
	}
	if want := resolvedDir(t, dir); fake.projects[0].Path != want {
		t.Fatalf("stored path = %q, want resolved %q", fake.projects[0].Path, want)
	}
	if fake.projects[0].Name != filepath.Base(resolvedDir(t, dir)) {
		t.Fatalf("name = %q, want the directory base name", fake.projects[0].Name)
	}
	if out := env.Out.(*bytes.Buffer).String(); !strings.Contains(out, "already registered") {
		t.Fatalf("output = %q, want an already-registered note", out)
	}
}

func TestProjectsRmByIdAndPath(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	fake := &fakeProjects{projects: []projectsRow{
		{ID: "pr_a", Name: "alpha", Path: resolvedDir(t, dirA), Threads: 2},
		{ID: "pr_b", Name: "beta", Path: resolvedDir(t, dirB)},
	}}
	env, closeServer := newFakeProjectsEnv(t, fake)
	defer closeServer()

	if err := runProjectsRm(context.Background(), env, []string{"pr_a"}); err != nil {
		t.Fatalf("rm by id: %v", err)
	}
	if len(fake.deletes) != 1 || fake.deletes[0] != "pr_a" {
		t.Fatalf("deletes = %v", fake.deletes)
	}

	// A path (relative to anything) resolves to the registered directory.
	if err := runProjectsRm(context.Background(), env, []string{dirB}); err != nil {
		t.Fatalf("rm by path: %v", err)
	}
	if len(fake.deletes) != 2 || fake.deletes[1] != "pr_b" {
		t.Fatalf("deletes = %v", fake.deletes)
	}
	if len(fake.projects) != 0 {
		t.Fatalf("projects = %+v, want empty", fake.projects)
	}
}

func TestProjectsRmRefusesAmbiguousPath(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeProjects{projects: []projectsRow{
		{ID: "pr_a", Name: "alpha", Path: resolvedDir(t, dir)},
		{ID: "pr_b", Name: "beta", Path: resolvedDir(t, dir)},
	}}
	env, closeServer := newFakeProjectsEnv(t, fake)
	defer closeServer()

	err := runProjectsRm(context.Background(), env, []string{dir})
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	for _, want := range []string{"more than one project", "pr_a", "pr_b"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	}
	if len(fake.deletes) != 0 {
		t.Fatalf("deleted despite ambiguity: %v", fake.deletes)
	}
}

func TestProjectsLsTableRendering(t *testing.T) {
	rows := []projectsRow{
		{ID: "pr_2", Name: "beta", Path: "/code/beta", Threads: 10},
		{ID: "pr_1", Name: "alpha", Path: "/code/alpha", Threads: 2},
	}
	var out bytes.Buffer
	if err := renderProjectsTable(&out, rows); err != nil {
		t.Fatalf("render: %v", err)
	}

	want := "ID     NAME    PATH          THREADS\n" +
		"pr_1   alpha   /code/alpha   2\n" +
		"pr_2   beta    /code/beta    10\n"
	if out.String() != want {
		t.Fatalf("table =\n%q\nwant\n%q", out.String(), want)
	}
}

func TestProjectsLsRendersDaemonList(t *testing.T) {
	fake := &fakeProjects{projects: []projectsRow{{ID: "pr_1", Name: "alpha", Path: "/code/alpha", Threads: 1}}}
	env, closeServer := newFakeProjectsEnv(t, fake)
	defer closeServer()

	if err := runProjects(context.Background(), env, nil); err != nil {
		t.Fatalf("bare projects: %v", err)
	}
	out := env.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "ID") || !strings.Contains(out, "alpha") || !strings.Contains(out, "/code/alpha") {
		t.Fatalf("output = %q", out)
	}
}

func TestResolveProjectDirRejectsFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveProjectDir(file); err == nil {
		t.Fatal("expected an error for a file path")
	}
	if _, err := resolveProjectDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected an error for a missing path")
	}
}
