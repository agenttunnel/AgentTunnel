// Package deliverables keeps each turn's final answer as a markdown file, so
// the answer can be read, shared, or diffed without the run's logs around it.
package deliverables

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store writes deliverables under one directory: <dir>/<threadID>/<file>.md.
type Store struct {
	dir string
}

// New returns a store rooted at dir. The directory is created on first write.
func New(dir string) *Store { return &Store{dir: dir} }

// Write saves one turn's final answer and returns the file path. The file name
// carries the turn's UTC time and the message ID, and an existing file is never
// overwritten: a name clash gets a numeric suffix instead.
func (s *Store) Write(threadID string, at time.Time, messageID, text string) (string, error) {
	if s == nil {
		return "", errors.New("deliverables: no store")
	}
	dir := filepath.Join(s.dir, safe(threadID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stem := "turn-" + at.UTC().Format("20060102-150405") + "-" + safe(messageID)
	for n := 0; ; n++ {
		name := stem + ".md"
		if n > 0 {
			name = fmt.Sprintf("%s-%d.md", stem, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.WriteString(strings.TrimRight(text, "\n") + "\n"); err != nil {
			f.Close()
			return "", err
		}
		return path, f.Close()
	}
}

// safe keeps an ID usable as a single path element: anything outside a small
// alphabet becomes '-', so an ID can never escape the store directory.
func safe(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}
