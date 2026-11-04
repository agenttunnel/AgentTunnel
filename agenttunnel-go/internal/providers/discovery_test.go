package providers

import (
	"context"
	"testing"
)

func hasModel(list []string, want string) bool {
	for _, m := range list {
		if m == want {
			return true
		}
	}
	return false
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[1mModel\x1b[0m: \x1b[32manthropic/claude-sonnet-4-5\x1b[0m\r\n"
	if got := StripANSI(in); got != "Model: anthropic/claude-sonnet-4-5\n" {
		t.Fatalf("got %q", got)
	}
}

func TestParseSlashModels(t *testing.T) {
	// The shape of `opencode models`: one provider/model slug per line.
	out := "opencode/big-pickle\nopencode-go/claude-haiku-5-5\nopencode-go/deepseek-v4-flash\n"
	got := ParseSlashModels(out)
	want := []string{"opencode/big-pickle", "opencode-go/claude-haiku-5-5", "opencode-go/deepseek-v4-flash"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParsePiModelListTable(t *testing.T) {
	// A sample of real `pi --list-models` output: a fixed-width table whose model
	// column has no slash, plus a duplicate row.
	out := `provider     model                               context  max-out  thinking  images
google       gemini-2.0-flash                    1.0M     8.2K     no        yes
google       gemini-2.5-flash                    1.0M     65.5K    yes       yes
opencode-go  deepseek-v4-flash                   1M       384K     yes       no
opencode-go  deepseek-v4-flash                   1M       384K     yes       no
`
	got := ParsePiModelListTable(out)
	want := []string{
		"google/gemini-2.0-flash",
		"google/gemini-2.5-flash",
		"opencode-go/deepseek-v4-flash",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParsePiModelListTableDropsNotice(t *testing.T) {
	// When no provider is authenticated, pi prints a notice instead of a table.
	out := "No models available. Use /login to log into a provider via OAuth or API key. See:\n" +
		"  /home/user/.pi/docs/providers.md\n" +
		"  /home/user/.pi/docs/models.md\n"
	if got := ParsePiModelListTable(out); len(got) != 0 {
		t.Fatalf("expected no models, got %v", got)
	}
}

func TestCommandDiscovererErrorsWhenBinaryMissing(t *testing.T) {
	d := NewCommandDiscoverer("definitely-not-a-real-binary-xyz", "models")
	if _, err := d.Discover(context.Background()); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestCommandDiscovererReadsStderr(t *testing.T) {
	// `pi --list-models` prints its table on stderr; combined output must see it.
	d := NewCommandDiscoverer("sh", "-c",
		`printf 'provider model context max-out thinking images\ngoogle     gemini-3-flash 1.0M 65.5K yes yes\n' 1>&2`).
		WithParser(ParsePiModelListTable)
	got, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !hasModel(got, "google/gemini-3-flash") {
		t.Fatalf("got %v", got)
	}
}

func TestChainDiscovererFallsBack(t *testing.T) {
	bad := NewCommandDiscoverer("definitely-not-a-real-binary-xyz", "models")
	good := NewCommandDiscoverer("sh", "-c", "printf 'grok/x-1\n'")

	got, err := NewChainDiscoverer(bad, good).Discover(context.Background())
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if !hasModel(got, "grok/x-1") {
		t.Fatalf("got %v", got)
	}
}
