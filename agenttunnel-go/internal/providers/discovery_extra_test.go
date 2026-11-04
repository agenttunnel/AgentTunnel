package providers

import "testing"

func TestParseCursorModelList(t *testing.T) {
	// A sample of real `cursor-agent --list-models` output.
	out := `Available models

auto - Auto (current, default)
gpt-5.3-codex-low - Codex 5.3 Low
gpt-5.3-codex-low-fast - Codex 5.3 Low Fast
gpt-5.3-codex - Codex 5.3
`
	got := ParseCursorModelList(out)
	want := []string{"auto", "gpt-5.3-codex-low", "gpt-5.3-codex-low-fast", "gpt-5.3-codex"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParseCommandCodeModels(t *testing.T) {
	// A sample of real `cmd --list-models` output: section headings plus rows.
	out := `Available models  ·  87 models

Open Source

deepseek/deepseek-v4-pro               hybrid-attention long-context reasoning
deepseek/deepseek-v4-flash             fast hybrid-attention reasoning (default)
moonshotai/kimi-k3                     long-horizon coding & knowledge work with 1M context
`
	got := ParseSlashModels(out)
	for _, want := range []string{
		"deepseek/deepseek-v4-pro",
		"deepseek/deepseek-v4-flash",
		"moonshotai/kimi-k3",
	} {
		if !hasModel(got, want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
}
