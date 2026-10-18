//go:build !windows

package devices

import (
	"fmt"
	"os"
)

// syncDir flushes the directory entry after a rename so the new name survives
// a crash. POSIX allows fsync on a directory handle.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", dir, err)
	}
	defer d.Close()
	return d.Sync()
}
