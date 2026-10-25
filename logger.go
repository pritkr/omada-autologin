package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	logRotateAt  = 512 * 1024
	logKeepBytes = 256 * 1024
)

// Logger writes timestamped lines to a file and, when verbose, to stderr.
// A missing log path disables file logging (useful for one-off commands).
type Logger struct {
	mu      sync.Mutex
	file    *os.File
	verbose bool
	path    string
}

func newLogger(path string, verbose bool) (*Logger, error) {
	l := &Logger{verbose: verbose, path: path}
	if path == "" {
		return l, nil
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	rotateLog(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	_ = f.Chmod(0o600)
	l.file = f
	return l, nil
}

func rotateLog(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= logRotateAt {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	keep := int64(logKeepBytes)
	if info.Size() < keep {
		keep = info.Size()
	}
	if _, err := f.Seek(info.Size()-keep, io.SeekStart); err != nil {
		return
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, tail, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func (l *Logger) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.WriteString(line)
	}
	if l.verbose {
		_, _ = os.Stderr.WriteString(line)
	}
}

func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
