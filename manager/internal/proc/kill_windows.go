//go:build windows

package proc

import "syscall"

// Windows developer builds: signals are approximated by terminating the
// process; production targets Linux where real signals exist.
func signalPID(pid int, sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		return forceTerminate(pid)
	}
	return nil
}

func pidAlive(pid int) bool { return pid > 0 }

func forceTerminate(pid int) error {
	return nil
}
