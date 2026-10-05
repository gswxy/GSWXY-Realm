//go:build !windows

package dbinit

import (
	"net"
	"os/exec"
)

func command(bin string, args []string) *exec.Cmd { return exec.Command(bin, args...) }
func netListen(network, addr string) (net.Listener, error) {
	return net.Listen(network, addr)
}
