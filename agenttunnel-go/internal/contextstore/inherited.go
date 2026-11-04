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
