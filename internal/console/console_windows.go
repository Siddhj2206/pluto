//go:build windows

package console

import (
	"os"

	"golang.org/x/sys/windows"
)

// IsTerminal reports whether f is an interactive console. GetConsoleMode
// succeeds only on a console handle; a file or a pipe fails, so a redirected
// stdin does not allocate a remote tty.
func IsTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
