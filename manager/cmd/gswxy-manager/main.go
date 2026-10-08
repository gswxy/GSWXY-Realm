// GSWXY Realm Manager — the single-binary lifecycle manager for the
// bundled AzerothCore + Playerbots stack on fnOS.
//
// Subcommands (invoked by the fnOS cmd/main wrapper):
//
//	gswxy-manager run [--port N]   run the daemon in the foreground
//	gswxy-manager start            daemonize (double-fork via re-exec)
//	gswxy-manager stop             graceful stop (SIGTERM + wait)
//	gswxy-manager status           exit 0 running / 3 stopped
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/gswxy/gswxy-realm/manager/internal/api"
	"github.com/gswxy/gswxy-realm/manager/internal/app"
	"github.com/gswxy/gswxy-realm/manager/internal/platform"
	"github.com/gswxy/gswxy-realm/manager/internal/update"
	"github.com/gswxy/gswxy-realm/manager/internal/version"
)

const defaultPort = 18700

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		port := defaultPort
		regenOnly := false
		for i := 2; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--regen-only":
				regenOnly = true
			case "--port":
				if i+1 < len(os.Args) {
					port, _ = strconv.Atoi(os.Args[i+1])
				}
			}
		}
		if regenOnly {
			runRegenOnly()
			return
		}
		runDaemon(port)
	case "start":
		startDaemon()
	case "stop":
		stopDaemon()
	case "status":
		statusDaemon()
	case "version", "--version":
		v := version.Load(platform.Detect().BuildInfo())
		fmt.Printf("GSWXY Realm Manager %s (%s)\n", v.Version, v.Channel)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: gswxy-manager <run|start|stop|status>")
}

// runRegenOnly regenerates run configs against the current payload and
// exits (used by the fnOS upgrade_callback).
func runRegenOnly() {
	a, err := app.New(platform.Detect())
	if err != nil {
		fmt.Fprintf(os.Stderr, "manager init: %v\n", err)
		os.Exit(1)
	}
	if err := a.RegenerateAll(); err != nil {
		fmt.Fprintf(os.Stderr, "regenerate: %v\n", err)
		os.Exit(1)
	}
	a.Log.Info("run configs regenerated (upgrade)")
	fmt.Println("regenerated")
}

// runDaemon serves the API+UI in the foreground.
func runDaemon(port int) {
	// 开发后门绝不能静默存在于正式发行环境。
	if os.Getenv("GSRM_DEV_NOAUTH") == "1" {
		p := platform.Detect()
		if p.OnFnOS {
			fmt.Fprintln(os.Stderr, "GSRM_DEV_NOAUTH 在 fnOS 生产环境被拒绝启动")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "WARN: GSRM_DEV_NOAUTH=1 — 管理接口无鉴权（仅限开发环境）")
	}
	a, err := app.New(platform.Detect())
	if err != nil {
		fmt.Fprintf(os.Stderr, "manager init: %v\n", err)
		os.Exit(1)
	}
	a.Log.Info("GSWXY Realm Manager starting (pid %d)", os.Getpid())
	a.EnsureDataLinks()

	// Recover from an unclean stop: if the servers were running when the
	// Manager died, fnOS will call start again anyway — nothing to do here.

	writeSelfPid(a)
	defer func() {
		a.StopAll()
		a.Log.Info("GSWXY Realm Manager stopped")
	}()

	auth := api.NewAuth(func() []byte { return []byte(a.Admin.SessionKey()) }, a.Log)
	srv := &api.Server{
		App: a, Log: a.Log, Auth: auth, Admin: a.Admin,
		State: a.State, Updater: update.New(),
	}

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		a.Log.Error("listen %s: %v", addr, err)
		os.Exit(1)
	}
	a.Log.Info("WebUI listening on %s", addr)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		a.Log.Info("termination signal received, shutting down gracefully")
		go func() {
			<-time.After(90 * time.Second)
			a.Log.Error("graceful shutdown timed out")
			os.Exit(1)
		}()
		_ = ln.Close()
	}()

	_ = http.Serve(ln, srv.Handler())
}

func writeSelfPid(a *app.App) {
	_ = os.WriteFile(filepath.Join(a.Paths.Var, "manager.pid"),
		[]byte(strconv.Itoa(os.Getpid())), 0o644)
}

// startDaemon re-execs itself detached with setsid (fnOS cmd/main calls
// this and must not block).
func startDaemon() {
	// Already running?
	if daemonAlive() {
		fmt.Println("already running")
		return
	}
	attr := procAttr([]*os.File{devNull("r"), devNull("w"), devNull("w")})
	self, _ := os.Executable()
	p, err := os.StartProcess(self, []string{self, "run"}, attr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(1)
	}
	_ = p.Release()
	fmt.Println("started")
}

func stopDaemon() {
	pid := readManagerPid()
	if pid == 0 {
		fmt.Println("not running")
		os.Exit(3)
	}
	_ = killPID(pid, syscall.SIGTERM)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			fmt.Println("stopped")
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	_ = killPID(pid, syscall.SIGKILL)
	fmt.Println("killed after timeout")
}

func statusDaemon() {
	if daemonAlive() {
		fmt.Println("running")
		return
	}
	fmt.Println("stopped")
	os.Exit(3)
}

func daemonAlive() bool {
	pid := readManagerPid()
	return pid > 0 && alive(pid)
}

func readManagerPid() int {
	p := platform.Detect()
	raw, err := os.ReadFile(filepath.Join(p.Var, "manager.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(string(raw))
	return pid
}

func devNull(mode string) *os.File {
	flag := os.O_RDONLY
	if mode == "w" {
		flag = os.O_WRONLY
	}
	f, err := os.OpenFile(os.DevNull, flag, 0)
	if err != nil {
		return nil
	}
	return f
}
