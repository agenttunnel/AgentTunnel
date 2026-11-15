// Package gitguard keeps git state changes with the team lead. Subagents get a
// `git` shim first on PATH that passes read-only git through and refuses every
// other subcommand. The shim is a guardrail for the ordinary `git` invocation,
// not a sandbox: a child that calls git by absolute path bypasses it, so the
// role prompts remain the primary control.
package gitguard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// shimDir is created once per daemon process, outside the StateDir, and holds
// only the generated `git` script. It is shared by every subagent spawn.
var shimDir = sync.OnceValues(func() (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("git guard needs a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "agenttunnel-git-")
	if err != nil {
		return "", err
	}
	if err := writeShim(dir, realGit); err != nil {
		return "", err
	}
	return dir, nil
})

// Env returns the environment for a non-lead agent: AT_GIT_POLICY enables the
// shim, and PATH puts it first. It returns nil when no shim can be built (no git
// on PATH, or Windows); the role prompts still forbid state changes there.
// Callers append it after the spawn env, and the last value of a key wins.
func Env() []string {
	dir, err := shimDir()
	if err != nil {
		return nil
	}
	return []string{
		"AT_GIT_POLICY=readonly",
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
}

// writeShim writes the git script into dir. The real binary path is baked in,
// so the shim never resolves back to itself.
func writeShim(dir, realGit string) error {
	path := filepath.Join(dir, "git")
	return os.WriteFile(path, []byte(shimScript(realGit)), 0o755)
}

// shimScript passes everything through unless AT_GIT_POLICY=readonly. Under that
// policy it skips global options (-C, -c, --git-dir, ...) to find the
// subcommand, allows the read-only list, and refuses anything else.
func shimScript(realGit string) string {
	quoted := "'" + strings.ReplaceAll(realGit, "'", `'\''`) + "'"
	return `#!/bin/sh
# AgentTunnel git guard: only the team lead changes git state.
[ "$AT_GIT_POLICY" = readonly ] || exec ` + quoted + ` "$@"
sub=""
skip=0
for a in "$@"; do
  if [ "$skip" = 1 ]; then skip=0; continue; fi
  case "$a" in
    -C|-c|--git-dir|--work-tree|--namespace) skip=1 ;;
    -*) ;;
    *) sub="$a"; break ;;
  esac
done
case "$sub" in
  ""|status|diff|log|show|rev-parse|ls-files|ls-tree|grep|blame|cat-file|describe|shortlog|rev-list|show-ref|name-rev|version|help)
    exec ` + quoted + ` "$@" ;;
esac
echo "git $sub is reserved for the team lead. Agents may run read-only git (status, diff, log, show, ...)." >&2
exit 1
`
}
