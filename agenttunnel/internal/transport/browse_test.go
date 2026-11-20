package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func browseRPC(t *testing.T, params map[string]any) (BrowseResult, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	disp := NewDispatcher(DispatcherDeps{})
	res, err := disp.Handle(context.Background(), Request{Method: "fs.browse", Params: raw})
	if err != nil {
		return BrowseResult{}, err
	}
	out, ok := res.(BrowseResult)
	if !ok {
		t.Fatalf("fs.browse result = %T, want BrowseResult", res)
	}
	return out, nil
}

func entryNames(r BrowseResult) []string {
	names := make([]string, len(r.Entries))
	for i, e := range r.Entries {
		names[i] = e.Name
	}
	return names
}

// fs.browse lists subdirectories only, sorted case-insensitively, hides dot
// folders unless asked, follows a symlink to a directory, and names the parent.
func TestBrowseListsDirectoriesOnly(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"beta", "Alpha", ".hidden", "gamma"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gamma"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "notes.txt"), filepath.Join(root, "filelink")); err != nil {
		t.Fatal(err)
	}

	r, err := browseRPC(t, map[string]any{"path": root})
	if err != nil {
		t.Fatalf("fs.browse: %v", err)
	}
	if got, want := strings.Join(entryNames(r), ","), "Alpha,beta,gamma,link"; got != want {
		t.Fatalf("entries = %s, want %s", got, want)
	}
	if r.Path != filepath.Clean(root) || r.Parent != filepath.Dir(filepath.Clean(root)) {
		t.Fatalf("path/parent = %q/%q", r.Path, r.Parent)
	}
	if r.Entries[0].Path != filepath.Join(root, "Alpha") {
		t.Fatalf("entry path = %q", r.Entries[0].Path)
	}

	r, err = browseRPC(t, map[string]any{"path": root, "hidden": true})
	if err != nil {
		t.Fatalf("fs.browse hidden: %v", err)
	}
	if got, want := strings.Join(entryNames(r), ","), ".hidden,Alpha,beta,gamma,link"; got != want {
		t.Fatalf("entries with hidden = %s, want %s", got, want)
	}
}

// An empty path starts at the home directory, so the picker opens somewhere useful.
func TestBrowseDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.Mkdir(filepath.Join(home, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "~"} {
		r, err := browseRPC(t, map[string]any{"path": path})
		if err != nil {
			t.Fatalf("fs.browse %q: %v", path, err)
		}
		if r.Path != home || r.Home != home {
			t.Fatalf("fs.browse %q: path=%q home=%q, want %q", path, r.Path, r.Home, home)
		}
		if got := strings.Join(entryNames(r), ","); got != "code" {
			t.Fatalf("fs.browse %q: entries = %s, want code", path, got)
		}
	}
	r, err := browseRPC(t, map[string]any{"path": "~/code"})
	if err != nil || r.Path != filepath.Join(home, "code") {
		t.Fatalf("fs.browse ~/code: %+v %v", r, err)
	}
}

// Relative paths, files and missing paths are refused with a plain message.
func TestBrowseRefusesBadPaths(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"relative": "some/dir",
		"file":     file,
		"missing":  filepath.Join(root, "nope"),
	}
	for name, path := range cases {
		if _, err := browseRPC(t, map[string]any{"path": path}); err == nil {
			t.Fatalf("%s: fs.browse %q succeeded, want an error", name, path)
		}
	}
}

// An unreadable directory reports permission denied instead of an empty list.
func TestBrowseReportsPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not bind here")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	_, err := browseRPC(t, map[string]any{"path": locked})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want permission denied", err)
	}
}
