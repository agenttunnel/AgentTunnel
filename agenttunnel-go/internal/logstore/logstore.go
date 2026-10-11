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

// Store keeps per-agent log entries in a bounded in-memory buffer and mirrors
// every entry to a JSONL file so full logs survive a restart.
type Store struct {
	dir string
	max int

	mu      sync.Mutex
	buffers map[contracts.AgentID][]contracts.LogEntry
}

// New creates a log store rooted at dir.
func New(dir string) *Store {
	return &Store{
		dir:     dir,
		max:     defaultMaxEntries,
		buffers: map[contracts.AgentID][]contracts.LogEntry{},
	}
}

func (s *Store) fileFor(agent contracts.AgentID) string {
	return filepath.Join(s.dir, "agent-output", sanitize(string(agent))+".log")
}

// sanitize makes an agent id safe to use as a file name. Ids are derived from
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

// Append records an entry in memory (bounded) and on disk (append-only).
func (s *Store) Append(e contracts.LogEntry) error {
	s.mu.Lock()
	buf := append(s.buffers[e.AgentID], e)
	if len(buf) > s.max {
		buf = buf[len(buf)-s.max:]
	}
	s.buffers[e.AgentID] = buf
	s.mu.Unlock()
	return s.appendFile(e)
}

func (s *Store) appendFile(e contracts.LogEntry) error {
	path := s.fileFor(e.AgentID)
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

// Entries returns the in-memory buffer for an agent.
func (s *Store) Entries(agent contracts.AgentID) []contracts.LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contracts.LogEntry(nil), s.buffers[agent]...)
}

// ReadFile reads the persisted JSONL log for an agent.
func (s *Store) ReadFile(agent contracts.AgentID) ([]contracts.LogEntry, error) {
	data, err := os.ReadFile(s.fileFor(agent))
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
