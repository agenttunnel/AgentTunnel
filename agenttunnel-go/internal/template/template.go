// Package template parses and scaffolds `at run` job templates: Markdown files
// whose YAML frontmatter names the job, picks the project, and pins per-role
// models, and whose body becomes the thread's first turn.
package template

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultName is the template the commands fall back to when no path is given.
const DefaultName = "agenttunnel.md"

// Roles lists the role ids a v1 template may configure. The daemon ships more
// builtin roles (facilitator, context-manager), but a job template is scoped to
// the roles a dispatched job actually uses.
var Roles = []string{"orchestrator", "planner", "executor", "tester", "reviewer"}

// RoleConfig is one role's model override. Every field is optional: an omitted
// field inherits the daemon's configured runtime for that role.
type RoleConfig struct {
	Harness string `yaml:"harness"`
	Model   string `yaml:"model"`
	Variant string `yaml:"variant"`
}

// ModelConfig maps a role id to its override.
type ModelConfig map[string]RoleConfig

// Template is a parsed job template.
type Template struct {
	// Name is the thread title: frontmatter `name`, else the file stem.
	Name string
	// Project is a path, project id, or project name. It defaults to ".".
	Project string
	// ModelConfig holds the per-role overrides that apply to this job only.
	ModelConfig ModelConfig
	// Body is the Markdown after the closing delimiter, verbatim. It is the
	// first turn's text.
	Body string
	// Path is the file Parse/Load read; empty for Parse.
	Path string
}

// frontmatter is the YAML document between the delimiter lines. Decoding with
// KnownFields makes a misspelled key a clear error instead of a silently
// ignored setting.
type frontmatter struct {
	Name        string      `yaml:"name"`
	Project     string      `yaml:"project"`
	ModelConfig ModelConfig `yaml:"modelConfig"`
}

// Parse parses a template from memory. Because there is no filename, an
// omitted `name` is an error; Load derives it from the file stem instead.
func Parse(data []byte) (*Template, error) {
	return parse(data, "")
}

// Load reads and parses a template file, defaulting the name to the file stem
// when the frontmatter omits it.
func Load(path string) (*Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	tmpl, err := parse(data, stem)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	tmpl.Path = path
	return tmpl, nil
}

func parse(data []byte, fallbackName string) (*Template, error) {
	front, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}

	var fm frontmatter
	dec := yaml.NewDecoder(bytes.NewReader(front))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("template frontmatter: %w", err)
	}

	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = strings.TrimSpace(fallbackName)
	}
	if name == "" {
		return nil, errors.New(`template: "name" is required (load the file with Load to default it to the filename)`)
	}

	project := strings.TrimSpace(fm.Project)
	if project == "" {
		project = "."
	}

	if err := validateModelConfig(fm.ModelConfig); err != nil {
		return nil, err
	}

	text := string(body)
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("template %q: the body is empty; put the task text after the closing --- line", name)
	}

	return &Template{
		Name:        name,
		Project:     project,
		ModelConfig: fm.ModelConfig,
		Body:        text,
	}, nil
}

// splitFrontmatter separates the YAML block from the body. The first line must
// be `---`; the block closes on the next line that is exactly `---`. The body
// is everything after that line, byte for byte.
func splitFrontmatter(data []byte) (front, body []byte, err error) {
	if len(data) == 0 {
		return nil, nil, errors.New(`template: the file is empty; it needs YAML frontmatter delimited by --- lines`)
	}

	firstEnd := bytes.IndexByte(data, '\n')
	firstLine := data
	rest := []byte(nil)
	if firstEnd >= 0 {
		firstLine = data[:firstEnd]
		rest = data[firstEnd+1:]
	}
	if string(bytes.TrimSuffix(firstLine, []byte("\r"))) != "---" {
		return nil, nil, errors.New(`template: frontmatter must start with "---" on the first line`)
	}

	for offset := 0; offset <= len(rest); {
		lineEnd := bytes.IndexByte(rest[offset:], '\n')
		var line []byte
		next := len(rest)
		if lineEnd < 0 {
			line = rest[offset:]
		} else {
			line = rest[offset : offset+lineEnd]
			next = offset + lineEnd + 1
		}
		if string(bytes.TrimSuffix(line, []byte("\r"))) == "---" {
			return rest[:offset], rest[next:], nil
		}
		if lineEnd < 0 {
			break
		}
		offset = next
	}
	return nil, nil, errors.New(`template: frontmatter is not closed; add a "---" line after the YAML`)
}

func validateModelConfig(mc ModelConfig) error {
	for role := range mc {
		if !validRole(role) {
			return fmt.Errorf("template: unknown modelConfig role %q (want one of %s)", role, strings.Join(Roles, ", "))
		}
	}
	return nil
}

func validRole(role string) bool {
	for _, known := range Roles {
		if role == known {
			return true
		}
	}
	return false
}

// Find resolves a template reference against cwd. It tries, in order, the ref
// itself, ref with ".md", and ref with ".agenttunnel.md"; an empty ref means
// the default agenttunnel.md. Absolute refs are used as they are. The first
// existing regular file wins.
func Find(cwd, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	var candidates []string
	switch {
	case ref == "":
		candidates = []string{filepath.Join(cwd, DefaultName)}
	case filepath.IsAbs(ref):
		candidates = []string{ref, ref + ".md", ref + ".agenttunnel.md"}
	default:
		candidates = []string{
			filepath.Join(cwd, ref),
			filepath.Join(cwd, ref+".md"),
			filepath.Join(cwd, ref+".agenttunnel.md"),
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if len(candidates) == 1 {
		return "", fmt.Errorf("template %s not found", candidates[0])
	}
	return "", fmt.Errorf("template %q not found (tried %s)", ref, strings.Join(candidates, ", "))
}

// Scaffold returns a starter template for name. The output is valid input to
// Parse: every active setting is a real key, and the rest of the format is
// taught in comments.
func Scaffold(name string) []byte {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSuffix(DefaultName, filepath.Ext(DefaultName))
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("# AgentTunnel job template.\n")
	b.WriteString("#\n")
	b.WriteString("# Everything between this line and the next `---` is YAML configuration.\n")
	b.WriteString("# The Markdown below the closing `---` is the task text: it becomes the\n")
	b.WriteString("# thread's first turn. Edit the body first; the settings are optional.\n")
	b.WriteString("#\n")
	b.WriteString("# Run it with:  at run <this file>\n")
	b.WriteString("\n")
	b.WriteString("# name: the thread title. Delete this line to use the file name instead.\n")
	fmt.Fprintf(&b, "name: %s\n", strconv.Quote(name))
	b.WriteString("\n")
	b.WriteString("# project: the repository this job works in. Use a path, or the id/name of\n")
	b.WriteString("# a registered project (`at projects ls`). Defaults to the current directory.\n")
	b.WriteString("project: \".\"\n")
	b.WriteString("\n")
	b.WriteString("# modelConfig: per-role model overrides for this job only. Roles:\n")
	b.WriteString("# orchestrator, planner, executor, tester, reviewer. Each role accepts\n")
	b.WriteString("# harness, model, and variant (variant is optional). Omit a role or a field\n")
	b.WriteString("# to inherit the daemon's setting for it. Keep only what you want to pin.\n")
	b.WriteString("modelConfig:\n")
	b.WriteString("  # The lead that plans the work and dispatches jobs.\n")
	b.WriteString("  orchestrator:\n")
	b.WriteString("    harness: opencode\n")
	b.WriteString("    model: claude-opus-4-5\n")
	b.WriteString("  # One worker per dispatched job. `variant` picks a reasoning level when the\n")
	b.WriteString("  # harness has them.\n")
	b.WriteString("  executor:\n")
	b.WriteString("    harness: codex\n")
	b.WriteString("    model: gpt-5-codex\n")
	b.WriteString("    variant: high\n")
	b.WriteString("  # Uncomment to pin the remaining roles:\n")
	b.WriteString("  # planner:\n")
	b.WriteString("  #   harness: opencode\n")
	b.WriteString("  #   model: claude-opus-4-5\n")
	b.WriteString("  # tester:\n")
	b.WriteString("  #   harness: codex\n")
	b.WriteString("  #   model: gpt-5-codex\n")
	b.WriteString("  # reviewer:\n")
	b.WriteString("  #   harness: claude\n")
	b.WriteString("  #   model: claude-opus-4-5\n")
	b.WriteString("---\n")
	b.WriteString("Describe the job here. State the goal, where to look, and what \"done\"\n")
	b.WriteString("means. Example:\n")
	b.WriteString("\n")
	b.WriteString("Audit this repository for dependency drift. Open one PR per finding.\n")
	return []byte(b.String())
}
