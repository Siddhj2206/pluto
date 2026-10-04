// Package cli implements the pluto command line: one binary in daemon and
// client modes.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/state"
)

// Version is the build version, overridable at link time.
var Version = "0.1.0-dev"

// Run executes one pluto command and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("pluto", flag.ContinueOnError)
	global.SetOutput(stderr)
	socket := global.String("socket", DefaultSocket(), "daemon unix socket")
	stateDir := global.String("state-dir", DefaultStateDir(), "state directory (daemon only)")
	if err := global.Parse(args); err != nil {
		return 2
	}
	rest := global.Args()
	if len(rest) == 0 {
		usage(stderr)
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "daemon":
		return runDaemon(cmdArgs, *stateDir, *socket, stdout, stderr)
	case "up":
		return runUp(cmdArgs, *socket, stdout, stderr)
	case "ls":
		return runLs(cmdArgs, *socket, stdout, stderr)
	case "status":
		return runStatus(cmdArgs, *socket, stdout, stderr)
	case "destroy":
		return runDestroy(cmdArgs, *socket, stdout, stderr)
	case "install":
		return runInstall(cmdArgs, stdout, stderr)
	case "uninstall":
		return runUninstall(cmdArgs, stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "pluto %s\n", Version)
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", cmd)
		usage(stderr)
		return 2
	}
}

// DefaultSocket is where the daemon listens unless overridden.
func DefaultSocket() string {
	if v := os.Getenv("PLUTO_SOCKET"); v != "" {
		return v
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "pluto", "pluto.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("pluto-%d", os.Getuid()), "pluto.sock")
}

// DefaultStateDir is where box records live unless overridden.
func DefaultStateDir() string {
	if v := os.Getenv("PLUTO_STATE_DIR"); v != "" {
		return v
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "pluto")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), fmt.Sprintf("pluto-state-%d", os.Getuid()))
	}
	return filepath.Join(home, ".local", "state", "pluto")
}

func resolveBox(c *client.Client, target string) (*state.Box, error) {
	if state.ValidID(target) {
		return c.Box(target)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	list, err := c.ListBoxes()
	if err != nil {
		return nil, err
	}
	for _, b := range list.Boxes {
		if b.Worktree == abs {
			return b, nil
		}
	}
	return nil, fmt.Errorf("no box for worktree %s", abs)
}

func isTerminal(f *os.File) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// splitFlags moves flags ahead of positionals so `destroy <target> --yes`
// parses. Only boolean flags are used with positional arguments today.
func splitFlags(args []string) []string {
	var flags, positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	return append(flags, positional...)
}

func fail(stderr io.Writer, err error) int {
	if errors.Is(err, client.ErrUnreachable) {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		fmt.Fprintln(stderr, "start the daemon with 'pluto daemon' or install it with 'pluto install'")
		return 1
	}
	fmt.Fprintf(stderr, "pluto: %v\n", err)
	return 1
}

func usage(w io.Writer) {
	fmt.Fprint(w, `pluto - durable work machines

usage: pluto [--socket PATH] [--state-dir PATH] <command> [args]

commands:
  up        create (or find) the box for a worktree
  ls        list boxes
  status    show one box (by id or worktree)
  destroy   remove a box and its disk
  daemon    run the host daemon in the foreground
  install   install the daemon as a systemd user service with linger
  uninstall remove the systemd user service
  version   print the version
`)
}
