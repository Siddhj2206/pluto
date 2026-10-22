//go:build !windows

package console

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// IsTerminal reports whether w is an interactive terminal. A writer that is
// not an *os.File (a test buffer or a pipe wrapper) is never a terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
