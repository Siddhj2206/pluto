//go:build !unix

package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// lockFile is the Windows-safe fallback: an exclusively created lock file with
// a bounded wait. A lock older than ten minutes is treated as stale, because
// Windows has no flock to release it when a process dies.
func lockFile(path string) (func(), error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
		if err == nil {
			return func() {
				_ = f.Close()
				_ = os.Remove(path)
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 10*time.Minute {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for workspace lock %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
