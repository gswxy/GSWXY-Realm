//go:build windows

package main

import (
	"os"
	"syscall"
)

// Windows developer stubs: the daemonization path is only exercised on
// fnOS/Linux; on Windows we run in the foreground.
func procAttr(files []*os.File) *os.ProcAttr {
	return &os.ProcAttr{Files: files}
}

func killPID(pid int, sig syscall.Signal) error {
	return syscall.Errno(1)
}

func alive(pid int) bool { return pid > 0 }
