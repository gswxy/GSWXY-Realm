//go:build !windows

package app

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

type fsStat struct{ availMB int64 }

func statFS(path string) (*fsStat, error) {
	out, err := exec.Command("df", "-Pm", path).Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("df output unexpected")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return nil, fmt.Errorf("df output unexpected")
	}
	mb, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return nil, err
	}
	return &fsStat{availMB: mb}, nil
}

// portBusy checks whether a TCP port is already listening on all/loopback.
func portBusy(port int) bool {
	c, err := exec.Command("ss", "-ltn").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(c), "\n") {
		for _, f := range strings.Fields(line) {
			if strings.HasSuffix(f, ":"+strconv.Itoa(port)) {
				return true
			}
		}
	}
	return false
}

var _ = syscall.Kill
