// Bounded box logs. The prod-host-setup guidance for Firecracker is that log
// storage must be upper-bounded: serial output and the Firecracker log both
// grow without limit otherwise (docs/research/firecracker-operation.md §4 A3).
package runner

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// maxLogBytes caps each box log file. On overflow the writer rotates once,
// keeping the previous segment as <name>.1, so a log occupies at most
// 2*maxLogBytes on disk.
const maxLogBytes = 8 << 20

// rotatingLog is an append-only writer that keeps its file under a byte cap.
// It rotates to <name>.1 rather than truncating in place, so a reader tailing
// the file never sees a half-written record.
type rotatingLog struct {
	path  string
	limit int64
	mu    sync.Mutex
	f     *os.File
	n     int64
}

// openRotatingLog opens (creating if needed) the file at path for appending,
// capped at limit bytes per segment.
func openRotatingLog(path string, limit int64) (*rotatingLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rotatingLog{path: path, limit: limit, f: f, n: info.Size()}, nil
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n > 0 && l.n+int64(len(p)) > l.limit {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.n += int64(n)
	return n, err
}

func (l *rotatingLog) rotate() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	l.f = f
	l.n = 0
	return nil
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// makeFIFO creates the named pipe Firecracker writes its log to, replacing any
// stale node from a previous run.
func makeFIFO(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return unix.Mkfifo(path, 0o600)
}

// fcLogPath is the runner-owned bounded file the Firecracker log pipe is
// drained into.
func fcLogPath(boxDir string) string { return filepath.Join(boxDir, "fc.log") }

// fcLogPipe is the named pipe Firecracker writes its structured log to.
// Firecracker opens log_path without O_APPEND and writes from offset zero, so
// the file cannot be bounded in place; a pipe lets the runner own the file.
func fcLogPipe(boxDir string) string { return filepath.Join(boxDir, "fc.log.fifo") }

// metricsPath is where Firecracker flushes its JSON metrics for a box.
func metricsPath(boxDir string) string { return filepath.Join(boxDir, "metrics.json") }
