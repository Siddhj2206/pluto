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

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		return 1
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
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		return 1
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
