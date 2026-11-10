package domain

import (
	"context"
	"sort"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// HarnessStat rolls up one harness across every thread in an environment. It is
// derived from each thread's read model, so it survives restarts and needs no
// persisted state of its own.
type HarnessStat struct {
	Harness contracts.Harness `json:"harness"`
	// Threads counts the threads that ran at least one agent on this harness.
	Threads int `json:"threads"`
	// Agents counts every agent instance (lead and subagent) on this harness.
	Agents int `json:"agents"`
	// Subagents counts dispatched subagents on this harness: the invocation count.
	Subagents int                  `json:"subagents"`
	Usage     contracts.TokenUsage `json:"usage"`
}

// addHarnessStats folds one thread's read model into the per-harness rollup. An
// agent's usage is attributed through its own record, so usage on a harness
// stays with the harness that ran it, even when a thread mixes several.
func addHarnessStats(acc map[contracts.Harness]*HarnessStat, st *ThreadState) {
	touched := map[contracts.Harness]bool{}
	get := func(h contracts.Harness) *HarnessStat {
		s, ok := acc[h]
		if !ok {
			s = &HarnessStat{Harness: h}
			acc[h] = s
		}
		touched[h] = true
		return s
	}

	for _, id := range st.AgentOrder {
		a := st.Agents[id]
		s := get(a.Harness)
		s.Agents++
		if !a.IsOrchestrator() {
			s.Subagents++
		}
	}
	for id, au := range st.Usage {
		s := get(st.Agents[id].Harness)
		s.Usage = s.Usage.Add(au.Usage)
	}
	for h := range touched {
		acc[h].Threads++
	}
}

// HarnessScope narrows the rollup. The zero value is the whole environment. A
// ThreadID selects that one thread; otherwise a ProjectID selects its threads.
type HarnessScope struct {
	ThreadID  contracts.ThreadID
	ProjectID string
}

func (s HarnessScope) includes(id contracts.ThreadID, st *ThreadState) bool {
	if s.ThreadID != "" {
		return id == s.ThreadID
	}
	return s.ProjectID == "" || st.ProjectID == s.ProjectID
}

// HarnessStats returns the per-harness rollup across the threads in scope, busiest
// harness first (by agent count, then by name for a stable order).
func (e *Engine) HarnessStats(ctx context.Context, scope HarnessScope) ([]HarnessStat, error) {
	ids, err := e.Threads(ctx)
	if err != nil {
		return nil, err
	}
	acc := map[contracts.Harness]*HarnessStat{}
	for _, id := range ids {
		st, err := e.State(ctx, id)
		if err != nil {
			return nil, err
		}
		if !scope.includes(id, st) {
			continue
		}
		addHarnessStats(acc, st)
	}
	out := make([]HarnessStat, 0, len(acc))
	for _, s := range acc {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agents != out[j].Agents {
			return out[i].Agents > out[j].Agents
		}
		return out[i].Harness < out[j].Harness
	})
	return out, nil
}
