package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Finder-launched app gets a minimal PATH. Command Code is installed by
// vite-plus into ~/.local/share/vite-plus/bin, so EnrichPATH must add it or
// discovery and dispatch both fail to resolve `commandcode`.
func TestEnrichPATHAddsViteplusBin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	EnrichPATH()

	want := filepath.Join(home, ".local", "share", "vite-plus", "bin")
	if !strings.Contains(os.Getenv("PATH"), want) {
		t.Fatalf("PATH = %q, want it to contain %q", os.Getenv("PATH"), want)
	}
}
