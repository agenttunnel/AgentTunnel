package orchestrator

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Docker-style names (adjective_scientist, e.g. quirky_turing) give an unnamed
// subagent a handle people can read in the roster, the graph, and a peer
// message's --to. Names are unique within a thread.
var (
	nameAdjectives = [...]string{
		"bold", "bright", "brave", "calm", "clever", "daring", "eager", "gentle",
		"happy", "jolly", "keen", "lively", "merry", "nimble", "proud", "quirky",
		"quiet", "swift", "witty", "zesty",
	}
	nameScientists = [...]string{
		"babbage", "curie", "dijkstra", "euler", "feynman", "gauss", "goodall",
		"hamilton", "hopper", "kepler", "knuth", "lamport", "lovelace", "mccarthy",
		"newton", "noether", "ritchie", "shannon", "thompson", "turing",
	}
)

// generateAgentName picks a docker-style name for a dispatch. The starting pair
// is a pure function of seed, so the same dispatch always gets the same first
// candidate; `taken` skips names the thread or the current batch already uses,
// scanning forward through the word pairs and then suffixing when every pair is
// taken.
func generateAgentName(seed string, taken func(string) bool) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	total := len(nameAdjectives) * len(nameScientists)
	start := int(h.Sum32() % uint32(total))
	for i := range total {
		k := (start + i) % total
		name := nameAdjectives[k/len(nameScientists)] + "_" + nameScientists[k%len(nameScientists)]
		if !taken(name) {
			return name
		}
	}
	base := nameAdjectives[start/len(nameScientists)] + "_" + nameScientists[start%len(nameScientists)]
	for n := 2; ; n++ {
		if name := fmt.Sprintf("%s-%d", base, n); !taken(name) {
			return name
		}
	}
}

// threadNameSet collects every agent name the thread has recorded, so a
// generated name never reuses one from an earlier pass.
func (o *Orchestrator) threadNameSet(ctx context.Context, threadID contracts.ThreadID) map[string]bool {
	used := map[string]bool{}
	st, err := o.runner.engine.State(ctx, threadID)
	if err != nil {
		return used
	}
	for _, a := range st.Agents {
		used[a.Name] = true
	}
	return used
}
