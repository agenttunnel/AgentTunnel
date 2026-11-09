package providers

import (
	"reflect"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

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
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseCommandCodeModels(t *testing.T) {
	// A sample of real `command-code --list-models` output: provider headings,
	// slash slugs, bare short names, usage hints, and a docs footer.
	out := `Available models  ·  87 models

Open Source

deepseek/deepseek-v4-pro               hybrid-attention long-context reasoning
deepseek/deepseek-v4-flash             fast hybrid-attention reasoning (default)
inclusionai/ling-3.1-flash:free        FREE hybrid-reasoning MoE for coding

Anthropic

claude-sonnet-5-5                      best combo of speed & intelligence (recommended)
claude-haiku-4-5                       prev Haiku, still fast for quick tasks

OpenAI

gpt-5.5                                latest frontier model for general complex work

Pass the full id, or just the short name after the last "/":
cmd --model moonshotai/kimi-k2.5

Docs:  https://commandcode.ai/docs/reference/cli/models
`
	got := ParseCommandCodeModels(out)
	want := []string{
		"deepseek/deepseek-v4-pro",
		"deepseek/deepseek-v4-flash",
		"inclusionai/ling-3.1-flash:free",
		"claude-sonnet-5-5",
		"claude-haiku-4-5",
		"gpt-5.5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuiltinRegistryProbesCommandCodeWithListModels(t *testing.T) {
	disc, ok := BuiltinRegistry().Discoverer(contracts.HarnessCommandCode)
	if !ok {
		t.Fatal("command-code has no discoverer")
	}
	chain, ok := disc.(*ChainDiscoverer)
	if !ok {
		t.Fatalf("command-code discoverer = %T, want *ChainDiscoverer", disc)
	}
	for i, want := range []string{"command-code", "cmd"} {
		if i >= len(chain.Discoverers) {
			t.Fatalf("chain has %d discoverers, want at least %d", len(chain.Discoverers), i+1)
		}
		cmd, ok := chain.Discoverers[i].(*CommandDiscoverer)
		if !ok {
			t.Fatalf("discoverer %d = %T, want *CommandDiscoverer", i, chain.Discoverers[i])
		}
		if cmd.Binary != want {
			t.Errorf("discoverer %d binary = %q, want %q", i, cmd.Binary, want)
		}
		if !reflect.DeepEqual(cmd.Args, []string{"--list-models"}) {
			t.Errorf("discoverer %d args = %v, want [--list-models]", i, cmd.Args)
		}
	}
}
