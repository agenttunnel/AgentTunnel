package providers

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ModelDiscoverer enumerates the models a harness can actually run.
type ModelDiscoverer interface {
	Discover(ctx context.Context) ([]string, error)
}

// CommandDiscoverer runs a non-interactive listing command and parses its output.
//
// It never writes to the process's stdin. Some harness CLIs treat an unknown
// subcommand as a prompt, so probes must be read-only and time-bounded — a wedged
// harness degrades to a missing catalog rather than blocking the registry.
type CommandDiscoverer struct {
	Binary  string
	Args    []string
	Timeout time.Duration
	Parse   func(output string) []string
}

// NewCommandDiscoverer builds a probe for a harness listing command.
func NewCommandDiscoverer(binary string, args ...string) *CommandDiscoverer {
	return &CommandDiscoverer{
		Binary:  binary,
		Args:    args,
		Timeout: 10 * time.Second,
		Parse:   ParseSlashModels,
	}
}

// WithParser returns a copy of the discoverer using a specific parser.
func (d *CommandDiscoverer) WithParser(parse func(string) []string) *CommandDiscoverer {
	c := *d
	c.Parse = parse
	return &c
}

// Discover runs the command and parses its combined output. stderr is included
// because some CLIs (for example `pi --list-models`) print their table there.
func (d *CommandDiscoverer) Discover(ctx context.Context) ([]string, error) {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, d.Binary, d.Args...).CombinedOutput()
	if err != nil {
		return nil, err
	}
	parse := d.Parse
	if parse == nil {
		parse = ParseSlashModels
	}
	return parse(string(out)), nil
}

// ChainDiscoverer tries discoverers in order and returns the first non-empty
// result.
type ChainDiscoverer struct {
	Discoverers []ModelDiscoverer
}

// NewChainDiscoverer composes discoverers, tried in order.
func NewChainDiscoverer(ds ...ModelDiscoverer) *ChainDiscoverer {
	return &ChainDiscoverer{Discoverers: ds}
}

// Discover returns the first non-empty result, or the first error if none
// produced models.
func (c *ChainDiscoverer) Discover(ctx context.Context) ([]string, error) {
	var firstErr error
	for _, d := range c.Discoverers {
		if d == nil {
			continue
		}
		got, err := d.Discover(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(got) > 0 {
			return got, nil
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

var (
	ansiRe       = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)
	slashModelRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*`)
	tokenCountRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KMB]?)$`)
	// Command Code model ids are provider/model slugs or a bare short name;
	// either may carry a ":free" style tier suffix.
	commandCodeModelRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._/-]*(?::[A-Za-z0-9._-]+)?$`)
)

// StripANSI removes terminal escape sequences and stray control characters.
func StripANSI(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 32 {
			return r
		}
		return -1
	}, s)
}

// ParseSlashModels extracts every "provider/model" token from free-form output.
// It is the right parser for `opencode models`, whose rows are one slug per line.
func ParseSlashModels(output string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range slashModelRe.FindAllString(StripANSI(output), -1) {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// ParseModelLines reads one model id per non-empty, non-noise line, for listing
// commands that print a bare slug per line.
func ParseModelLines(output string) []string {
	seen := map[string]bool{}
	var models []string
	for _, raw := range strings.Split(StripANSI(output), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch strings.ToLower(line) {
		case "model", "models", "model id", "available models":
			continue
		}
		if strings.ContainsAny(line, " \t") {
			fields := strings.Fields(line)
			if len(fields) == 0 || !looksLikeModel(fields[0]) {
				continue
			}
			line = fields[0]
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		models = append(models, line)
	}
	return models
}

// ParsePiModelListTable parses the fixed-width table `pi --list-models` prints
// (provider, model, context, max-out, thinking, images) into `provider/model`
// slugs. A row is kept only when both token counts are parseable and both flags
// are booleans, so notices such as "No models available. Use /login ..." are
// dropped rather than read as a model.
func ParsePiModelListTable(output string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.Split(StripANSI(output), "\n") {
		cols := strings.Fields(strings.TrimSpace(raw))
		if len(cols) < 6 {
			continue
		}
		provider, model := cols[0], cols[1]
		if provider == "provider" && model == "model" {
			continue
		}
		if _, ok := parseTokenCount(cols[2]); !ok {
			continue
		}
		if _, ok := parseTokenCount(cols[3]); !ok {
			continue
		}
		if _, ok := parsePiBool(cols[4]); !ok {
			continue
		}
		if _, ok := parsePiBool(cols[5]); !ok {
			continue
		}
		slug := provider + "/" + model
		if seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
	}
	return out
}

// parseTokenCount parses pi's abbreviated counts ("1.0M", "65.5K", "384K").
func parseTokenCount(s string) (int64, bool) {
	m := tokenCountRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	amount, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	mult := float64(1)
	switch strings.ToUpper(m[2]) {
	case "K":
		mult = 1e3
	case "M":
		mult = 1e6
	case "B":
		mult = 1e9
	}
	return int64(amount * mult), true
}

func parsePiBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "true", "y":
		return true, true
	case "no", "false", "n":
		return false, true
	}
	return false, false
}

// ParseCommandCodeModels parses `commandcode --list-models` (also `cmd`). Each
// row is a model id followed by a description. The CLI accepts both a
// provider/model slug and the bare short name after the last "/", and prints both
// forms, so a slash-only parser silently drops half the catalog. Provider
// headings and usage hints are skipped.
func ParseCommandCodeModels(output string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.Split(StripANSI(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) == 0 {
			continue
		}
		id := fields[0]
		if !commandCodeModelRe.MatchString(id) || !strings.ContainsAny(id, "/-._:") {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// ParseCursorModelList parses `cursor-agent --list-models`, whose rows are
// "<slug> - <description>" (for example "gpt-5.3-codex-low - Codex 5.3 Low").
func ParseCursorModelList(output string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.Split(StripANSI(output), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, " - ") {
			continue
		}
		slug := strings.TrimSpace(strings.SplitN(line, " - ", 2)[0])
		if slug == "" || strings.ContainsAny(slug, " \t") || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
	}
	return out
}

func looksLikeModel(s string) bool {
	return strings.ContainsAny(s, "/-._:") && !strings.Contains(s, "=")
}

// ErrNoModels is returned when a probe succeeds but reports nothing.
var ErrNoModels = errors.New("no models reported")
