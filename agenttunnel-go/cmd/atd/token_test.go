package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveTokenPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "from-flag-file")
	if err := os.WriteFile(file, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		flagTok  string
		flagFile string
		env      string
		want     string
		origin   string
	}{
		{"flag beats everything", "flag-token", file, "env-token", "flag-token", "--token"},
		{"token file beats env", "", file, "env-token", "file-token", "--token-file"},
		{"env beats state dir", "", "", "env-token", "env-token", "AT_DAEMON_TOKEN"},
		{"whitespace env is unset", "", "", "  \n", "", "state dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := resolveToken(tc.flagTok, tc.flagFile, tc.env, dir)
			if err != nil {
				t.Fatalf("resolveToken: %v", err)
			}
			if tc.want != "" && src.Value != tc.want {
				t.Fatalf("token = %q, want %q", src.Value, tc.want)
			}
			if src.Origin != tc.origin {
				t.Fatalf("origin = %q, want %q", src.Origin, tc.origin)
			}
		})
	}
}

func TestResolveTokenFileIsTrimmed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("  abc123\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := resolveToken("", file, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if src.Value != "abc123" {
		t.Fatalf("token = %q, want trimmed abc123", src.Value)
	}
	if src.Path != file {
		t.Fatalf("path = %q, want %q", src.Path, file)
	}
}

func TestResolveTokenEmptyFileFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveToken("", file, "", t.TempDir()); err == nil {
		t.Fatal("empty token file must fail, not run with an empty token")
	}
}

// The state-dir token is generated once and reused on every later start, so
// clients keep a working token across restarts.
func TestResolveTokenStateDirIsStableAcrossRestarts(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")

	first, err := resolveToken("", "", "", stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Value == "" {
		t.Fatal("generated token is empty")
	}
	wantPath := filepath.Join(stateDir, tokenFileName)
	if first.Path != wantPath {
		t.Fatalf("path = %q, want %q", first.Path, wantPath)
	}

	second, err := resolveToken("", "", "", stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Value != first.Value {
		t.Fatalf("token changed across restarts: %q then %q", first.Value, second.Value)
	}
}

func TestResolveTokenCreatesFileOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	path := filepath.Join(t.TempDir(), "nested", "token")
	if _, err := resolveToken("", path, "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("token file mode = %o, want 600", mode)
	}
}

// An explicit --token-file that does not exist yet is created, then reused.
func TestResolveTokenExplicitFileCreatedThenReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atd.token")
	first, err := resolveToken("", path, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveToken("", path, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if first.Value == "" || first.Value != second.Value {
		t.Fatalf("explicit token file not stable: %q then %q", first.Value, second.Value)
	}
}
