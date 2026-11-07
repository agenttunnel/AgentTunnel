package orchestrator

import (
	"fmt"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// spawnRequest is a subagent's proposal to spawn one of its own subagents,
// held until the thread's lead approves or rejects it by name.
type spawnRequest struct {
	RequesterID contracts.AgentID
	Name        string
	Role        contracts.RoleID
	Task        string
	Harness     string
	Model       string
	Lane        string
	DependsOn   []string
}

// spawnStore holds pending spawn requests, partitioned by thread: the runner
// is daemon-global and the lead's fixed agent id is thread-agnostic, so
// requests must never be keyed by agent alone.
type spawnStore struct {
	mu      sync.Mutex
	pending map[contracts.ThreadID][]spawnRequest
}

func newSpawnStore() *spawnStore {
	return &spawnStore{pending: map[contracts.ThreadID][]spawnRequest{}}
}

// add records a request, reporting false when an identical one is already
// pending so a command that was both executed and printed does not double-list.
// Names are made unique within the thread so the lead's approve/reject --name
// is unambiguous.
func (s *spawnStore) add(threadID contracts.ThreadID, req spawnRequest) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.pending[threadID]
	for _, p := range list {
		if p.RequesterID == req.RequesterID && p.Name == req.Name && p.Role == req.Role && p.Task == req.Task {
			return false
		}
	}
	name := req.Name
	for i := 2; nameTaken(list, name); i++ {
		name = fmt.Sprintf("%s-%d", req.Name, i)
	}
	req.Name = name
	s.pending[threadID] = append(list, req)
	return true
}

func nameTaken(list []spawnRequest, name string) bool {
	for _, p := range list {
		if p.Name == name {
			return true
		}
	}
	return false
}

// list returns an ordered snapshot of a thread's pending requests.
func (s *spawnStore) list(threadID contracts.ThreadID) []spawnRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spawnRequest, len(s.pending[threadID]))
	copy(out, s.pending[threadID])
	return out
}

// take removes and returns the request a lead decision named.
func (s *spawnStore) take(threadID contracts.ThreadID, name string) (spawnRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.pending[threadID]
	for i, p := range list {
		if p.Name == name {
			s.pending[threadID] = append(list[:i:i], list[i+1:]...)
			return p, true
		}
	}
	return spawnRequest{}, false
}

// clear drops a thread's pending requests. RunTurn defers this on every exit
// path, so a request never outlives its turn.
func (s *spawnStore) clear(threadID contracts.ThreadID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, threadID)
}
