package providers

import "github.com/agenttunnel/agenttunnel-go/internal/contracts"

// ResumeArgv is the harness CLI invocation that continues a run from its
// session id, in the run's working directory. It reports false for a harness
// that keeps no resumable session (pi runs with --no-session), or for an id
// that is not a plain token. The id is copied into a shell line, so only
// letters, digits, '-' and '_' pass.
func ResumeArgv(h contracts.Harness, session string) ([]string, bool) {
	if !plainToken(session) {
		return nil, false
	}
	switch h {
	case contracts.HarnessClaude:
		return []string{"claude", "--resume", session}, true
	case contracts.HarnessCodex:
		return []string{"codex", "resume", session}, true
	case contracts.HarnessOpenCode:
		return []string{"opencode", "--session", session}, true
	}
	return nil, false
}

func plainToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
