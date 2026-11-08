package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// FileStore is an append-only JSONL event store: one file per thread under dir.
// It is the durable counterpart to MemoryStore, so a thread's history survives a
// restart and the read model can be rebuilt by replaying it.
type FileStore struct {
	dir string

	mu  sync.Mutex
	seq map[contracts.ThreadID]int64
}

// NewFileStore opens (creating if needed) an event store rooted at dir.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir, seq: map[contracts.ThreadID]int64{}}, nil
}

func (s *FileStore) path(id contracts.ThreadID) string {
	return filepath.Join(s.dir, string(id)+".jsonl")
}

// Append writes events to the thread's log, assigning monotonic sequence numbers
// and syncing to disk before returning.
func (s *FileStore) Append(_ context.Context, threadID contracts.ThreadID, payloads []contracts.EventPayload, clock Clock) ([]contracts.Event, error) {
	if clock == nil {
		clock = time.Now
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.seq[threadID]; !ok {
		existing, err := s.loadLocked(threadID)
		if err != nil {
			return nil, err
		}
		var max int64
		for _, e := range existing {
			if e.Seq > max {
				max = e.Seq
			}
		}
		s.seq[threadID] = max
	}

	var buf bytes.Buffer
	out := make([]contracts.Event, 0, len(payloads))
	for _, payload := range payloads {
		s.seq[threadID]++
		ev := contracts.Event{Seq: s.seq[threadID], ThreadID: threadID, At: clock(), Payload: payload}
		data, err := json.Marshal(ev)
		if err != nil {
			return nil, err
		}
		buf.Write(data)
		buf.WriteByte('\n')
		out = append(out, ev)
	}

	f, err := os.OpenFile(s.path(threadID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	return out, nil
}

// Load reads a thread's events. A torn final line (from an interrupted write) is
// skipped rather than failing the whole log.
func (s *FileStore) Load(_ context.Context, threadID contracts.ThreadID) ([]contracts.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(threadID)
}

func (s *FileStore) loadLocked(threadID contracts.ThreadID) ([]contracts.Event, error) {
	data, err := os.ReadFile(s.path(threadID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []contracts.Event
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev contracts.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // torn or unknown event: skip it rather than lose the thread
		}
		out = append(out, ev)
	}
	return out, nil
}

// Threads lists the threads with a durable log.
func (s *FileStore) Threads(_ context.Context) ([]contracts.ThreadID, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []contracts.ThreadID
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		out = append(out, contracts.ThreadID(strings.TrimSuffix(name, ".jsonl")))
	}
	return out, nil
}

// DropEmptyThreads removes the event logs of threads whose only events are
// their creation ones — threads that were created and never used. Creation is
// provisional now and writes nothing, but logs written by older versions keep
// these ghosts; this reclaims them. Returns the ids removed.
func (s *FileStore) DropEmptyThreads() ([]contracts.ThreadID, error) {
	ctx := context.Background()
	ids, err := s.Threads(ctx)
	if err != nil {
		return nil, err
	}
	var removed []contracts.ThreadID
	for _, id := range ids {
		events, err := s.Load(ctx, id)
		if err != nil {
			// A torn or unreadable log is not the caller's problem here; leave it.
			continue
		}
		empty := true
		for _, ev := range events {
			switch ev.Payload.(type) {
			case contracts.ThreadCreated, contracts.PeerMailboxSet:
			default:
				empty = false
			}
		}
		if !empty {
			continue
		}
		if err := os.Remove(s.path(id)); err == nil {
			removed = append(removed, id)
		}
	}
	return removed, nil
}
