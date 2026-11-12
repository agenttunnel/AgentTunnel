// Package fileedit lists, reads, and atomically rewrites text files under a
// thread's working directory for the desktop file editor. Every client path is
// root-relative and slash-separated; a path that would name anything outside
// the root, directly or through a symlink, is rejected.
package fileedit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxFileBytes caps a file the editor will read or write. MaxListEntries caps
// one directory listing so a huge folder cannot flood the client.
const (
	MaxFileBytes   = 1 << 20
	MaxListEntries = 2000
)

var (
	ErrOutsideRoot = errors.New("path escapes the working directory")
	ErrNotFile     = errors.New("not a regular file")
	ErrNotDir      = errors.New("not a directory")
	ErrBinary      = errors.New("file is not text")
	ErrTooLarge    = errors.New("file is larger than the editor limit")
	// ErrGitDir refuses anything under a .git entry. Hooks and config there run
	// code or change repository behavior, so no editor path may reach them.
	ErrGitDir = errors.New(".git is not editable from the file editor")
	// ErrMixedLineEndings refuses a file whose line breaks are not all LF or all
	// CRLF (or that has bare CRs). Saving it would rewrite some of its breaks.
	ErrMixedLineEndings = errors.New("file mixes line endings; edit it in another editor")
	// ErrConflict means the file's content no longer matches the hash the
	// client read, so a write would silently discard someone else's edit.
	ErrConflict = errors.New("file changed on disk since it was opened")
)

// Entry is one child of a listed directory.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
}

// Listing is one directory level. Truncated is set when MaxListEntries cut it.
type Listing struct {
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated,omitempty"`
}

// Document is a text file's content plus the hash a later Write must present.
type Document struct {
	Path string `json:"path"`
	Text string `json:"text"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
}

// Root is a working directory with its symlinks resolved once, so every
// containment check compares real paths.
type Root struct{ real string }

// Open resolves root to its real path. An empty root is an error: callers
// must not fall back to the process working directory.
func Open(root string) (Root, error) {
	if root == "" {
		return Root{}, errors.New("thread has no working directory")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Root{}, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Root{}, err
	}
	return Root{real: real}, nil
}

// resolve maps a client path to the real on-disk path it names. Traversal is
// rejected lexically before anything touches the filesystem; symlinks are
// followed and the final target must still sit inside the root. Every check
// applies to .git too, both as the named path and as a symlink target, so
// List, Read, and Write agree on what is reachable.
func (r Root) resolve(rel string) (string, string, error) {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) || filepath.VolumeName(rel) != "" {
		return "", "", ErrOutsideRoot
	}
	slashed := filepath.ToSlash(rel)
	for _, part := range strings.Split(slashed, "/") {
		if part == ".." {
			return "", "", ErrOutsideRoot
		}
	}
	clean := path.Clean(slashed)
	if clean == "." {
		clean = ""
	}
	if hasGitPart(clean) {
		return "", "", ErrGitDir
	}
	real, err := filepath.EvalSymlinks(filepath.Join(r.real, filepath.FromSlash(clean)))
	if err != nil {
		return "", "", err
	}
	if !within(r.real, real) {
		return "", "", ErrOutsideRoot
	}
	inside, err := filepath.Rel(r.real, real)
	if err != nil {
		return "", "", ErrOutsideRoot
	}
	if hasGitPart(filepath.ToSlash(inside)) {
		return "", "", ErrGitDir
	}
	return clean, real, nil
}

// hasGitPart reports whether any slash-separated element of p is .git. The
// comparison folds case because macOS and Windows filesystems do too.
func hasGitPart(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

// lineEnding reports the line break a file uses: "\r\n" or "\n". ok is false
// when the file mixes breaks or has bare CRs, because the editor cannot keep
// such a file's bytes intact through CodeMirror's LF-only buffer.
func lineEnding(data []byte) (eol string, ok bool) {
	crlf := bytes.Count(data, []byte("\r\n"))
	bareLF := bytes.Count(data, []byte("\n")) - crlf
	bareCR := bytes.Count(data, []byte("\r")) - crlf
	switch {
	case bareCR > 0, crlf > 0 && bareLF > 0:
		return "", false
	case crlf > 0:
		return "\r\n", true
	}
	return "\n", true
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// List returns one directory level, dirs first then alphabetical. It skips
// entries that resolve() refuses: .git, symlinks whose target falls outside
// the root, and broken links.
func List(root, rel string) (Listing, error) {
	r, err := Open(root)
	if err != nil {
		return Listing{}, err
	}
	clean, dir, err := r.resolve(rel)
	if err != nil {
		return Listing{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Listing{}, err
	}
	if !info.IsDir() {
		return Listing{}, ErrNotDir
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return Listing{}, err
	}
	out := Listing{Entries: []Entry{}}
	for _, de := range des {
		childRel := path.Join(clean, de.Name())
		_, child, err := r.resolve(childRel)
		if err != nil {
			continue
		}
		ci, err := os.Stat(child)
		if err != nil {
			continue
		}
		if len(out.Entries) == MaxListEntries {
			out.Truncated = true
			break
		}
		e := Entry{Name: de.Name(), Path: childRel, Dir: ci.IsDir()}
		if ci.Mode().IsRegular() {
			e.Size = ci.Size()
		}
		out.Entries = append(out.Entries, e)
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

// Read returns a text file's content with CRLF folded to LF, since the editor
// buffer is LF-only. Hash covers the raw bytes, which is what Write compares.
// Files over MaxFileBytes, files that are not valid UTF-8 or contain NUL bytes,
// and files with mixed line endings are refused rather than truncated or
// rewritten, so the editor can never save a partial or altered file back.
func Read(root, rel string) (Document, error) {
	r, err := Open(root)
	if err != nil {
		return Document{}, err
	}
	clean, real, err := r.resolve(rel)
	if err != nil {
		return Document{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return Document{}, err
	}
	if !info.Mode().IsRegular() {
		return Document{}, ErrNotFile
	}
	if info.Size() > MaxFileBytes {
		return Document{}, ErrTooLarge
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return Document{}, err
	}
	if len(data) > MaxFileBytes {
		return Document{}, ErrTooLarge
	}
	if !isText(data) {
		return Document{}, ErrBinary
	}
	if _, ok := lineEnding(data); !ok {
		return Document{}, ErrMixedLineEndings
	}
	return Document{
		Path: clean,
		Text: strings.ReplaceAll(string(data), "\r\n", "\n"),
		Size: int64(len(data)),
		Hash: hashOf(data),
	}, nil
}

// Write replaces an existing text file atomically: the content goes to a
// temp file beside the target, is synced, then renamed over it, so a reader
// or a crash sees either the old file or the new one. base must be the hash
// the client last read; any other content on disk returns ErrConflict. The
// file's own line break is restored, so a CRLF file stays CRLF.
//
// Known limit: the hash check and the rename are two steps, so a writer that
// lands between them is overwritten. Agents edit in their own processes, so an
// in-process lock would not cover them; the window is milliseconds and is
// accepted rather than locked.
func Write(root, rel, text, base string) (Document, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if len(text) > MaxFileBytes {
		return Document{}, ErrTooLarge
	}
	if strings.Contains(text, "\r") {
		return Document{}, ErrMixedLineEndings
	}
	if !isText([]byte(text)) {
		return Document{}, ErrBinary
	}
	r, err := Open(root)
	if err != nil {
		return Document{}, err
	}
	clean, real, err := r.resolve(rel)
	if err != nil {
		return Document{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return Document{}, err
	}
	if !info.Mode().IsRegular() {
		return Document{}, ErrNotFile
	}
	cur, err := os.ReadFile(real)
	if err != nil {
		return Document{}, err
	}
	if hashOf(cur) != base {
		return Document{}, ErrConflict
	}
	eol, ok := lineEnding(cur)
	if !ok {
		return Document{}, ErrMixedLineEndings
	}
	data := []byte(text)
	if eol == "\r\n" {
		data = []byte(strings.ReplaceAll(text, "\n", "\r\n"))
	}
	if len(data) > MaxFileBytes {
		return Document{}, ErrTooLarge
	}
	if err := writeAtomic(real, data, info.Mode().Perm()); err != nil {
		return Document{}, err
	}
	return Document{Path: clean, Size: int64(len(data)), Hash: hashOf(data)}, nil
}

func writeAtomic(target string, data []byte, perm os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(target), err)
	}
	return nil
}

func isText(data []byte) bool {
	return bytes.IndexByte(data, 0) < 0 && utf8.Valid(data)
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
