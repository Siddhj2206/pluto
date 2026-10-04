// Package cli implements the pluto command line: one binary in daemon and
// client modes.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unsafe"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/systemd"
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

func runDaemon(args []string, stateDir, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, err := state.Open(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		return 1
	}
	defer st.Close()

	srv := daemon.New(st, Version)
	if err := srv.Listen(socket); err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "pluto %s daemon listening on %s (state %s)\n", Version, socket, stateDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "pluto: %v\n", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		fmt.Fprintln(stderr, "pluto daemon stopped")
		return 0
	}
}

func runUp(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	worktree := fs.String("worktree", "", "worktree path (default: current directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := *worktree
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "pluto: %v\n", err)
			return 1
		}
	}
	root, branch, err := gitInfo(dir)
	if err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		return 1
	}
	box, created, err := client.New(socket).CreateBox(api.CreateBoxRequest{
		Worktree: root,
		Project:  filepath.Base(root),
		Branch:   branch,
	})
	if err != nil {
		return fail(stderr, err)
	}
	if created {
		fmt.Fprintf(stdout, "created box %s for %s/%s\n", shortID(box.ID), box.Project, box.Branch)
	} else {
		fmt.Fprintf(stdout, "box %s already exists for %s/%s\n", shortID(box.ID), box.Project, box.Branch)
	}
	return 0
}

func runLs(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	list, err := client.New(socket).ListBoxes()
	if err != nil {
		return fail(stderr, err)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPROJECT/BRANCH\tSTATE\tCREATED")
	for _, b := range list.Boxes {
		fmt.Fprintf(w, "%s\t%s/%s\t%s\t%s\n", shortID(b.ID), b.Project, b.Branch, b.State, b.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	w.Flush()
	for _, e := range list.Errors {
		fmt.Fprintf(stderr, "warning: unreadable box record %s: %s\n", e.Path, e.Err)
	}
	return 0
}

func runStatus(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto status <box-id|worktree>")
		return 2
	}
	c := client.New(socket)
	box, err := resolveBox(c, fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "id:       %s\n", box.ID)
	fmt.Fprintf(stdout, "project:  %s\n", box.Project)
	fmt.Fprintf(stdout, "branch:   %s\n", box.Branch)
	fmt.Fprintf(stdout, "worktree: %s\n", box.Worktree)
	fmt.Fprintf(stdout, "state:    %s\n", box.State)
	fmt.Fprintf(stdout, "created:  %s\n", box.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(stdout, "updated:  %s\n", box.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	return 0
}

func runDestroy(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(splitFlags(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto destroy <box-id|worktree> [--yes]")
		return 2
	}
	c := client.New(socket)
	box, err := resolveBox(c, fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	if !*yes {
		if !isTerminal(os.Stdin) {
			fmt.Fprintln(stderr, "pluto: refusing to destroy without confirmation; pass --yes")
			return 1
		}
		fmt.Fprintf(stdout, "destroy box %s (%s/%s, worktree %s)? [y/N] ", shortID(box.ID), box.Project, box.Branch, box.Worktree)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stderr, "aborted")
			return 1
		}
	}
	if err := c.DestroyBox(box.ID); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "destroyed box %s (%s/%s)\n", shortID(box.ID), box.Project, box.Branch)
	return 0
}

func runInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := systemd.Install(exe, stdout); err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		return 1
	}
	return 0
}

func runUninstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := systemd.Uninstall(stdout); err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		return 1
	}
	return 0
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

func gitInfo(dir string) (root, branch string, err error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", "", fmt.Errorf("%s is not a git worktree", dir)
	}
	root = strings.TrimSpace(string(out))
	out, err = exec.Command("git", "-C", root, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		if _, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").Output(); err != nil {
			return "", "", fmt.Errorf("resolve branch in %s: %w", root, err)
		}
		return root, "(detached)", nil
	}
	branch = strings.TrimSpace(string(out))
	return root, branch, nil
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
