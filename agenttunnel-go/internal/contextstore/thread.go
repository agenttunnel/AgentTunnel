package contextstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ThreadSnapshot is the durable per-thread context that outlives the process, so
// closing the laptop does not lose the orchestrator's memory of a thread.
type ThreadSnapshot struct {
	ThreadID  string             `json:"threadId"`
	UpdatedAt time.Time          `json:"updatedAt"`
	Notebook  map[string][]Entry `json:"notebook"`
	History   []string           `json:"history"`
}

// ThreadStore persists one JSON file per thread.
type ThreadStore struct {
	dir string
}

// NewThreadStore points at the base directory for thread snapshots.
func NewThreadStore(dir string) *ThreadStore { return &ThreadStore{dir: dir} }

func (s *ThreadStore) path(id string) string {
	return filepath.Join(s.dir, "threads", id+".json")
}

// Save writes the snapshot atomically (temp file + rename).
func (s *ThreadStore) Save(snap ThreadSnapshot) error {
	dir := filepath.Join(s.dir, "threads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	snap.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path(snap.ThreadID))
}

// Load reads a thread snapshot. The bool reports whether one existed.
func (s *ThreadStore) Load(id string) (ThreadSnapshot, bool, error) {
	data, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return ThreadSnapshot{}, false, nil
	}
	if err != nil {
		return ThreadSnapshot{}, false, err
	}
	var snap ThreadSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ThreadSnapshot{}, false, err
	}
	return snap, true, nil
}
