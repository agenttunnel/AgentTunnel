//go:build unix

package main

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid names a live process. Signal 0 performs the
// permission and existence checks without delivering anything; EPERM means the
// process exists but belongs to another user.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
