// Package logging provides a small rotating file logger for the Manager.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maxSize  = 10 << 20 // rotate at 10 MiB
	maxFiles = 5
)

// Logger writes timestamped lines to a rotating file and stderr.
type Logger struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

// New opens (or creates) the log file.
func New(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	st, _ := f.Stat()
	var size int64
	if st != nil {
		size = st.Size()
	}
	return &Logger{path: path, f: f, size: size}, nil
}

func (l *Logger) Write(level, format string, args ...any) {
	line := fmt.Sprintf("%s [%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05"), level, fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rotateLocked()
	if l.f != nil {
		_, _ = l.f.WriteString(line)
		l.size += int64(len(line))
	}
	_, _ = io.WriteString(os.Stderr, line)
}

func (l *Logger) Info(format string, args ...any)  { l.Write("INFO", format, args...) }
func (l *Logger) Warn(format string, args ...any)  { l.Write("WARN", format, args...) }
func (l *Logger) Error(format string, args ...any) { l.Write("ERROR", format, args...) }

// Audit appends an audit record (never rotated by size during one write).
func (l *Logger) Audit(actor, action, detail string) {
	line := fmt.Sprintf("%s AUDIT actor=%s action=%s %s\n",
		time.Now().Format("2006-01-02 15:04:05"), actor, action, detail)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_, _ = l.f.WriteString(line)
	}
}

// rotateLocked moves gswxy-manager.log -> .1 etc. when size exceeds the cap.
func (l *Logger) rotateLocked() {
	if l.f == nil || l.size < maxSize {
		return
	}
	_ = l.f.Close()
	for i := maxFiles - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", l.path, i), fmt.Sprintf("%s.%d", l.path, i+1))
	}
	_ = os.Rename(l.path, l.path+".1")
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		l.f = nil
		return
	}
	l.f = f
	l.size = 0
}
