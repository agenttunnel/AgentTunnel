//go:build windows

package transport

import "os/exec"

// shellInvocation runs the command through cmd.exe.
func shellInvocation(command string) (string, []string) {
	return "cmd", []string{"/C", command}
}

// configureShell leaves the default cancel (kill the shell process). Child
// processes of cmd.exe are not tracked on Windows.
func configureShell(*exec.Cmd) {}
