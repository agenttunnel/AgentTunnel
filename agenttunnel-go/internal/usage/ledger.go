package usage

import (
	"sort"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Ledger accumulates usage across every agent in an environment. It answers the
// "collective usage, including the orchestrator" question, and preserves the
// orchestrator/subagent split at the environment level too.
type Ledger struct {
	mu             sync.Mutex
	perAgent       map[contracts.AgentID]contracts.AgentUsage
	isOrchestrator map[contracts.AgentID]bool
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{
		perAgent:       map[contracts.AgentID]contracts.AgentUsage{},
		isOrchestrator: map[contracts.AgentID]bool{},
	}
}

// Record adds a usage delta for one agent, accumulating across calls.
func (l *Ledger) Record(agent contracts.AgentID, role contracts.RoleID, orchestrator bool, delta contracts.TokenUsage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := l.perAgent[agent]
	cur.AgentID = agent
	cur.Role = role
	cur.Usage = cur.Usage.Add(delta)
	l.perAgent[agent] = cur
	l.isOrchestrator[agent] = orchestrator
}

// Agent returns the accumulated usage for a single agent.
func (l *Ledger) Agent(agent contracts.AgentID) (contracts.AgentUsage, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	u, ok := l.perAgent[agent]
	return u, ok
}

// Snapshot rolls the environment into an orchestrator/subagent split plus a
// per-agent listing, sorted by agent id for stable output.
func (l *Ledger) Snapshot() contracts.ThreadUsage {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out contracts.ThreadUsage
	for id, u := range l.perAgent {
		if l.isOrchestrator[id] {
			out.Orchestrator = out.Orchestrator.Add(u.Usage)
		} else {
			out.Subagents = out.Subagents.Add(u.Usage)
		}
		out.ByAgent = append(out.ByAgent, u)
	}
	sort.Slice(out.ByAgent, func(i, j int) bool { return out.ByAgent[i].AgentID < out.ByAgent[j].AgentID })
	return out
}

// Total is orchestrator + subagent usage combined.
func (l *Ledger) Total() contracts.TokenUsage { return l.Snapshot().Total() }
