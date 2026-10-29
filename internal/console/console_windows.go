//go:build windows

package console

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// IsTerminal reports whether w is an interactive console. GetConsoleMode
// succeeds only on a console handle; a file or a pipe fails, so a redirected
// stdin does not allocate a remote tty. A writer that is not an *os.File is
// never a terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
