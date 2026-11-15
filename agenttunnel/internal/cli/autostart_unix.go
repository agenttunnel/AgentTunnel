//go:build !windows

package cli

import "syscall"

// detachedSysProcAttr starts the daemon in its own session, so it survives the
// CLI exiting and has no controlling terminal.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
