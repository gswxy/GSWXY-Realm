package dbinit

import (
	"net"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gswxy/gswxy-realm/manager/internal/platform"
)

// ClientEnv builds the environment for bundled DB client tools: their
// private lib dirs carry libmysqlclient / bundled extras (protobuf, …).
func ClientEnv(p platform.Paths) []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"LD_LIBRARY_PATH=" + strings.Join([]string{
			filepath.Join(p.MySQLRuntime(), "lib"),
			filepath.Join(p.AppDest, "lib"),
		}, ":"),
	}
}

func command(bin string, args []string) *exec.Cmd { return exec.Command(bin, args...) }
func netListen(network, addr string) (net.Listener, error) {
	return net.Listen(network, addr)
}
