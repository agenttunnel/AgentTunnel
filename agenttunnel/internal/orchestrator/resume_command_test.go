package orchestrator

import "testing"

func TestShellQuoteSurvivesQuotes(t *testing.T) {
	cases := map[string]string{
		"/Users/me/proj.worktrees/x": "'/Users/me/proj.worktrees/x'",
		"/tmp/it's here":             `'/tmp/it'\''s here'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Fatalf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
