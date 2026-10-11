package logstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Agent ids reach the filesystem as file names, so a hostile-looking id must not
// be able to write outside the store directory.
func TestStoreConfinesAgentIDsToItsDirectory(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	const evil contracts.AgentID = "../../evil"
	if err := s.Append(contracts.LogEntry{AgentID: evil, Kind: contracts.LogText, Text: "x"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	var files []string
	if err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	if rel, err := filepath.Rel(dir, files[0]); err != nil || rel == "" || rel[0] == '.' {
		t.Fatalf("log escaped the store directory: %s", files[0])
	}

	entries, err := s.ReadFile(evil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 1 || entries[0].Text != "x" {
		t.Fatalf("entries = %+v", entries)
	}
}
