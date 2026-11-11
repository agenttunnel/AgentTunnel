package domain

import (
	"context"
	"math"
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

// scopedStates returns the read model of every thread in scope.
func (e *Engine) scopedStates(ctx context.Context, scope HarnessScope) ([]*ThreadState, error) {
	ids, err := e.Threads(ctx)
	if err != nil {
		return nil, err
	}
	var out []*ThreadState
	for _, id := range ids {
		st, err := e.State(ctx, id)
		if err != nil {
			return nil, err
		}
		if scope.includes(id, st) {
			out = append(out, st)
		}
	}
	return out, nil
}

// HarnessStats returns the per-harness rollup across the threads in scope, busiest
// harness first (by agent count, then by name for a stable order).
func (e *Engine) HarnessStats(ctx context.Context, scope HarnessScope) ([]HarnessStat, error) {
	states, err := e.scopedStates(ctx, scope)
	if err != nil {
		return nil, err
	}
	acc := map[contracts.Harness]*HarnessStat{}
	for _, st := range states {
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

// ModelStat rolls up one harness and model pair across the threads in scope. An
// empty Model is an agent that ran on the harness default.
type ModelStat struct {
	Harness   contracts.Harness    `json:"harness"`
	Model     string               `json:"model"`
	Threads   int                  `json:"threads"`
	Agents    int                  `json:"agents"`
	Subagents int                  `json:"subagents"`
	Usage     contracts.TokenUsage `json:"usage"`
	// Resolved counts subagent runs that succeeded. Only those feed the timing
	// fields: a run that failed or was cancelled says nothing about how long the
	// model takes to finish a task.
	Resolved int `json:"resolved"`
	// MedianSeconds and P90Seconds are the wall-clock time of resolved subagent
	// runs. Both are zero when Resolved is zero.
	MedianSeconds float64 `json:"medianSeconds"`
	P90Seconds    float64 `json:"p90Seconds"`
}

type modelKey struct {
	harness contracts.Harness
	model   string
}

// modelAcc is one model's rollup while the environment is still being walked.
// Durations stay here until the walk ends, so quantiles see every thread at once.
type modelAcc struct {
	stat      ModelStat
	durations []float64
}

// addModelStats folds one thread's read model into the per-model rollup, keyed
// the way addHarnessStats keys harnesses, with the model as the second part.
func addModelStats(acc map[modelKey]*modelAcc, st *ThreadState) {
	touched := map[modelKey]bool{}
	get := func(a contracts.Agent) *modelAcc {
		k := modelKey{a.Harness, a.Model}
		m, ok := acc[k]
		if !ok {
			m = &modelAcc{stat: ModelStat{Harness: a.Harness, Model: a.Model}}
			acc[k] = m
		}
		touched[k] = true
		return m
	}

	for _, id := range st.AgentOrder {
		a := st.Agents[id]
		m := get(a)
		m.stat.Agents++
		if a.IsOrchestrator() {
			continue
		}
		m.stat.Subagents++
		if a.Status == contracts.AgentSucceeded && a.EndedAt != nil {
			m.stat.Resolved++
			m.durations = append(m.durations, a.EndedAt.Sub(a.StartedAt).Seconds())
		}
	}
	for id, au := range st.Usage {
		m := get(st.Agents[id])
		m.stat.Usage = m.stat.Usage.Add(au.Usage)
	}
	for k := range touched {
		acc[k].stat.Threads++
	}
}

// quantile reads the p-th quantile (0..1) of sorted values by linear
// interpolation between the two nearest ranks, so the median of an even count
// is the mean of the middle pair.
func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// ModelStats returns the harness-and-model rollup across the threads in scope,
// most-used first (by agent count, then by harness and model for a stable order).
// Clients re-rank by tokens themselves, so this list stays the source of truth.
func (e *Engine) ModelStats(ctx context.Context, scope HarnessScope) ([]ModelStat, error) {
	states, err := e.scopedStates(ctx, scope)
	if err != nil {
		return nil, err
	}
	acc := map[modelKey]*modelAcc{}
	for _, st := range states {
		addModelStats(acc, st)
	}
	out := make([]ModelStat, 0, len(acc))
	for _, m := range acc {
		s := m.stat
		if len(m.durations) > 0 {
			sort.Float64s(m.durations)
			s.MedianSeconds = quantile(m.durations, 0.5)
			s.P90Seconds = quantile(m.durations, 0.9)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Agents != b.Agents {
			return a.Agents > b.Agents
		}
		if a.Harness != b.Harness {
			return a.Harness < b.Harness
		}
		return a.Model < b.Model
	})
	return out, nil
}
