//go:build !windows

package host

import "syscall"

// DiskFree reports free bytes on the filesystem holding path.
func DiskFree(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return uint64(s.Bavail) * uint64(s.Bsize), nil //nolint:unconvert // Field types differ across platforms.
}
