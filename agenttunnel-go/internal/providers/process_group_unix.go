//go:build !windows

package providers

import (
	"os/exec"
	"syscall"
)

// setProcessGroup detaches the child into its own process group so the whole
// tree — a harness CLI spawns tools of its own — can be torn down together.
// Without it, cancelling kills only the direct child and its orphaned
// grandchildren keep the output pipes open forever.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the child and everything it spawned.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
