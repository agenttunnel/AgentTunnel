//go:build !unix

package main

// processAlive conservatively reports a recorded pid as alive where the
// platform cannot probe it with signal 0. The discovery file is then never
// removed by an attach probe; its owner overwrites it atomically on the next
// boot.
func processAlive(pid int) bool { return pid > 0 }
