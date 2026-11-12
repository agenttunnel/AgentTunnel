// Package files lists and reads a thread's working tree so clients can render
// a file browser. All paths are slash-separated and relative to the workdir
// root; anything escaping the root is rejected.
package files

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// MaxEntries caps a tree so a huge worktree cannot flood the client.
// MaxReadBytes caps a single file read. MaxWriteBytes caps a single file write.
const (
	MaxEntries    = 5000
	MaxReadBytes  = 256 * 1024
	MaxWriteBytes = 1024 * 1024
)

// Entry is one file or directory in the tree.
type Entry struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	Dir      bool    `json:"dir"`
	Size     int64   `json:"size,omitempty"`
	Children []Entry `json:"children,omitempty"`
}

// skipDirs are never descended into when walking a non-git directory.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	".venv": true, "__pycache__": true, "target": true,
	".agenttunnel": true, ".t3": true, ".opencode": true,
}

// Tree returns the workdir's file tree, dirs-first and alpha-sorted like a
// file explorer. Inside a git repo the listing comes from git ls-files so
// .gitignore is honoured for free; otherwise the directory is walked with a
// skip list. Only files that exist on disk are included.
func Tree(ctx context.Context, workdir string) ([]Entry, error) {
	paths, err := trackedPaths(ctx, workdir)
	if err != nil {
		return nil, err
	}
	root := &Entry{Name: ".", Path: "", Dir: true}
	count := 0
	for _, p := range paths {
		if count >= MaxEntries {
			break
		}
		full := filepath.Join(workdir, filepath.FromSlash(p))
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		insert(root, p, info.Size())
		count++
	}
	sortEntries(root)
	return root.Children, nil
}

func insert(root *Entry, path string, size int64) {
	parts := strings.Split(path, "/")
	node := root
	cur := ""
	for i, part := range parts {
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		last := i == len(parts)-1
		var next *Entry
		for j := range node.Children {
			if node.Children[j].Name == part {
				next = &node.Children[j]
				break
			}
		}
		if next == nil {
			next = &Entry{Name: part, Path: cur, Dir: !last}
			node.Children = append(node.Children, *next)
			next = &node.Children[len(node.Children)-1]
		}
		if last {
			next.Size = size
		}
		node = next
	}
}

func sortEntries(e *Entry) {
	sort.Slice(e.Children, func(i, j int) bool {
		a, b := e.Children[i], e.Children[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	for i := range e.Children {
		if e.Children[i].Dir {
			sortEntries(&e.Children[i])
		}
	}
}

// trackedPaths lists candidate files: git ls-files (tracked + untracked,
// excluding ignored) inside a repo, else a bounded directory walk.
func trackedPaths(ctx context.Context, workdir string) ([]string, error) {
	if out, err := gitOut(ctx, workdir, "ls-files", "--cached", "--others", "--exclude-standard", "-z"); err == nil {
		var paths []string
		for _, p := range strings.Split(out, "\x00") {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
		return paths, nil
	}
	var paths []string
	err := filepath.WalkDir(workdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(workdir, path)
		if err != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || len(strings.Split(rel, string(os.PathSeparator))) > 10 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if len(paths) >= MaxEntries {
			return filepath.SkipDir
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	return paths, err
}

func gitOut(ctx context.Context, workdir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", workdir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git: %s", strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// ReadResult is a file's content with truncation metadata.
type ReadResult struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Text      string `json:"text"`
}

// Read returns up to MaxReadBytes of a workdir-relative file. Binary files
// (NUL in the sniffed prefix) come back flagged with empty text.
func Read(workdir, relPath string) (ReadResult, error) {
	full, err := Resolve(workdir, relPath)
	if err != nil {
		return ReadResult{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return ReadResult{}, fmt.Errorf("stat %q: %w", relPath, err)
	}
	if !info.Mode().IsRegular() {
		return ReadResult{}, fmt.Errorf("%q is not a file", relPath)
	}
	f, err := os.Open(full)
	if err != nil {
		return ReadResult{}, err
	}
	defer f.Close()
	buf := make([]byte, MaxReadBytes+1)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return ReadResult{}, err
	}
	buf = buf[:n]
	res := ReadResult{Path: relPath, Size: info.Size(), Truncated: info.Size() > MaxReadBytes}
	if bytes.IndexByte(buf[:min(len(buf), 8192)], 0) >= 0 {
		res.Binary = true
		return res, nil
	}
	res.Text = string(buf[:min(len(buf), MaxReadBytes)])
	return res, nil
}

// Write replaces a workdir-relative file with data, creating missing parent
// directories. The same root confinement as Read applies.
func Write(workdir, relPath string, data []byte) error {
	full, err := Resolve(workdir, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

// Resolve joins a client-supplied relative path to the workdir and returns the
// real on-disk location it names. Symlinks are followed before the confinement
// check: the deepest existing ancestor of the target is resolved with
// EvalSymlinks and must stay under the resolved workdir, so a link inside the
// worktree that points outside it is refused for reads and writes alike. A
// missing tail is lexically under that ancestor, so it cannot be a link.
func Resolve(workdir, relPath string) (string, error) {
	clean := filepath.Clean("/" + filepath.ToSlash(relPath))
	if clean == "/" {
		return "", fmt.Errorf("not a file")
	}
	rel := filepath.FromSlash(strings.TrimPrefix(clean, "/"))
	root, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	// Lstat, not Stat, so a dangling link is seen and refused rather than
	// written through. The walk stops at the root at the latest, which exists.
	existing := filepath.Join(root, rel)
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = filepath.Dir(existing)
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("path cannot be resolved: %w", err)
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes the working directory")
	}
	return filepath.Join(append([]string{resolved}, tail...)...), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
