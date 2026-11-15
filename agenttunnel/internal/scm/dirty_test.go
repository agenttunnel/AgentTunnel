package scm

import (
	"context"
	"errors"
	"testing"
)

func TestDirtyFromPorcelain(t *testing.T) {
	cases := map[string]struct {
		out  string
		want bool
	}{
		"clean":      {out: "", want: false},
		"whitespace": {out: "\n", want: false},
		"modified":   {out: " M internal/scm/board.go\n", want: true},
		"untracked":  {out: "?? notes.md\n", want: true},
		"staged":     {out: "A  new.go\n", want: true},
	}
	for name, tc := range cases {
		if got := dirtyFromPorcelain([]byte(tc.out)); got != tc.want {
			t.Fatalf("%s: dirtyFromPorcelain(%q) = %v, want %v", name, tc.out, got, tc.want)
		}
	}
}

func TestClientDirtyReadsGitStatusInWorkdir(t *testing.T) {
	var gotDir, gotName string
	var gotArgs []string
	c := Client{Run: func(_ context.Context, dir, name string, args ...string) ([]byte, string, error) {
		gotDir, gotName, gotArgs = dir, name, args
		return []byte(" M a.go\n"), "", nil
	}}
	if !c.Dirty(context.Background(), "/wt/a") {
		t.Fatal("Dirty = false, want true for porcelain output with a change")
	}
	if gotDir != "/wt/a" || gotName != "git" || len(gotArgs) != 2 || gotArgs[0] != "status" || gotArgs[1] != "--porcelain" {
		t.Fatalf("ran %s %v in %q, want git status --porcelain in /wt/a", gotName, gotArgs, gotDir)
	}
}

func TestClientDirtyFailsSoftToClean(t *testing.T) {
	c := Client{Run: func(context.Context, string, string, ...string) ([]byte, string, error) {
		return []byte(" M a.go\n"), "not a git repository", errors.New("exit 128")
	}}
	if c.Dirty(context.Background(), "/wt/gone") {
		t.Fatal("Dirty = true after git failed, want false (unknown reads as clean)")
	}
}
