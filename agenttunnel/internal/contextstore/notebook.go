package contextstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
//
// A notebook opened from a NotebookStore also mirrors each write to its file.
// The memory write and the file write happen under the same lock, so file order
// always matches memory order.
type Notebook struct {
	mu      sync.RWMutex
	entries map[string][]Entry
	order   []string
	clock   func() time.Time

	// file is the thread's JSONL mirror; empty means memory only.
	file    string
	onError func(error)
	// torn is set after a failed write; the next write starts a new line so a
	// partial record cannot merge with it.
	torn bool
	// removed is set once the thread is deleted: later writes must not
	// recreate its file.
	removed bool
}

// NewNotebook returns an empty, memory-only notebook.
func NewNotebook() *Notebook {
	return &Notebook{entries: map[string][]Entry{}, clock: time.Now}
}

// Set replaces a key's entries with a single value (typically the orchestrator).
func (n *Notebook) Set(key, value, author string) {
	key, value, author = clean(key), clean(value), clean(author)
	n.mu.Lock()
	defer n.mu.Unlock()
	e := Entry{Author: author, Value: value, At: n.clock()}
	n.place(key, e, true)
	n.persist(opSet, key, e)
}

// Append adds a value to a key without replacing existing entries (subagents).
func (n *Notebook) Append(key, value, author string) {
	key, value, author = clean(key), clean(value), clean(author)
	n.mu.Lock()
	defer n.mu.Unlock()
	e := Entry{Author: author, Value: value, At: n.clock()}
	n.place(key, e, false)
	n.persist(opAppend, key, e)
}

// clean replaces invalid UTF-8 before the write. json.Marshal would substitute
// U+FFFD only on disk, so memory and a reload would disagree.
func clean(s string) string {
	return strings.ToValidUTF8(s, "�")
}

// Discard stops disk writes for a deleted thread's notebook. A turn still
// holding the notebook can keep writing in memory, but cannot recreate the file.
func (n *Notebook) Discard() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.removed = true
}

// place applies one write to memory. Caller holds n.mu.
func (n *Notebook) place(key string, e Entry, replace bool) {
	if _, ok := n.entries[key]; !ok {
		n.order = append(n.order, key)
	}
	if replace {
		n.entries[key] = []Entry{e}
		return
	}
	n.entries[key] = append(n.entries[key], e)
}

// persist mirrors one write to the thread file. Caller holds n.mu. A failed
// write is reported, never returned: the turn that wrote the note keeps going,
// and memory stays authoritative for this process.
func (n *Notebook) persist(op, key string, e Entry) {
	if n.file == "" || n.removed {
		return
	}
	line, err := json.Marshal(record{Op: op, Key: key, Author: e.Author, Value: e.Value, At: e.At})
	if err == nil {
		err = n.appendLine(line)
	}
	if err != nil {
		n.torn = true
		n.report(err)
	}
}

// appendLine writes one record as one write call. After a failure it leads with
// a newline, which ends any partial record the failure left behind; the blank
// line is skipped on load.
func (n *Notebook) appendLine(line []byte) error {
	if err := os.MkdirAll(filepath.Dir(n.file), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(n.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	out := append(line, '\n')
	if n.torn {
		out = append([]byte{'\n'}, out...)
	}
	if _, err := f.Write(out); err != nil {
		return err
	}
	n.torn = false
	return nil
}

// report surfaces a persistence failure through the owner's hook, if any.
func (n *Notebook) report(err error) {
	if err != nil && n.onError != nil {
		n.onError(err)
	}
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
