package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/runner"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/systemd"
)

func runDaemon(args []string, stateDir, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "daemon", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, err := state.Open(stateDir)
	if err != nil {
		return fail(stderr, err, "choose a writable directory with 'pluto --state-dir <path> daemon'")
	}
	defer st.Close()

	exe, err := os.Executable()
	if err != nil {
		return fail(stderr, err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	rn := runner.New(st, exe)
	rn.ReconcileAll()

	srv := daemon.New(st, rn, Version)
	srv.Logf = func(format string, args ...any) {
		fmt.Fprintf(stderr, "pluto: "+format+"\n", args...)
	}
	if err := srv.Listen(socket); err != nil {
		return fail(stderr, err, "stop the process using the socket, or start it on another socket with 'pluto --socket <path> daemon'")
	}
	fmt.Fprintf(stderr, "pluto %s daemon listening on %s (state %s)\n", Version, socket, stateDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()
	go srv.AutoPauseLoop(ctx, daemon.AutoPauseInterval)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fail(stderr, err)
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

func runInstall(args []string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "install", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(stderr, err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := systemd.Install(exe, stdout); err != nil {
		return fail(stderr, err, "check the systemd user session with 'systemctl --user status'")
	}
	return 0
}

func runUninstall(args []string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "uninstall", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := systemd.Uninstall(stdout); err != nil {
		return fail(stderr, err, "check the systemd user session with 'systemctl --user status'")
	}
	return 0
}
