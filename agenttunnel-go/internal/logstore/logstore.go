package logstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

const defaultMaxEntries = 2000

// Store keeps each thread's output entries in a bounded in-memory buffer and
// mirrors every entry to that thread's JSONL file, so a thread's output survives
// a restart. Entries are keyed by thread, not agent: the team lead's id is fixed
// across threads, so an agent-keyed log would mix threads together.
type Store struct {
	dir string
	max int

	mu      sync.Mutex
	buffers map[contracts.ThreadID][]contracts.LogEntry
	loaded  map[contracts.ThreadID]bool
}

// New creates a log store rooted at dir.
func New(dir string) *Store {
	return &Store{
		dir:     dir,
		max:     defaultMaxEntries,
		buffers: map[contracts.ThreadID][]contracts.LogEntry{},
		loaded:  map[contracts.ThreadID]bool{},
	}
}

func (s *Store) fileFor(thread contracts.ThreadID) string {
	return filepath.Join(s.dir, "thread-output", sanitize(string(thread))+".log")
}

// sanitize makes an id safe to use as a file name. Ids are derived from
// model-supplied task names, so anything that could escape the directory (path
// separators, "..") is replaced.
func sanitize(id string) string {
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

// ensureLoaded fills a thread's buffer from its file the first time the thread
// is touched in this process, so the buffer always holds the newest entries
// including those written before a restart. Caller holds s.mu.
func (s *Store) ensureLoaded(thread contracts.ThreadID) {
	if s.loaded[thread] {
		return
	}
	s.loaded[thread] = true
	entries, _ := s.readFile(thread)
	if len(entries) > s.max {
		entries = entries[len(entries)-s.max:]
	}
	s.buffers[thread] = entries
}

// Append records an entry in memory (bounded) and on disk (append-only).
func (s *Store) Append(thread contracts.ThreadID, e contracts.LogEntry) error {
	s.mu.Lock()
	s.ensureLoaded(thread)
	buf := append(s.buffers[thread], e)
	if len(buf) > s.max {
		buf = buf[len(buf)-s.max:]
	}
	s.buffers[thread] = buf
	s.mu.Unlock()
	return s.appendFile(thread, e)
}

func (s *Store) appendFile(thread contracts.ThreadID, e contracts.LogEntry) error {
	path := s.fileFor(thread)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

// Entries returns a copy of a thread's bounded log, oldest first.
func (s *Store) Entries(thread contracts.ThreadID) []contracts.LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded(thread)
	return append([]contracts.LogEntry(nil), s.buffers[thread]...)
}

// readFile reads a thread's persisted JSONL log.
func (s *Store) readFile(thread contracts.ThreadID) ([]contracts.LogEntry, error) {
	data, err := os.ReadFile(s.fileFor(thread))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []contracts.LogEntry
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e contracts.LogEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
