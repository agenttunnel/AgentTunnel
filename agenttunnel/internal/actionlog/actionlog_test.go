package actionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLogAppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "actions.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	l.Log("command", map[string]any{"type": "thread.create"})
	l.Log("event", map[string]any{"type": "thread.created"})
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var lines []map[string]any
	for _, raw := range splitLines(data) {
		var rec map[string]any
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("line is not JSON: %v (%s)", err, raw)
		}
		lines = append(lines, rec)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if lines[0]["kind"] != "command" || lines[0]["type"] != "thread.create" {
		t.Fatalf("first record = %+v", lines[0])
	}
	if _, ok := lines[0]["at"]; !ok {
		t.Fatal("record should carry a timestamp")
	}
}

func TestNilLoggerIsSafe(t *testing.T) {
	var l *Logger
	l.Log("command", nil) // must not panic
	if l.Func() != nil {
		t.Fatal("nil logger should yield a nil Func")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close on nil: %v", err)
	}
}

func TestRotatesWhenLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "actions.jsonl")
	l, _ := Open(path)
	l.maxBytes = 200

	for i := 0; i < 20; i++ {
		l.Log("command", map[string]any{"type": "x", "n": i})
	}
	_ = l.Close()

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a rotated backup: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 400 {
		t.Fatalf("live log should have been rotated, size = %d", info.Size())
	}
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	return out
}
