package files

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxMarkdownBytes caps a markdown document the viewer loads. Larger files are
// refused rather than truncated, so the viewer never shows a silently cut page.
const MaxMarkdownBytes = MaxReadBytes

// MarkdownFile is one markdown document for the read-only viewer.
type MarkdownFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Text string `json:"text"`
}

// ReadMarkdown returns a markdown document under root. rel is root-relative or
// an absolute path. The resolved file (after symlinks) must sit inside root and
// end in .md or .markdown, and must be at most MaxMarkdownBytes. Anything else
// is refused. root must be non-empty: an empty root would resolve to the
// daemon's own working directory.
func ReadMarkdown(root, rel string) (MarkdownFile, error) {
	if root == "" {
		return MarkdownFile{}, errors.New("markdown: no working directory for this thread")
	}
	if !isMarkdownName(rel) {
		return MarkdownFile{}, fmt.Errorf("markdown: %q is not a .md or .markdown file", rel)
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return MarkdownFile{}, err
	}
	if base, err = filepath.EvalSymlinks(base); err != nil {
		return MarkdownFile{}, fmt.Errorf("markdown: working directory: %w", err)
	}
	full := filepath.Join(base, filepath.FromSlash(rel))
	if filepath.IsAbs(rel) {
		full = filepath.Clean(rel)
	}
	real, err := resolveInside(base, full, rel)
	if err != nil {
		return MarkdownFile{}, err
	}
	// Open first, then verify the descriptor. Checking a path and reopening it
	// leaves a window where a component can be swapped for a symlink out of the
	// root; reading through the verified descriptor does not.
	f, err := os.Open(real)
	if err != nil {
		return MarkdownFile{}, fmt.Errorf("markdown %q: %w", rel, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return MarkdownFile{}, fmt.Errorf("markdown %q: %w", rel, err)
	}
	if !fi.Mode().IsRegular() {
		return MarkdownFile{}, fmt.Errorf("markdown %q is not a file", rel)
	}
	if fi.Size() > MaxMarkdownBytes {
		return MarkdownFile{}, fmt.Errorf("markdown %q is too large to view (%d bytes, max %d)", rel, fi.Size(), MaxMarkdownBytes)
	}
	// The descriptor must be the file the path resolves to inside root right now.
	again, err := resolveInside(base, full, rel)
	if err != nil {
		return MarkdownFile{}, err
	}
	ri, err := os.Stat(again)
	if err != nil {
		return MarkdownFile{}, fmt.Errorf("markdown %q: %w", rel, err)
	}
	if !os.SameFile(fi, ri) {
		return MarkdownFile{}, fmt.Errorf("markdown %q changed while it was being opened", rel)
	}
	// Bound the read too: the file may have grown since Stat.
	data, err := io.ReadAll(io.LimitReader(f, MaxMarkdownBytes+1))
	if err != nil {
		return MarkdownFile{}, err
	}
	if len(data) > MaxMarkdownBytes {
		return MarkdownFile{}, fmt.Errorf("markdown %q grew past %d bytes while reading", rel, MaxMarkdownBytes)
	}
	relOut, err := filepath.Rel(base, again)
	if err != nil {
		return MarkdownFile{}, err
	}
	return MarkdownFile{Path: filepath.ToSlash(relOut), Size: fi.Size(), Text: string(data)}, nil
}

// resolveInside resolves full through symlinks and checks the result is a
// markdown file inside base. base must already be symlink-resolved.
func resolveInside(base, full, rel string) (string, error) {
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("markdown %q: %w", rel, err)
	}
	if !within(base, real) {
		return "", fmt.Errorf("markdown %q is outside the working directory", rel)
	}
	if !isMarkdownName(real) {
		return "", fmt.Errorf("markdown %q resolves to a non-markdown file", rel)
	}
	return real, nil
}

func isMarkdownName(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// within reports whether p is base or lies beneath it. Both must already be
// symlink-resolved.
func within(base, p string) bool {
	return p == base || strings.HasPrefix(p, strings.TrimSuffix(base, string(os.PathSeparator))+string(os.PathSeparator))
}
