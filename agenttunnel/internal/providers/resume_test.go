package providers

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestResumeArgv(t *testing.T) {
	cases := []struct {
		name    string
		harness contracts.Harness
		session string
		want    []string
		ok      bool
	}{
		{"claude", contracts.HarnessClaude, "7f3c-9a", []string{"claude", "--resume", "7f3c-9a"}, true},
		{"codex", contracts.HarnessCodex, "019a2b", []string{"codex", "resume", "019a2b"}, true},
		{"opencode", contracts.HarnessOpenCode, "ses_1Ab", []string{"opencode", "--session", "ses_1Ab"}, true},
		// pi runs with --no-session: nothing to resume, even if an id appears.
		{"pi keeps no session", contracts.HarnessPi, "abc", nil, false},
		{"no session yet", contracts.HarnessClaude, "", nil, false},
		{"shell metacharacters", contracts.HarnessClaude, "x; rm -rf ~", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ResumeArgv(c.harness, c.session)
			if ok != c.ok || len(got) != len(c.want) {
				t.Fatalf("ResumeArgv(%s, %q) = %v, %v; want %v, %v", c.harness, c.session, got, ok, c.want, c.ok)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("ResumeArgv(%s, %q) = %v; want %v", c.harness, c.session, got, c.want)
				}
			}
		})
	}
}

// Each harness reports its run's session id on a frame the translator reads.
func TestTranslatorsReadHarnessSession(t *testing.T) {
	claudeInit := `{"type":"system","subtype":"init","session_id":"claude-run-1"}`
	openCodeStep := `{"type":"step_start","sessionID":"ses_open_1","part":{"type":"step-start"}}`
	codexStart := `{"type":"thread.started","thread_id":"codex-thread-1"}`

	sessionOf := func(evs []Event) string {
		for _, ev := range evs {
			if ev.HarnessSession != "" {
				return ev.HarnessSession
			}
		}
		return ""
	}
	if got := sessionOf(JSONLineTranslator{}.Translate(claudeInit)); got != "claude-run-1" {
		t.Fatalf("claude session = %q", got)
	}
	if got := sessionOf(JSONLineTranslator{}.Translate(openCodeStep)); got != "ses_open_1" {
		t.Fatalf("opencode session = %q", got)
	}
	if got := sessionOf(CodexTranslator{}.Translate(codexStart)); got != "codex-thread-1" {
		t.Fatalf("codex session = %q", got)
	}
}
