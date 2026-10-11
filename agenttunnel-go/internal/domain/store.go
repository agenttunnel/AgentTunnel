package domain

import (
	"context"
	"sync"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Clock returns the current time; injectable for deterministic tests.
type Clock func() time.Time

// EventStore is the durable append-only log of domain events, partitioned by
// thread. Appending assigns a monotonic per-thread sequence number.
type EventStore interface {
	Append(ctx context.Context, threadID contracts.ThreadID, payloads []contracts.EventPayload, clock Clock) ([]contracts.Event, error)
	Load(ctx context.Context, threadID contracts.ThreadID) ([]contracts.Event, error)
	Threads(ctx context.Context) ([]contracts.ThreadID, error)
}

// MemoryStore is an in-process EventStore. It is the reference implementation;
// a SQLite-backed store is a drop-in for durability.
type MemoryStore struct {
	mu   sync.Mutex
	logs map[contracts.ThreadID][]contracts.Event
	seq  map[contracts.ThreadID]int64
}

// NewMemoryStore returns an empty in-memory event store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		logs: make(map[contracts.ThreadID][]contracts.Event),
		seq:  make(map[contracts.ThreadID]int64),
	}
}

func (m *MemoryStore) Append(_ context.Context, threadID contracts.ThreadID, payloads []contracts.EventPayload, clock Clock) ([]contracts.Event, error) {
	if clock == nil {
		clock = time.Now
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]contracts.Event, 0, len(payloads))
	for _, p := range payloads {
		m.seq[threadID]++
		ev := contracts.Event{
			Seq:      m.seq[threadID],
			ThreadID: threadID,
			At:       clock(),
			Payload:  p,
		}
		m.logs[threadID] = append(m.logs[threadID], ev)
		out = append(out, ev)
	}
	return out, nil
}

func (m *MemoryStore) Load(_ context.Context, threadID contracts.ThreadID) ([]contracts.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]contracts.Event(nil), m.logs[threadID]...), nil
}

func (m *MemoryStore) Threads(_ context.Context) ([]contracts.ThreadID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]contracts.ThreadID, 0, len(m.logs))
	for id := range m.logs {
		out = append(out, id)
	}
	return out, nil
}
