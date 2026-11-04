package contextstore

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Entry is one value in the shared notebook, attributed to its author.
type Entry struct {
	Author string    `json:"author"`
	Value  string    `json:"value"`
	At     time.Time `json:"at"`
}

// Notebook is bounded shared memory for a workflow run: the orchestrator and its
// subagents read and write named keys so each agent can see what the others did.
type Notebook struct {
	mu      sync.RWMutex
	entries map[string][]Entry
	order   []string
	clock   func() time.Time
}

// NewNotebook returns an empty notebook.
func NewNotebook() *Notebook {
	return &Notebook{entries: map[string][]Entry{}, clock: time.Now}
}

// Set replaces a key's entries with a single value (typically the orchestrator).
func (n *Notebook) Set(key, value, author string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.entries[key]; !ok {
		n.order = append(n.order, key)
	}
	n.entries[key] = []Entry{{Author: author, Value: value, At: n.clock()}}
}

// Append adds a value to a key without replacing existing entries (subagents).
func (n *Notebook) Append(key, value, author string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.entries[key]; !ok {
		n.order = append(n.order, key)
	}
	n.entries[key] = append(n.entries[key], Entry{Author: author, Value: value, At: n.clock()})
}

// Get returns the entries for a key.
func (n *Notebook) Get(key string) []Entry {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return append([]Entry(nil), n.entries[key]...)
}

// Keys returns keys in first-write order.
func (n *Notebook) Keys() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return append([]string(nil), n.order...)
}

// Snapshot returns a deep copy of all entries, for persistence.
func (n *Notebook) Snapshot() map[string][]Entry {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make(map[string][]Entry, len(n.entries))
	for k, v := range n.entries {
		out[k] = append([]Entry(nil), v...)
	}
	return out
}

// Render produces a markdown block for prompt injection. With no keys, every key
// is rendered in insertion order.
func (n *Notebook) Render(keys ...string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	selected := keys
	if len(selected) == 0 {
		selected = n.order
	}
	var b strings.Builder
	b.WriteString("## Shared context\n")
	for _, k := range selected {
		entries := n.entries[k]
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n", k)
		for _, e := range entries {
			fmt.Fprintf(&b, "- [%s] %s\n", e.Author, e.Value)
		}
	}
	return b.String()
}
