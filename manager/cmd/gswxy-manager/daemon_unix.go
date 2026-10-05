//go:build !windows

package main

import (
	"os"
	"syscall"
)

// detachAttr and signal helpers for Unix (fnOS production target).
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func procAttr(files []*os.File) *os.ProcAttr {
	return &os.ProcAttr{Files: files, Sys: detachAttr()}
}

func killPID(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}
