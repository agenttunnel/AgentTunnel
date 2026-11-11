package deps

import (
	"context"
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"gh version 2.45.0 (2024-03-04)\n", "2.45.0"},
		{"gh version 2.40.1 (2024-01-01)\n", "2.40.1"},
		{"glab 1.40.0 (abcdef)\n", "1.40.0"},
		{"glab version 1.40.0\n", "1.40.0"},
		{"v2.45.0", "2.45.0"},
		{"no version here", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := parseVersion(tc.in); got != tc.want {
			t.Errorf("parseVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.45.0", "2.40.0", 1},
		{"2.40.0", "2.40.0", 0},
		{"2.39.9", "2.40.0", -1},
		{"1.40.0", "1.36.0", 1},
		{"1.36.0", "1.36.0", 0},
		{"1.35.9", "1.36.0", -1},
		{"v2.45.0", "2.40.0", 1},
		{"3.0.0", "2.99.99", 1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

type fakeRun map[string]struct {
	out string
	err error
}

func (f fakeRun) run(_ context.Context, name string) (string, error) {
	r, ok := f[name]
	if !ok {
		return "", errors.New("executable file not found in $PATH")
	}
	return r.out, r.err
}

func TestCheckReportsInstalledVersions(t *testing.T) {
	checker := Checker{Run: fakeRun{
		"gh":   {out: "gh version 2.45.0 (2024-03-04)\n"},
		"glab": {out: "glab version 1.40.0\n"},
	}.run}
	got := checker.Check(context.Background())
	want := []Tool{
		{Name: "gh", Installed: true, Version: "2.45.0", MinVersion: "2.40.0", Status: StatusOK},
		{Name: "glab", Installed: true, Version: "1.40.0", MinVersion: "1.36.0", Status: StatusOK},
	}
	assertTools(t, got, want)
}

func TestCheckFlagsOutdatedVersion(t *testing.T) {
	checker := Checker{Run: fakeRun{
		"gh":   {out: "gh version 2.39.2 (2024-01-01)\n"},
		"glab": {out: "glab 1.35.0 (abcdef)\n"},
	}.run}
	got := checker.Check(context.Background())
	want := []Tool{
		{Name: "gh", Installed: true, Version: "2.39.2", MinVersion: "2.40.0", Status: StatusOutdated},
		{Name: "glab", Installed: true, Version: "1.35.0", MinVersion: "1.36.0", Status: StatusOutdated},
	}
	assertTools(t, got, want)
}

func TestCheckReportsMissingBinary(t *testing.T) {
	checker := Checker{Run: fakeRun{}.run}
	got := checker.Check(context.Background())
	want := []Tool{
		{Name: "gh", MinVersion: "2.40.0", Status: StatusMissing},
		{Name: "glab", MinVersion: "1.36.0", Status: StatusMissing},
	}
	assertTools(t, got, want)
}

func TestCheckUnparseableVersionIsOutdated(t *testing.T) {
	checker := Checker{Run: fakeRun{
		"gh":   {out: "dev build\n"},
		"glab": {out: "glab 1.40.0\n"},
	}.run}
	got := checker.Check(context.Background())
	if len(got) != 2 {
		t.Fatalf("got %d tools, want 2", len(got))
	}
	if !got[0].Installed || got[0].Status != StatusOutdated || got[0].Version != "" {
		t.Errorf("unparseable gh = %+v, want installed outdated with no version", got[0])
	}
	if got[1].Status != StatusOK {
		t.Errorf("glab status = %q, want ok", got[1].Status)
	}
}

func TestCheckToleratesRunErrorWithOutput(t *testing.T) {
	// A binary that exists but exits non-zero must not read as installed.
	checker := Checker{Run: fakeRun{
		"gh":   {out: "gh version 2.45.0\n", err: errors.New("exit 1")},
		"glab": {out: "glab 1.40.0\n"},
	}.run}
	got := checker.Check(context.Background())
	if got[0].Installed || got[0].Status != StatusMissing {
		t.Errorf("failed gh = %+v, want missing", got[0])
	}
}

func assertTools(t *testing.T, got, want []Tool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d tools, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tool %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
