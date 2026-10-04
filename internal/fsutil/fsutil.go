// Package fsutil provides small filesystem helpers used when preparing boxes.
package fsutil

import (
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

// ficlone is the FICLONE ioctl: make dst a reflink (copy-on-write clone) of
// src. Filesystems that do not support it return an error and the caller
// falls back to a plain copy.
const ficlone = 0x40049409

// CloneFile copies src to dst, preferring a reflink so a box disk shares
// blocks with its base image until it is written. The destination is created
// or truncated and inherits the source's permissions.
func CloneFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()

	if err := reflink(out, in); err == nil {
		return out.Sync()
	}
	if err := out.Truncate(0); err != nil {
		return fmt.Errorf("truncate %s: %w", dst, err)
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s: %w", dst, err)
	}
	return out.Sync()
}

func reflink(dst, src *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, dst.Fd(), ficlone, src.Fd())
	if errno != 0 {
		return errno
	}
	return nil
}

// TailFile returns the last n lines of a file; a missing file is empty.
func TailFile(path string, n int) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n", nil
}
