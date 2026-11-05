//go:build !windows

package host

import (
	"os"
	"syscall"
)

// Lock takes the single-host lock, and holds it until the file is closed or the
// process ends. Two hosts sharing one database would double-start boxes.
func Lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
