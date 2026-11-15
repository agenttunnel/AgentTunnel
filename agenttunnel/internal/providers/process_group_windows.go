//go:build windows

package providers

import "os/exec"

// setProcessGroup is a no-op on Windows: job objects are the equivalent, and
// the single-process kill is the best simple trade here.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills the child process.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
