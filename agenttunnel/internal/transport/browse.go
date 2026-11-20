package transport

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxBrowseEntries bounds one fs.browse reply. A directory wider than this is
// not one a person scrolls through to find a project.
const maxBrowseEntries = 2000

// BrowseEntry is one subdirectory in an fs.browse reply.
type BrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// BrowseResult lists the subdirectories of one directory on the daemon's
// machine. Parent is empty at the filesystem root.
type BrowseResult struct {
	Path      string        `json:"path"`
	Parent    string        `json:"parent,omitempty"`
	Home      string        `json:"home,omitempty"`
	Entries   []BrowseEntry `json:"entries"`
	Truncated bool          `json:"truncated,omitempty"`
}

// browseDirs backs fs.browse, the web UI's folder picker for adding a project:
// a browser has no native dialog that can name a path on the daemon's machine.
// It lists directory names only, never files or contents. An empty path starts
// at the home directory; "~" expands to it; anything else must be absolute.
func browseDirs(path string, hidden bool) (BrowseResult, error) {
	home, _ := os.UserHomeDir()
	path = strings.TrimSpace(path)
	switch {
	case path == "" || path == "~":
		if home == "" {
			return BrowseResult{}, errors.New("no home directory; enter an absolute path")
		}
		path = home
	case strings.HasPrefix(path, "~/") && home != "":
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return BrowseResult{}, fmt.Errorf("%q is not an absolute path", path)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return BrowseResult{}, browseErr(path, err)
	}
	if !info.IsDir() {
		return BrowseResult{}, fmt.Errorf("%s is not a folder", path)
	}
	dirents, err := os.ReadDir(path)
	if err != nil {
		return BrowseResult{}, browseErr(path, err)
	}
	out := BrowseResult{Path: path, Home: home, Entries: []BrowseEntry{}}
	if parent := filepath.Dir(path); parent != path {
		out.Parent = parent
	}
	for _, e := range dirents {
		name := e.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		isDir := e.IsDir()
		// A symlink counts when it resolves to a directory; a broken one is skipped.
		if e.Type()&fs.ModeSymlink != 0 {
			if target, err := os.Stat(full); err == nil {
				isDir = target.IsDir()
			}
		}
		if !isDir {
			continue
		}
		if len(out.Entries) == maxBrowseEntries {
			out.Truncated = true
			break
		}
		out.Entries = append(out.Entries, BrowseEntry{Name: name, Path: full})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
	})
	return out, nil
}

func browseErr(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("cannot open %s: permission denied", path)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s does not exist", path)
	}
	return fmt.Errorf("cannot open %s: %w", path, err)
}
