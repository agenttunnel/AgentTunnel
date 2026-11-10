package settings

import (
	"path/filepath"
	"testing"
)

func TestRelayDefaultsOffAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(path, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := s.Relay(); got.Enabled || got.URL != "" {
		t.Fatalf("relay should default off, got %+v", got)
	}

	s.SetRelay(Relay{Enabled: true, URL: "  ws://127.0.0.1:7777  "})
	again, err := Open(path, "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := again.Relay(); !got.Enabled || got.URL != "ws://127.0.0.1:7777" {
		t.Fatalf("relay not persisted: %+v", got)
	}
}
