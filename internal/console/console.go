// Package console reports whether a writer is an interactive terminal. The CLI
// uses it to decide between `ssh -t` and a plain exec, and to decide whether a
// confirmation prompt can be shown. The check is platform-specific: a POSIX
// termios ioctl on unix, GetConsoleMode on Windows. A writer that is not an
// *os.File is never a terminal.
package console
