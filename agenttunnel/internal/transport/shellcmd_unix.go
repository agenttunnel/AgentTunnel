//go:build !windows

package transport

import (
	"os"
	"os/exec"
	"syscall"
)

// shellInvocation runs the command through the user's shell, falling back to
// sh when SHELL is unset.
func shellInvocation(command string) (string, []string) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	return sh, []string{"-c", command}
}

// configureShell gives the command its own process group so cancelling or
// timing out kills the whole pipeline, not just the shell.
func configureShell(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
