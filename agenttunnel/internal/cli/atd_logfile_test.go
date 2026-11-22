package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// An autostarted daemon must rotate its own log: spawnDaemon hands it the
// atd.log path through --log-file. The fake atd records its argv, so the test
// checks the flag the real daemon would receive.
func TestSpawnDaemonPassesLogFileSoTheDaemonRotatesIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake atd is a shell script")
	}
	dir := t.TempDir()
	argsOut := filepath.Join(t.TempDir(), "argv")
	fake := filepath.Join(t.TempDir(), "atd")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_OUT\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AT_ATD", fake)
	t.Setenv("ARGS_OUT", argsOut)

	if err := spawnDaemon(dir); err != nil {
		t.Fatalf("spawnDaemon: %v", err)
	}
	var argv []byte
	err := waitFor(context.Background(), 10*time.Second, func() bool {
		data, _ := os.ReadFile(argsOut)
		argv = data
		return bytes.Contains(data, []byte("\n"))
	})
	if err != nil {
		t.Fatalf("fake atd never recorded its argv: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	want := []string{"--port", "0", "--home-dir", dir, "--log-file", atdLogPath(dir)}
	if strings.Join(lines, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("atd argv = %q, want %q", lines, want)
	}
}
