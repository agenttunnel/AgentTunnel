package prefs

import (
	"os"
	"path/filepath"
	"testing"
)

func set(v string) *string { return &v }

func TestPreferencesPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	s := Open(path)
	if err := s.Apply(map[string]*string{
		"at.theme":      set("nord"),
		"at.threadView": set("board"),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := Open(path).Snapshot()
	if got["at.theme"] != "nord" || got["at.threadView"] != "board" {
		t.Fatalf("snapshot = %+v, want the saved values", got)
	}
}

func TestApplyDeletesNilKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	s := Open(path)
	if err := s.Apply(map[string]*string{
		"at.theme":  set("nord"),
		"at.layout": set(`{"sidebarW":300}`),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := s.Apply(map[string]*string{"at.theme": nil}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got := Open(path).Snapshot()
	if _, ok := got["at.theme"]; ok {
		t.Fatalf("deleted key came back: %+v", got)
	}
	if got["at.layout"] == "" {
		t.Fatalf("unrelated key was dropped: %+v", got)
	}
}

func TestMalformedFileReadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Open(path).Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot = %+v, want empty", got)
	}
}

func TestInvalidEntriesInFileAreIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	data := `{"at.theme":"nord","":"bad","at.bad\u0000key":"x"}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Open(path).Snapshot()
	if len(got) != 1 || got["at.theme"] != "nord" {
		t.Fatalf("snapshot = %+v, want only at.theme", got)
	}
}
