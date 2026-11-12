//go:build windows

package cli

import "syscall"

const (
	// DETACHED_PROCESS: no console window and no inherited console.
	detachedProcess = 0x00000008
	// CREATE_NEW_PROCESS_GROUP: the daemon is not signalled with the CLI's group.
	createNewProcessGroup = 0x00000200
)

// detachedSysProcAttr starts the daemon detached from the CLI's console and
// process group.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}
