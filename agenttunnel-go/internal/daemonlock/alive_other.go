//go:build !unix

package daemonlock

// processAlive conservatively reports a recorded pid as alive where the
// platform cannot probe it with signal 0. The staleAfter age limit still frees
// a lock left behind by a crashed process.
func processAlive(pid int) bool { return pid > 0 }
