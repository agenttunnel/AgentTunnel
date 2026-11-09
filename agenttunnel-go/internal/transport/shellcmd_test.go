package transport

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipWithoutPOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixtures use POSIX sh syntax")
	}
}

func runForTest(t *testing.T, command string, mutate func(*shellRun)) shellResult {
	t.Helper()
	run := shellRun{Dir: t.TempDir(), Command: command, MaxBytes: shellOutputCap, Timeout: shellTimeout}
	if mutate != nil {
		mutate(&run)
	}
	res, err := runShell(context.Background(), run)
	if err != nil {
		t.Fatalf("runShell(%q): %v", command, err)
	}
	return res
}

func TestRunShellReportsOutputAndExitCode(t *testing.T) {
	skipWithoutPOSIXShell(t)
	res := runForTest(t, "echo hi; exit 3", nil)
	if res.Output != "hi\n" {
		t.Fatalf("output = %q, want %q", res.Output, "hi\n")
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d, want 3", res.ExitCode)
	}
	if res.TimedOut || res.Stopped || res.Truncated {
		t.Fatalf("unexpected flags: %+v", res)
	}
}

func TestRunShellMergesStderr(t *testing.T) {
	skipWithoutPOSIXShell(t)
	res := runForTest(t, "echo out; echo err >&2", nil)
	if !strings.Contains(res.Output, "out") || !strings.Contains(res.Output, "err") {
		t.Fatalf("output = %q, want both stdout and stderr", res.Output)
	}
}

func TestRunShellTruncatesAtCap(t *testing.T) {
	skipWithoutPOSIXShell(t)
	res := runForTest(t, "printf '%0100d' 0", func(r *shellRun) { r.MaxBytes = 10 })
	if len(res.Output) != 10 {
		t.Fatalf("kept %d bytes, want 10", len(res.Output))
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0 — truncation must not change the exit", res.ExitCode)
	}
}

func TestRunShellRunsInDirectory(t *testing.T) {
	skipWithoutPOSIXShell(t)
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	res := runForTest(t, "pwd -P", func(r *shellRun) { r.Dir = dir })
	if got := strings.TrimSpace(res.Output); got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
}

func TestRunShellStripsAgentEnvironment(t *testing.T) {
	skipWithoutPOSIXShell(t)
	t.Setenv("AT_TOKEN", "secret")
	t.Setenv("AT_AGENT_ID", "agent-1")
	res := runForTest(t, `echo "[${AT_TOKEN-unset}][${AT_AGENT_ID-unset}]"`, nil)
	if got := strings.TrimSpace(res.Output); got != "[unset][unset]" {
		t.Fatalf("AT_* leaked into command env: %q", got)
	}
}

func TestRunShellStdinIsClosed(t *testing.T) {
	skipWithoutPOSIXShell(t)
	// With stdin on /dev/null, read hits EOF at once instead of hanging.
	res := runForTest(t, "if read -r x; then echo got; else echo eof; fi", nil)
	if got := strings.TrimSpace(res.Output); got != "eof" {
		t.Fatalf("stdin output = %q, want eof", got)
	}
}

func TestRunShellTimeoutKillsCommand(t *testing.T) {
	skipWithoutPOSIXShell(t)
	// The timeout is the behaviour under test, not a synchronisation sleep.
	res := runForTest(t, "sleep 30; echo done", func(r *shellRun) { r.Timeout = 50 * time.Millisecond })
	if !res.TimedOut {
		t.Fatalf("TimedOut = false, result %+v", res)
	}
	if res.Stopped {
		t.Fatal("a timeout must not be reported as Stopped")
	}
	if strings.Contains(res.Output, "done") {
		t.Fatal("command ran to completion despite the timeout")
	}
}

func TestRunShellCancelStopsCommand(t *testing.T) {
	skipWithoutPOSIXShell(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	done := make(chan shellResult, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := runShell(ctx, shellRun{
			Dir: t.TempDir(), Command: "sleep 30; echo done",
			MaxBytes: shellOutputCap, Timeout: shellTimeout,
			Started: func() { close(started) },
		})
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()

	<-started
	cancel()

	select {
	case res := <-done:
		if !res.Stopped {
			t.Fatalf("Stopped = false, result %+v", res)
		}
		if strings.Contains(res.Output, "done") {
			t.Fatal("command ran to completion despite cancel")
		}
	case err := <-errs:
		t.Fatalf("runShell: %v", err)
	}
}

func TestShellMessageFencesAndStatus(t *testing.T) {
	cases := []struct {
		name    string
		command string
		res     shellResult
		want    []string
	}{
		{
			name:    "success",
			command: "echo hi",
			res:     shellResult{Output: "hi\n", ExitCode: 0},
			want:    []string{"```sh\n$ echo hi\nhi\n```", "exit 0"},
		},
		{
			name:    "failure keeps exit code",
			command: "false",
			res:     shellResult{ExitCode: 1},
			want:    []string{"exit 1"},
		},
		{
			name:    "timeout",
			command: "sleep 9",
			res:     shellResult{ExitCode: -1, TimedOut: true},
			want:    []string{"timed out after"},
		},
		{
			name:    "stopped",
			command: "sleep 9",
			res:     shellResult{ExitCode: -1, Stopped: true},
			want:    []string{"stopped"},
		},
		{
			name:    "truncated is flagged",
			command: "yes",
			res:     shellResult{Output: "y\n", ExitCode: 0, Truncated: true},
			want:    []string{"output truncated"},
		},
		{
			name:    "backticks in output cannot close the fence",
			command: "cat f.md",
			res:     shellResult{Output: "```\ninside\n```\n", ExitCode: 0},
			want:    []string{"````sh\n", "\n````\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shellMessage(tc.command, tc.res)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("message %q does not contain %q", got, w)
				}
			}
		})
	}
}
