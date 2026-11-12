package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func init() {
	Register(Command{
		Name:    "open",
		Summary: "Open a thread in the AgentTunnel desktop app",
		Usage:   "agenttunnel open <thread>",
		Run:     runOpen,
	})
}

// desktopBinary is the desktop client on PATH (the app bundle's launcher link).
const desktopBinary = "agenttunnel-desktop"

// desktopInstallHint is the error when the desktop client is not installed.
const desktopInstallHint = "agenttunnel-desktop is not on PATH; install the AgentTunnel desktop app " +
	"(the release installer links it into ~/.local/bin, or run desktop/build-app.sh --install from a checkout)"

// startDesktop launches the desktop client without waiting for it: the window
// outlives the command. Tests replace it.
var startDesktop = func(path string, args ...string) error {
	cmd := exec.Command(path, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// lookDesktop finds the desktop client on PATH. It runs before any daemon
// contact: a missing desktop must not autostart a daemon.
func lookDesktop() (string, error) {
	path, err := exec.LookPath(desktopBinary)
	if err != nil {
		return "", errors.New(desktopInstallHint)
	}
	return path, nil
}

func runOpen(ctx context.Context, env *Env, args []string) error {
	var ref string
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "-"):
			return usageError("unknown flag %q (usage: agenttunnel open <thread>)", arg)
		case ref != "":
			return usageError("usage: agenttunnel open <thread>")
		default:
			ref = arg
		}
	}
	if ref == "" {
		return usageError("usage: agenttunnel open <thread>")
	}
	path, err := lookDesktop()
	if err != nil {
		return err
	}
	c, _, err := env.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	row, err := openThreadInDesktop(ctx, c, ref, path, startDesktop)
	if err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(map[string]string{"threadId": row.ID, "title": row.Title})
	}
	fmt.Fprintf(env.Out, "Opening %s in AgentTunnel desktop\n", row.ID)
	return nil
}

// openThreadInDesktop resolves ref against the daemon and asks the desktop
// client to show that thread. A running app focuses it over the daemon; a
// closed one boots onto it.
func openThreadInDesktop(ctx context.Context, c cliCaller, ref, path string, start func(string, ...string) error) (threadListRow, error) {
	row, err := resolveThreadRow(ctx, c, ref)
	if err != nil {
		return threadListRow{}, err
	}
	if err := start(path, "--thread", row.ID); err != nil {
		return threadListRow{}, fmt.Errorf("start %s: %w", desktopBinary, err)
	}
	return row, nil
}
