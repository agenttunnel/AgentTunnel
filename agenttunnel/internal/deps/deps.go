// Package deps reports whether the forge CLIs that AgentTunnel shells out to
// are installed and new enough for the flags the PR tab uses. Probing is a
// local `<bin> --version` call: no credentials, no network, no daemon state.
package deps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Status is a tool's compatibility verdict.
type Status string

const (
	StatusOK       Status = "ok"
	StatusOutdated Status = "outdated"
	StatusMissing  Status = "missing"
)

// Tool is one dependency as the settings screen shows it.
type Tool struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	// Version is the parsed version, empty when the tool is missing or its
	// output could not be read.
	Version    string `json:"version,omitempty"`
	MinVersion string `json:"minVersion"`
	Status     Status `json:"status"`
}

// Requirement is a CLI and the oldest version whose flags the daemon relies on.
type Requirement struct {
	Name string
	Min  string
}

// Required is what the PR tab calls. gh gained `pr list --json` fields we read
// in 2.40.0; glab gained `mr view --output json` / `mr list --output json`
// in 1.36.0.
var Required = []Requirement{
	{Name: "gh", Min: "2.40.0"},
	{Name: "glab", Min: "1.36.0"},
}

// Probe reads the version of every required CLI with the real process runner.
func Probe(ctx context.Context) []Tool {
	return Checker{Run: runVersion}.Check(ctx)
}

// Checker holds the process seam so tests can script the CLIs.
type Checker struct {
	// Run executes `<name> --version` and returns its output. A non-nil error
	// means the binary could not be run, which reads as missing.
	Run func(ctx context.Context, name string) (string, error)
}

// Check probes each required CLI and orders the result like Required.
func (c Checker) Check(ctx context.Context) []Tool {
	out := make([]Tool, 0, len(Required))
	for _, req := range Required {
		out = append(out, c.check(ctx, req))
	}
	return out
}

func (c Checker) check(ctx context.Context, req Requirement) Tool {
	tool := Tool{Name: req.Name, MinVersion: req.Min, Status: StatusMissing}
	stdout, err := c.Run(ctx, req.Name)
	if err != nil {
		return tool
	}
	tool.Installed = true
	tool.Version = parseVersion(stdout)
	if tool.Version != "" && compareVersions(tool.Version, req.Min) >= 0 {
		tool.Status = StatusOK
		return tool
	}
	// An unreadable version is not a confirmed minimum either: treat it as
	// outdated so the UI prompts a reinstall/upgrade instead of claiming ok.
	tool.Status = StatusOutdated
	return tool
}

const versionTimeout = 5 * time.Second

func runVersion(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, "--version")
	// `--version` on these CLIs may check for updates; keep it offline.
	cmd.Env = append(os.Environ(), "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// versionRE finds a bare semver in version output. It is deliberately one
// pattern: `gh version 2.45.0 (2024-03-04)`, `glab 1.40.0 (...)`, and
// `glab version 1.40.0` all expose the same first triplet.
var versionRE = regexp.MustCompile(`(?:^|[^\d])(\d+)\.(\d+)\.(\d+)`)

// parseVersion returns the first dotted version in s, or "" when there is none.
func parseVersion(s string) string {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return fmt.Sprintf("%s.%s.%s", m[1], m[2], m[3])
}

// compareVersions orders two dotted versions numerically. Components beyond
// the third are ignored; unparseable sides compare equal.
func compareVersions(a, b string) int {
	pa, okA := numericParts(a)
	pb, okB := numericParts(b)
	if !okA || !okB {
		return 0
	}
	for i := 0; i < 3; i++ {
		switch {
		case pa[i] < pb[i]:
			return -1
		case pa[i] > pb[i]:
			return 1
		}
	}
	return 0
}

func numericParts(v string) ([3]int, bool) {
	var out [3]int
	fields := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(fields) < 3 {
		return out, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
