package providers

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These probe the real harness CLIs. They are opt-in — run with
// AGENTTUNNEL_LIVE_TESTS=1 — because they depend on the CLIs being installed and
// authenticated and can call out to the network, which makes them unsuitable for
// a deterministic `go test ./...`.

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTTUNNEL_LIVE_TESTS") != "1" {
		t.Skip("set AGENTTUNNEL_LIVE_TESTS=1 to probe installed harness CLIs")
	}
}

const liveProbeTimeout = 30 * time.Second

func TestPiDiscoveryAgainstInstalledCLI(t *testing.T) {
	requireLive(t)
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi not installed")
	}
	d := NewCommandDiscoverer("pi", "--list-models").WithParser(ParsePiModelListTable)
	d.Timeout = liveProbeTimeout

	got, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected models from the installed pi CLI")
	}
	for _, m := range got {
		if !strings.Contains(m, "/") {
			t.Fatalf("expected provider/model slugs, got %q", m)
		}
	}
	t.Logf("pi reported %d models, e.g. %s", len(got), got[0])
}

func TestOpenCodeDiscoveryAgainstInstalledCLI(t *testing.T) {
	requireLive(t)
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not installed")
	}
	d := NewCommandDiscoverer("opencode", "models")
	d.Timeout = liveProbeTimeout

	got, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected models from the installed opencode CLI")
	}
	t.Logf("opencode reported %d models, e.g. %s", len(got), got[0])
}

func TestCommandCodeDiscoveryAgainstInstalledCLI(t *testing.T) {
	requireLive(t)
	bin := ""
	for _, candidate := range []string{"command-code", "cmd"} {
		if _, err := exec.LookPath(candidate); err == nil {
			bin = candidate
			break
		}
	}
	if bin == "" {
		t.Skip("command-code/cmd not installed")
	}
	d := NewCommandDiscoverer(bin, "--list-models").WithParser(ParseCommandCodeModels)
	d.Timeout = liveProbeTimeout

	got, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected models from the installed Command Code CLI")
	}
	t.Logf("%s reported %d models, e.g. %s", bin, len(got), got[0])
}

func TestCursorDiscoveryAgainstInstalledCLI(t *testing.T) {
	requireLive(t)
	if _, err := exec.LookPath("cursor-agent"); err != nil {
		t.Skip("cursor-agent not installed")
	}
	d := NewCommandDiscoverer("cursor-agent", "--list-models").WithParser(ParseCursorModelList)
	d.Timeout = liveProbeTimeout

	got, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected models from the installed cursor-agent CLI")
	}
	t.Logf("cursor-agent reported %d models, e.g. %s", len(got), got[0])
}
