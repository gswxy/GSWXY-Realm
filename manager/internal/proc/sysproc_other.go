//go:build !linux

package proc

import "os/exec"

// Non-Linux developer builds do not need process groups.
func setSysProcAttr(cmd *exec.Cmd) {}
