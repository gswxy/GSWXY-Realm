//go:build linux

package proc

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr puts the child in its own process group so that a shell
// exit does not deliver SIGHUP, and so that group-wide signalling works.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
