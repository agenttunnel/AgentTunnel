package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The respawn contract (service.go): a clean exit never restarts the job, a
// failed exit does, and nothing else restarts it. The renderer is the only place
// those keys are written, so the exact blocks are pinned here, not just the
// substrings the argv tests happen to check.

func TestRespawnContractLaunchdKeepAliveOnlyOnFailure(t *testing.T) {
	plist := renderLaunchAgent(goldenSpec)
	block := "<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>"
	if !strings.Contains(plist, block) {
		t.Fatalf("plist must restart only on a non-zero exit, KeepAlive block:\n%s", plist)
	}
	if strings.Contains(plist, "<key>KeepAlive</key>\n\t<true/>") {
		t.Fatal("KeepAlive true restarts on a clean exit: a yield would respawn forever")
	}
}

func TestRespawnContractSystemdRestartsOnFailureOnly(t *testing.T) {
	unit := renderSystemdUnit(goldenSpec)
	var restarts []string
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "Restart=") {
			restarts = append(restarts, line)
		}
	}
	if len(restarts) != 1 || restarts[0] != "Restart=on-failure" {
		t.Fatalf("Restart lines = %q, want exactly [Restart=on-failure]", restarts)
	}
}

func TestRespawnContractTaskRestartsOnFailureOnly(t *testing.T) {
	task := renderTaskXML(goldenSpec)
	if n := strings.Count(task, "<RestartOnFailure>"); n != 1 {
		t.Fatalf("RestartOnFailure blocks = %d, want exactly 1", n)
	}
	// The logon trigger starts the task; no other trigger repeats it. "Trigger>"
	// counts the open and close tags of the one LogonTrigger element only
	// ("<Triggers>" ends in "Triggers>", not "Trigger>").
	if n := strings.Count(task, "<LogonTrigger>"); n != 1 || strings.Count(task, "Trigger>") != 2 {
		t.Fatalf("triggers must be the single logon trigger; LogonTrigger=%d", n)
	}
}

func TestServiceManagerMissingClassification(t *testing.T) {
	bus := "Failed to connect to bus: No medium found"
	cases := []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{name: "no error", err: nil, want: false},
		{name: "binary missing", err: exec.ErrNotFound, want: true},
		{name: "binary missing text", err: errors.New(`exec: "launchctl": executable file not found in $PATH`), want: true},
		{name: "not found text", err: errors.New("not found"), want: true},
		{name: "no user bus", out: bus, err: errors.New("exit status 1"), want: true},
		{name: "no systemd as init", out: "System has not been booted with systemd as init system (PID 1).", err: errors.New("exit status 1"), want: true},
		{name: "launchd refused bootout", out: "Boot-out failed: 3: No such process", err: errors.New("exit status 3"), want: false},
		{name: "bootstrap refused", out: "Bootstrap failed: 5: Input/output error", err: errors.New("exit status 5"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isServiceManagerMissing(tc.out, tc.err); got != tc.want {
				t.Fatalf("isServiceManagerMissing = %v, want %v", got, tc.want)
			}
		})
	}
}

// commandErr folds multi-line tool output onto one line, so the CLI prints a
// single error line and never a dump that reads like a stack.
func TestCommandErrIsOneLine(t *testing.T) {
	err := commandErr("systemctl --user enable", "line one\n\nline two\n", errors.New("exit status 1"))
	if strings.ContainsAny(err.Error(), "\n\r") {
		t.Fatalf("error spans lines: %q", err.Error())
	}
	if want := "systemctl --user enable: exit status 1: line one line two"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

// With the service manager's binary missing, `daemon install` must print one
// clear line and return the distinguishable exit code, so install.sh can warn
// and continue. Every service command is the fake that answers "not found".
func TestDaemonInstallReportsMissingServiceManagerOnOneLine(t *testing.T) {
	// Clear the agent-session guard: a test run from an agent shell would
	// otherwise hit the refusal before reaching the service manager.
	t.Setenv("AT_AGENT_ID", "")
	t.Setenv("AT_ATD", t.TempDir()+"/atd")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	prev := serviceCommands
	serviceCommands = &fakeRunner{reply: func(argv []string) (string, error) {
		return "", fmt.Errorf("exec: %q: executable file not found in $PATH", argv[0])
	}}
	t.Cleanup(func() { serviceCommands = prev })
	prevPause := servicePause
	servicePause = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { servicePause = prevPause })

	var stderr bytes.Buffer
	env := &Env{Out: &stderr, Err: &stderr, HomeDir: t.TempDir()}
	err := runDaemonInstall(context.Background(), env, nil)
	if !errors.Is(err, errServiceManagerUnavailable) {
		t.Fatalf("err = %v, want errServiceManagerUnavailable", err)
	}
	if code := exitCode(env, err); code != exitServiceUnavailable {
		t.Fatalf("exit = %d, want %d", code, exitServiceUnavailable)
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "agenttunnel: service manager unavailable") {
		t.Fatalf("stderr = %q, want one clear line", stderr.String())
	}
}
