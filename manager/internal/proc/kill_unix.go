//go:build !windows

package proc

import "syscall"

// signalPID sends a signal to a pid (or its process group when negative).
func signalPID(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
