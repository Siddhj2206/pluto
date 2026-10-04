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
	case "run":
		return runRun(cmdArgs, *socket, stdout, stderr)
	case "attach":
		return runAttach(cmdArgs, *socket, stdout, stderr)
	case "pause":
		return runPause(cmdArgs, *socket, stdout, stderr)
	case "ls":
		return runLs(cmdArgs, *socket, stdout, stderr)
	case "status":
		return runStatus(cmdArgs, *socket, stdout, stderr)
	case "logs":
		return runLogs(cmdArgs, *socket, stdout, stderr)
	case "destroy":
		return runDestroy(cmdArgs, *socket, stdout, stderr)
	case "image":
		return runImage(cmdArgs, *socket, stdout, stderr)
	case "box":
		return runBox(cmdArgs, *stateDir, stdout, stderr)
	case "vsock":
		return runVsock(cmdArgs, stdout, stderr)
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
	list, err := c.ListBoxes()
	if err != nil {
		return nil, err
	}
	if abs, err := filepath.Abs(target); err == nil {
		for _, b := range list.Boxes {
			if b.Worktree == abs {
				return b, nil
			}
		}
	}
	// `ls` and `status` print short ids; accept them as targets too.
	if idPrefix(target) {
		var match *state.Box
		for _, b := range list.Boxes {
			if strings.HasPrefix(b.ID, target) {
				if match != nil {
					return nil, fmt.Errorf("box id prefix %q matches more than one box", target)
				}
				match = b
			}
		}
		if match != nil {
			return match, nil
		}
		return nil, fmt.Errorf("no box with id prefix %q", target)
	}
	return nil, fmt.Errorf("no box for worktree %s", target)
}

// idPrefix reports whether target could be a box id prefix as printed by
// `ls` and `status` (hex characters and dashes), rather than a path.
func idPrefix(target string) bool {
	if len(target) < 8 {
		return false
	}
	for _, r := range target {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r == '-':
		default:
			return false
		}
	}
	return true
}

func isTerminal(f *os.File) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}

// short shortens an id or hash for display.
func short(s string) string {
	if len(s) >= 8 {
		return s[:8]
	}
	return s
}

// splitFlags moves flags ahead of positionals so `destroy <target> --yes` and
// `logs <target> --service web` parse. valueFlags names the flags that consume
// the following argument.
func splitFlags(args []string, valueFlags ...string) []string {
	takesValue := make(map[string]bool, len(valueFlags))
	for _, name := range valueFlags {
		takesValue[name] = true
	}
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if takesValue[arg] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, arg)
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
  up        create (or wake) the box for a worktree
  run       run a bounded command in a box (refuses a second run while one is active)
  attach    open an ssh session in a box (wakes it first)
  pause     stop a box cleanly; its disk stays on the host
  ls        list boxes
  status    show one box (by id or worktree)
  logs      show a box's provision, wake, service, or job logs
  destroy   remove a box and its disk
  image     import or list base images
  daemon    run the host daemon in the foreground
  install   install the daemon as a systemd user service with linger
  uninstall remove the systemd user service
  version   print the version
`)
}
