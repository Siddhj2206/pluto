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
	"syscall"
	"time"

	"github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

func runDaemon(args []string, stateDir, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "daemon", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	runRetentionDefault := state.DefaultRunRetention
	if v := os.Getenv("PLUTO_RUN_RETENTION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return fail(stderr, fmt.Errorf("PLUTO_RUN_RETENTION %q is not a duration (for example 720h)", v))
		}
		runRetentionDefault = d
	}
	runRetention := fs.Duration("run-retention", runRetentionDefault, "how long run records and logs are kept (0 keeps everything)")
	freezeIdle := fs.Duration("freeze-idle", daemon.DefaultFreezeWindow, "how long a running box may sit idle before it freezes, memory preserved (0 disables)")
	stopIdle := fs.Duration("stop-idle", daemon.DefaultStopWindow, "how long a box may sit idle before it stops, keeping its volumes (0 disables)")
	retainedMaxAge := fs.Duration("retained-max-age", box.DefaultRetainedMaxAge, "how long a retained failed CI box is kept (0 disables the age bound)")
	retainedMaxCount := fs.Int("retained-max-count", box.DefaultRetainedMaxCount, "how many retained failed CI boxes are kept (0 disables the count bound)")
	orphanGrace := fs.Duration("orphan-grace", box.DefaultOrphanGrace, "how long an orphaned durable box must be unchanged before it is collected")
	if code := parseCommand(fs, args, stderr, "usage: pluto daemon"); code != 0 {
		return code
	}
	if *freezeIdle < 0 || *stopIdle < 0 {
		return fail(stderr, errors.New("idle windows must not be negative"))
	}
	if *retainedMaxAge < 0 || *retainedMaxCount < 0 || *orphanGrace < 0 {
		return fail(stderr, errors.New("retention windows and counts must not be negative"))
	}
	if *freezeIdle > 0 && *stopIdle > 0 && *stopIdle < *freezeIdle {
		return fail(stderr, errors.New("--stop-idle must be at least --freeze-idle"))
	}
	st, err := state.Open(stateDir)
	if err != nil {
		return fail(stderr, err, "choose a writable directory with 'pluto --state-dir <path> daemon'")
	}
	defer st.Close()

	boxes := box.New(st, container.NewCLI(container.DefaultBinary()), vcs.NewGit())
	srv := daemon.New(st, Version, daemon.WithBoxManager(boxes))
	srv.RunRetention = *runRetention
	srv.RetainedMaxAge = *retainedMaxAge
	srv.RetainedMaxCount = *retainedMaxCount
	srv.OrphanGrace = *orphanGrace
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
	go srv.IdleLoop(ctx, daemon.IdleInterval, *freezeIdle, *stopIdle)
	go srv.SchedulerLoop(ctx, daemon.SchedulerInterval)
	go srv.RetentionLoop(ctx, daemon.RetentionInterval)
	go srv.BoxRetentionLoop(ctx, daemon.BoxRetentionInterval)

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
