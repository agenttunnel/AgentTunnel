package discovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadRemoveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := Info{
		WSURL:     "ws://127.0.0.1:7331/ws",
		HTTPURL:   "http://127.0.0.1:7331",
		Token:     "secret-token",
		PID:       1234,
		Version:   "1.2.3",
		StartedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := Write(dir, in); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The file holds a bearer token: it must never be group/world readable.
	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}

	got, err := Read(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.WSURL != in.WSURL || got.HTTPURL != in.HTTPURL || got.Token != in.Token ||
		got.PID != in.PID || got.Version != in.Version || !got.StartedAt.Equal(in.StartedAt) {
		t.Fatalf("round trip = %+v, want %+v", got, in)
	}

	if err := Remove(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := Read(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read after remove = %v, want not-exist", err)
	}
	// Removing again is a no-op.
	if err := Remove(dir); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestReadRejectsEmptyAndInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"empty":       "",
		"whitespace":  "   \n",
		"garbage":     "{not json",
		"missing url": `{"token":"x"}`,
		"missing tok": `{"wsUrl":"ws://127.0.0.1:1/ws"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(dir); err == nil {
				t.Fatalf("Read accepted %q", body)
			}
		})
	}
}

func TestWriteValidatesRequiredFields(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Info{Token: "t"}); err == nil {
		t.Fatal("Write accepted a missing wsUrl")
	}
	if err := Write(dir, Info{WSURL: "ws://x/ws"}); err == nil {
		t.Fatal("Write accepted a missing token")
	}
}
