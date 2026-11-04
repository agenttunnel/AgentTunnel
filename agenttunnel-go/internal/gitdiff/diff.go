// Package gitdiff turns the working tree into a structured, line-addressable
// diff so clients can render a review view and attach comments to lines.
package gitdiff

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// LineKind classifies a diff line.
type LineKind string

const (
	KindMeta    LineKind = "meta"
	KindHunk    LineKind = "hunk"
	KindContext LineKind = "context"
	KindAdd     LineKind = "add"
	KindDelete  LineKind = "delete"
)

// Line is one line of a file's diff, addressed by its old and/or new number so a
// review comment can target it precisely.
type Line struct {
	Old  int      `json:"old,omitempty"`
	New  int      `json:"new,omitempty"`
	Kind LineKind `json:"kind"`
	Text string   `json:"text"`
}

// File is a changed file with its parsed lines.
type File struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Lines     []Line `json:"lines"`
}

// Diff is a parsed working-tree diff.
type Diff struct {
	Files []File `json:"files"`
	Raw   string `json:"raw"`
	Empty bool   `json:"empty"`
}

// WorkingTreeDiff diffs the working tree (staged + unstaged) against HEAD.
func WorkingTreeDiff(ctx context.Context, workdir string) (Diff, error) {
	out, err := run(ctx, workdir, "diff", "HEAD", "--no-color", "--no-ext-diff")
	if err != nil {
		// No commits yet: fall back to the index-vs-worktree diff.
		out, err = run(ctx, workdir, "diff", "--no-color", "--no-ext-diff")
		if err != nil {
			return Diff{}, err
		}
	}
	return ParseDiff(out), nil
}

// StatusCode is a file's porcelain status: added, modified, deleted,
// unmerged, untracked, or renamed. Renames report under their new path.
type StatusCode string

const (
	StatusAdded     StatusCode = "added"
	StatusModified  StatusCode = "modified"
	StatusDeleted   StatusCode = "deleted"
	StatusUnmerged  StatusCode = "unmerged"
	StatusUntracked StatusCode = "untracked"
	StatusRenamed   StatusCode = "renamed"
)

// StatusEntry is one path's working-tree status.
type StatusEntry struct {
	Path   string     `json:"path"`
	Status StatusCode `json:"status"`
}

// Status runs git status --porcelain and maps it to per-path entries, capped
// so a huge worktree cannot flood the client. Non-git directories yield no
// entries (and no error) so file trees still render.
func Status(ctx context.Context, workdir string) ([]StatusEntry, error) {
	out, err := run(ctx, workdir, "status", "--porcelain=v1", "-uall", "--no-renames")
	if err != nil {
		return nil, nil
	}
	var entries []StatusEntry
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		xy, path := line[:2], strings.TrimSpace(line[3:])
		// Porcelain quotes paths with special characters; strip the quotes so
		// they join back to real filesystem paths.
		path = strings.Trim(path, `"`)
		if path == "" {
			continue
		}
		entries = append(entries, StatusEntry{Path: path, Status: classifyStatus(xy)})
		if len(entries) >= 2000 {
			break
		}
	}
	return entries, nil
}

func classifyStatus(xy string) StatusCode {
	// Unmerged states carry U in either column, or AA/DD pairs.
	if strings.Contains(xy, "U") || xy == "AA" || xy == "DD" {
		return StatusUnmerged
	}
	x, y := xy[0], xy[1]
	// Prefer the worktree column: it is what the user sees uncommitted.
	if y == '?' && x == '?' {
		return StatusUntracked
	}
	if y == 'A' || x == 'A' {
		return StatusAdded
	}
	if y == 'D' || x == 'D' {
		return StatusDeleted
	}
	if y == 'R' || x == 'R' {
		return StatusRenamed
	}
	if y == 'M' || x == 'M' || y == 'T' || x == 'T' {
		return StatusModified
	}
	return StatusModified
}

func run(ctx context.Context, workdir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", workdir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return string(out), nil
}

// ParseDiff parses unified diff text into files and numbered lines.
func ParseDiff(text string) Diff {
	d := Diff{Raw: text}
	var cur *File
	oldLine, newLine := 0, 0

	for _, raw := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(raw, "diff --git "):
			d.Files = append(d.Files, File{})
			cur = &d.Files[len(d.Files)-1]
			oldLine, newLine = 0, 0
			cur.Lines = append(cur.Lines, Line{Kind: KindMeta, Text: raw})

		case cur == nil:
			continue

		case strings.HasPrefix(raw, "--- "):
			cur.OldPath = strings.TrimPrefix(strings.TrimPrefix(raw, "--- "), "a/")
			cur.Lines = append(cur.Lines, Line{Kind: KindMeta, Text: raw})

		case strings.HasPrefix(raw, "+++ "):
			cur.Path = strings.TrimPrefix(strings.TrimPrefix(raw, "+++ "), "b/")
			cur.Lines = append(cur.Lines, Line{Kind: KindMeta, Text: raw})

		case strings.HasPrefix(raw, "@@"):
			oldLine, newLine = parseHunkHeader(raw)
			cur.Lines = append(cur.Lines, Line{Kind: KindHunk, Text: raw})

		case strings.HasPrefix(raw, "\\"):
			cur.Lines = append(cur.Lines, Line{Kind: KindMeta, Text: raw})

		case strings.HasPrefix(raw, "+"):
			cur.Additions++
			cur.Lines = append(cur.Lines, Line{New: newLine, Kind: KindAdd, Text: raw[1:]})
			newLine++

		case strings.HasPrefix(raw, "-"):
			cur.Deletions++
			cur.Lines = append(cur.Lines, Line{Old: oldLine, Kind: KindDelete, Text: raw[1:]})
			oldLine++

		case raw == "":
			continue

		default:
			cur.Lines = append(cur.Lines, Line{Old: oldLine, New: newLine, Kind: KindContext, Text: strings.TrimPrefix(raw, " ")})
			oldLine++
			newLine++
		}
	}

	d.Empty = len(d.Files) == 0
	return d
}

func parseHunkHeader(s string) (int, int) {
	s = strings.TrimPrefix(s, "@@")
	var oldStart, newStart int
	for _, field := range strings.Fields(s) {
		switch {
		case strings.HasPrefix(field, "-"):
			oldStart = leadingNumber(field[1:])
		case strings.HasPrefix(field, "+"):
			newStart = leadingNumber(field[1:])
		}
	}
	return oldStart, newStart
}

func leadingNumber(s string) int {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}
