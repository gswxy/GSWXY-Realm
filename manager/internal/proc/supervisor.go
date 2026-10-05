// Package proc supervises native child processes (mariadbd, authserver,
// worldserver) with pid files, health checks and exponential backoff.
package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// State of a supervised process.
type State int

const (
	StateStopped State = iota
	StateStarting
	StateRunning
	StateBackoff // crashed, waiting to restart
	StateStopping
	StateFailed // restart budget exhausted, manual start required
)

func (s State) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateBackoff:
		return "backoff"
	case StateStopping:
		return "stopping"
	case StateFailed:
		return "failed"
	}
	return "unknown"
}

// ExitCallback is invoked with the exit error (nil on clean exit code 0).
type ExitCallback func(name string, err error)

// Spec describes one supervised process.
type Spec struct {
	Name    string
	Bin     string
	Args    []string
	Env     []string
	Dir     string
	PidFile string
	Stdin   bool // keep a pipe on stdin (worldserver console)
	OnExit  ExitCallback
	// StopGrace is how long SIGTERM may take before SIGKILL.
	StopGrace time.Duration
}

// Proc is one supervised child.
type Proc struct {
	Spec Spec

	mu          sync.Mutex
	state       State
	cmd         *exec.Cmd
	stdinW      *os.File // write end of child's stdin (console)
	pid         int
	startedAt   time.Time
	consecFails int
	backoff     time.Duration
	stopCh      chan struct{}
	console     *consolePipe
}

// Supervisor owns all child processes.
type Supervisor struct {
	mu    sync.Mutex
	procs map[string]*Proc
	wg    sync.WaitGroup
}

func NewSupervisor() *Supervisor {
	return &Supervisor{procs: map[string]*Proc{}}
}

// Register adds a process spec (not started).
func (s *Supervisor) Register(spec Spec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs[spec.Name] = &Proc{Spec: spec, state: StateStopped}
}

func (s *Supervisor) Get(name string) (*Proc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[name]
	return p, ok
}

func (s *Supervisor) All() []*Proc {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Proc, 0, len(s.procs))
	for _, p := range s.procs {
		out = append(out, p)
	}
	return out
}

// Start launches the named process. Running processes are left alone.
func (s *Supervisor) Start(name string) error {
	p, ok := s.Get(name)
	if !ok {
		return fmt.Errorf("unknown process %q", name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == StateRunning || p.state == StateStarting {
		return nil
	}
	return s.startLocked(p)
}

func (s *Supervisor) startLocked(p *Proc) error {
	if _, err := os.Stat(p.Spec.Bin); err != nil {
		p.state = StateFailed
		return fmt.Errorf("binary missing: %s: %w", p.Spec.Bin, err)
	}
	// Stale pid file from a hard crash? Verify it is not a live process.
	if pid, ok := readPid(p.Spec.PidFile); ok && pidAlive(pid) {
		return fmt.Errorf("process %s already running (pid %d)", p.Spec.Name, pid)
	}

	cmd := exec.Command(p.Spec.Bin, p.Spec.Args...)
	cmd.Dir = p.Spec.Dir
	if len(p.Spec.Env) > 0 {
		cmd.Env = append(os.Environ(), p.Spec.Env...)
	}
	setSysProcAttr(cmd) // own process group, survive shell exit

	if p.Spec.Stdin {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		cmd.Stdin = r
		p.stdinW = w
		p.console = newConsolePipe()
		// r belongs to the child (worldserver reads console commands from
		// it); we close our copy only after Start() has dup'd the fd.
		defer r.Close()
	} else {
		cmd.Stdin = nil
	}

	logFile, logW, err := openLog(p.Spec.Name)
	if err != nil {
		return err
	}
	cmd.Stdout = logW
	cmd.Stderr = logW

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start %s: %w", p.Spec.Name, err)
	}
	p.cmd = cmd
	p.pid = cmd.Process.Pid
	p.startedAt = time.Now()
	p.state = StateRunning
	p.consecFails = 0
	p.backoff = 2 * time.Second
	p.stopCh = make(chan struct{})
	_ = writePid(p.Spec.PidFile, p.pid)
	logFile.Close() // child keeps its dup

	s.wg.Add(1)
	go s.watch(p, cmd)
	return nil
}

// watch waits for the child; unexpected exits trigger backoff restarts.
func (s *Supervisor) watch(p *Proc, cmd *exec.Cmd) {
	defer s.wg.Done()
	err := cmd.Wait()

	p.mu.Lock()
	stopping := p.state == StateStopping
	runtime := time.Since(p.startedAt)
	p.stdinW = nil
	if p.console != nil {
		p.console.close()
	}
	p.state = StateStopped
	if p.stopCh != nil {
		close(p.stopCh) // unblock Stop() waiters
		p.stopCh = nil
	}
	_ = os.Remove(p.Spec.PidFile)

	if stopping || errors.Is(err, context.Canceled) {
		p.mu.Unlock()
		if p.Spec.OnExit != nil {
			p.Spec.OnExit(p.Spec.Name, nil)
		}
		return
	}

	// Stable for 10 minutes resets the failure budget.
	if runtime > 10*time.Minute {
		p.consecFails = 0
	}
	p.consecFails++
	if p.consecFails >= 10 {
		p.state = StateFailed
		p.mu.Unlock()
		if p.Spec.OnExit != nil {
			p.Spec.OnExit(p.Spec.Name, fmt.Errorf("restart budget exhausted: %v", err))
		}
		return
	}
	wait := p.backoff
	p.backoff *= 2
	if wait > 5*time.Minute {
		wait = 5 * time.Minute
	}
	p.state = StateBackoff
	p.mu.Unlock()

	if p.Spec.OnExit != nil {
		p.Spec.OnExit(p.Spec.Name, err)
	}

	select {
	case <-time.After(wait):
	case <-p.stopCh:
		return
	}
	p.mu.Lock()
	if p.state != StateBackoff { // someone started/stopped meanwhile
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	_ = s.Start(p.Spec.Name) // restart (exponential backoff loop)
}

// Stop terminates gracefully: SIGTERM (or custom hook), then SIGKILL.
func (s *Supervisor) Stop(name string) error {
	p, ok := s.Get(name)
	if !ok {
		return fmt.Errorf("unknown process %q", name)
	}
	return s.stop(p)
}

func (s *Supervisor) stop(p *Proc) error {
	p.mu.Lock()
	if p.state == StateStopped || p.state == StateFailed {
		p.state = StateStopped
		p.mu.Unlock()
		return nil
	}
	pid := p.pid
	p.state = StateStopping
	stopCh := p.stopCh
	p.mu.Unlock()

	// Graceful path: SIGTERM to the process group.
	if pid > 0 {
		_ = signalPID(-pid, syscall.SIGTERM)
	}
	grace := p.Spec.StopGrace
	if grace <= 0 {
		grace = 30 * time.Second
	}
	select {
	case <-stopCh:
	case <-time.After(grace):
		if pid > 0 {
			_ = signalPID(-pid, syscall.SIGKILL)
		}
		select {
		case <-stopCh:
		case <-time.After(5 * time.Second):
		}
	}
	return nil
}

// StopAll stops every child (used on shutdown / app stop).
func (s *Supervisor) StopAll() {
	for _, p := range s.All() {
		_ = s.stop(p)
	}
}

// Running reports whether the named process is running.
func (s *Supervisor) Running(name string) bool {
	p, ok := s.Get(name)
	if !ok {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == StateRunning
}

// StatusSnapshot returns name->state for the API.
func (s *Supervisor) StatusSnapshot() map[string]string {
	out := map[string]string{}
	for _, p := range s.All() {
		p.mu.Lock()
		out[p.Spec.Name] = p.state.String()
		if p.state == StateRunning {
			out[p.Spec.Name+".pid"] = strconv.Itoa(p.pid)
			out[p.Spec.Name+".uptime"] = time.Since(p.startedAt).Round(time.Second).String()
		}
		p.mu.Unlock()
	}
	return out
}

// WriteConsole sends a line to the child's stdin (worldserver console).
func (s *Supervisor) WriteConsole(name, line string) error {
	p, ok := s.Get(name)
	if !ok {
		return fmt.Errorf("unknown process %q", name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdinW == nil || p.state != StateRunning {
		return fmt.Errorf("%s is not running or has no console", name)
	}
	_, err := p.stdinW.WriteString(line + "\n")
	if err == nil {
		p.console.record(line)
	}
	return err
}

// ConsoleTail returns the last n console lines of the named process.
func (s *Supervisor) ConsoleTail(name string, n int) []string {
	p, ok := s.Get(name)
	if !ok {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.console == nil {
		return nil
	}
	return p.console.tail(n)
}

// ConsoleBuffer exposes the ring buffer subscription (for streaming).
func (s *Supervisor) ConsoleBuffer(name string) (*consolePipe, bool) {
	p, ok := s.Get(name)
	if !ok {
		return nil, false
	}
	return p.console, p.console != nil
}

// ---- helpers ----

func readPid(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, false
	}
	return pid, true
}

func writePid(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644)
}

func openLog(name string) (*os.File, *os.File, error) {
	dir := os.Getenv("GSRM_PROC_LOG_DIR")
	if dir == "" {
		dir = "/tmp/gsrealm-procs"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, nil, err
	}
	return f, f, nil
}
