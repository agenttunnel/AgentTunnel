package contextstore

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const guideSeed = `# Inherited context

Guidance shared by every project in this environment. Treat it as binding unless
the current task overrides it.

## Auto-mode model selection

In auto mode, pick the harness and model for each task from the runtime choices
you are given, not from this guide. Model lineups change; this list does not.

- Quick lookups and small edits: the fastest, cheapest capable model.
- Multi-file implementation: a capable general coding model.
- Deep debugging or review: the strongest reasoning model offered.
- Long-running refactors: a model and harness that keep a steady session.
- Cheap bulk or read-only exploration: the lowest-cost model that can read and summarize.

Where a harness offers model families (for example Haiku, Sonnet, Opus), choose
the family that fits the row above. When a pick is confirmed to work well, or
goes badly, record it in LOG.md with the task kind, harness, model, and outcome.
`

// InheritedContext is the global, cross-project memory. It lives outside any
// repository so learnings carry between projects.
type InheritedContext struct {
	dir   string
	mu    sync.Mutex
	clock func() time.Time
}

// NewInheritedContext points at a context directory (created by Ensure).
func NewInheritedContext(dir string) *InheritedContext {
	return &InheritedContext{dir: dir, clock: time.Now}
}

// Dir returns the context directory.
func (c *InheritedContext) Dir() string { return c.dir }

// Ensure creates the context folder and its seed files if missing.
func (c *InheritedContext) Ensure() error {
	if err := os.MkdirAll(filepath.Join(c.dir, "rules"), 0o755); err != nil {
		return err
	}
	for name, seed := range map[string]string{
		"GUIDE.md": guideSeed,
		"LOG.md":   "# Learnings log\n",
	} {
		path := filepath.Join(c.dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// Render returns the guide, all rule files, and the log, concatenated for prompt
// injection.
func (c *InheritedContext) Render() (string, error) {
	var b strings.Builder
	if guide, err := os.ReadFile(filepath.Join(c.dir, "GUIDE.md")); err == nil {
		b.Write(guide)
		b.WriteString("\n")
	}
	rules, _ := filepath.Glob(filepath.Join(c.dir, "rules", "*.md"))
	sort.Strings(rules)
	for _, rule := range rules {
		data, err := os.ReadFile(rule)
		if err != nil {
			continue
		}
		b.WriteString("\n<!-- " + filepath.Base(rule) + " -->\n")
		b.Write(data)
		b.WriteString("\n")
	}
	if log, err := os.ReadFile(filepath.Join(c.dir, "LOG.md")); err == nil {
		b.WriteString("\n")
		b.Write(log)
	}
	return b.String(), nil
}

// Record appends a learning to LOG.md.
func (c *InheritedContext) Record(learning string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Ensure(); err != nil {
		return err
	}
	line := "- " + strings.TrimSpace(learning) + " (" + c.clock().UTC().Format(time.RFC3339) + ")\n"
	f, err := os.OpenFile(filepath.Join(c.dir, "LOG.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

// Rule is one rules/*.md file, by file base name.
type Rule struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

// Guide returns GUIDE.md.
func (c *InheritedContext) Guide() (string, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, "GUIDE.md"))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

// Log returns LOG.md.
func (c *InheritedContext) Log() (string, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, "LOG.md"))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

// Rules returns the rules/*.md files sorted by name.
func (c *InheritedContext) Rules() ([]Rule, error) {
	paths, _ := filepath.Glob(filepath.Join(c.dir, "rules", "*.md"))
	sort.Strings(paths)
	out := make([]Rule, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out = append(out, Rule{Name: strings.TrimSuffix(filepath.Base(p), ".md"), Body: string(data)})
	}
	return out, nil
}

// SetGuide overwrites GUIDE.md.
func (c *InheritedContext) SetGuide(body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Ensure(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.dir, "GUIDE.md"), []byte(body), 0o644)
}

// SetLog overwrites LOG.md.
func (c *InheritedContext) SetLog(body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Ensure(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.dir, "LOG.md"), []byte(body), 0o644)
}

// ruleName keeps a rule's file name safe: letters, digits, dashes, underscores.
func ruleName(name string) (string, error) {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "", os.ErrInvalid
	}
	return out, nil
}

// SetRule writes one rules/<name>.md file.
func (c *InheritedContext) SetRule(name, body string) error {
	safe, err := ruleName(name)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Ensure(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.dir, "rules", safe+".md"), []byte(body), 0o644)
}

// DeleteRule removes one rules/<name>.md file.
func (c *InheritedContext) DeleteRule(name string) error {
	safe, err := ruleName(name)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return os.Remove(filepath.Join(c.dir, "rules", safe+".md"))
}
