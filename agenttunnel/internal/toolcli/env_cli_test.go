package toolcli

import (
	"context"
	"os"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// withStdin feeds content to os.Stdin for the duration of the test, the way a
// piped `tool write-file` receives its file.
func withStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
}

func TestToolEnvCLIWriteReadAndShell(t *testing.T) {
	eng, _ := newCLIServer(t)
	ctx := context.Background()
	t.Setenv("AT_AGENT_ID", "agent-orchestrator")
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c-t1", Type: contracts.CmdThreadCreate, ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Dispatch(ctx, contracts.Command{ID: "r-t1", Type: contracts.CmdThreadRename, ThreadID: "t1", Title: "t1"}); err != nil {
		t.Fatal(err)
	}

	// Content piped on stdin is the file's whole text, when --stdin asks for it.
	withStdin(t, "line one\nline two\n")
	if code := Run([]string{"write-file", "--path", "notes/a.txt", "--stdin"}); code != 0 {
		t.Fatalf("write-file from stdin exit = %d, want 0", code)
	}
	out := captureStdout(t, func() {
		if code := Run([]string{"read-file", "--path", "notes/a.txt"}); code != 0 {
			t.Errorf("read-file exit = %d, want 0", code)
		}
	})
	if out != "line one\nline two\n" {
		t.Fatalf("read-file printed %q, want the file as written", out)
	}

	// --text wins over stdin; a shell prints its output then the exit code, and
	// its call succeeds even though the command failed.
	if code := Run([]string{"write-file", "--path", "notes/b.txt", "--text", "hello"}); code != 0 {
		t.Fatalf("write-file --text exit = %d, want 0", code)
	}
	out = captureStdout(t, func() {
		if code := Run([]string{"shell", "--command", "cat notes/b.txt; exit 4"}); code != 0 {
			t.Errorf("shell exit = %d, want 0 (the call ran; the command failed)", code)
		}
	})
	if out != "hello\nexit: 4\n" {
		t.Fatalf("shell printed %q, want output then exit line", out)
	}

	if code := Run([]string{"read-file", "--path", "notes/missing.txt"}); code != 1 {
		t.Fatalf("read-file of a missing file exit = %d, want 1", code)
	}
	if code := Run([]string{"read-file"}); code != 1 {
		t.Fatalf("read-file with no path exit = %d, want 1", code)
	}
}

// write-file reads stdin only on --stdin. Without --text or --stdin it refuses
// at once, so a call never waits on a pipe that nothing closes. This pipe is
// left open on purpose: a reader that tried to drain it would hang the test.
func TestToolEnvCLIWriteFileNeedsTextOrStdin(t *testing.T) {
	newCLIServer(t)
	t.Setenv("AT_AGENT_ID", "agent-orchestrator")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	defer r.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })

	if code := Run([]string{"write-file", "--path", "a.txt"}); code != 2 {
		t.Fatalf("write-file with neither --text nor --stdin exit = %d, want 2", code)
	}
	if code := Run([]string{"write-file", "--path", "a.txt", "--text", "x", "--stdin"}); code != 2 {
		t.Fatalf("write-file with both --text and --stdin exit = %d, want 2", code)
	}
}

func TestToolEnvCLIRefusedForSubagents(t *testing.T) {
	newCLIServer(t)
	// newCLIServer sets the caller to a subagent; a subagent may not touch the
	// environment, so the server's refusal exits non-zero.
	if code := Run([]string{"read-file", "--path", "a.txt"}); code != 1 {
		t.Fatalf("subagent read-file exit = %d, want 1", code)
	}
	if code := Run([]string{"shell", "--command", "true"}); code != 1 {
		t.Fatalf("subagent shell exit = %d, want 1", code)
	}
}
