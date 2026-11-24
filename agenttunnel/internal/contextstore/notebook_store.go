package contextstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	opSet    = "set"
	opAppend = "append"
)

// record is one line of a thread's notebook file. A set replaces the key's
// entries with this one; an append adds it. Replaying the lines in order
// rebuilds the same key order, entries, and timestamps.
type record struct {
	Op     string    `json:"op"`
	Key    string    `json:"key"`
	Author string    `json:"author"`
	Value  string    `json:"value"`
	At     time.Time `json:"at"`
}

// NotebookStore keeps each thread's notebook in <dir>/<thread>.jsonl so shared
// results, notes, and curated context survive a daemon restart.
type NotebookStore struct {
	dir string
}

// NewNotebookStore returns a store rooted at dir. Files are created on first write.
// Compaction temp files left by a crash are swept here, once per daemon boot,
// before any compaction can be in flight.
func NewNotebookStore(dir string) *NotebookStore {
	if stale, _ := filepath.Glob(filepath.Join(dir, "*.jsonl.tmp-*")); len(stale) > 0 {
		for _, p := range stale {
			os.Remove(p)
		}
	}
	return &NotebookStore{dir: dir}
}

func (s *NotebookStore) pathFor(id string) string {
	return filepath.Join(s.dir, fileName(id)+".jsonl")
}

// Open replays a thread's notebook from its file and returns it bound to that
// file. It never fails: a file it cannot read or rewrite leaves the notebook
// memory-only for this process, and the reason goes to onError.
//
// Replay skips torn or corrupt lines. When the file holds superseded records,
// a bad line, or a torn tail, it is rewritten with only the live entries. That
// compaction runs on load: a file grows within one process and is trimmed the
// next time its thread is opened, so its size is bounded per load, not per process.
func (s *NotebookStore) Open(id string, onError func(error)) *Notebook {
	n := NewNotebook()
	n.onError = onError
	n.file = s.pathFor(id)
	data, err := os.ReadFile(n.file)
	if os.IsNotExist(err) {
		return n
	}
	if err != nil {
		n.file = ""
		n.report(err)
		return n
	}
	if rewrite := replay(data, n); rewrite {
		if err := n.compact(); err != nil {
			n.file = ""
			n.report(err)
		}
	}
	return n
}

// Remove deletes a thread's notebook file. A missing file is not an error.
func (s *NotebookStore) Remove(id string) error {
	err := os.Remove(s.pathFor(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// replay applies a notebook file's records to n in order. It reports whether
// the file should be rewritten: a corrupt line, a torn tail, or records that
// the live entries no longer need.
func replay(data []byte, n *Notebook) (rewrite bool) {
	records := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil || (rec.Op != opSet && rec.Op != opAppend) {
			rewrite = true
			continue
		}
		n.place(rec.Key, Entry{Author: rec.Author, Value: rec.Value, At: rec.At}, rec.Op == opSet)
		records++
	}
	live := 0
	for _, entries := range n.entries {
		live += len(entries)
	}
	if records > live || (len(data) > 0 && data[len(data)-1] != '\n') {
		rewrite = true
	}
	return rewrite
}

// compact rewrites the thread's file with exactly the live entries, through a
// temp file and a rename so a crash leaves either the old or the new file.
// Replaying the rewritten file gives the same key order, entries, and timestamps.
func (n *Notebook) compact() error {
	tmp, err := os.CreateTemp(filepath.Dir(n.file), filepath.Base(n.file)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	for _, key := range n.order {
		for i, e := range n.entries[key] {
			op := opAppend
			if i == 0 {
				op = opSet
			}
			line, err := json.Marshal(record{Op: op, Key: key, Author: e.Author, Value: e.Value, At: e.At})
			if err != nil {
				tmp.Close()
				return err
			}
			w.Write(append(line, '\n'))
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	// Flush the data before the rename, as domain/filestore does for its log:
	// otherwise a crash can leave the renamed file empty and every note lost.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), n.file)
}

// fileName makes a thread id safe to use as a file name. Thread ids are
// server-generated th_<hex>, so this is defence in depth: any separator or
// ".." in an id is replaced, the same rule logstore applies.
func fileName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "_"
	}
	return out
}
