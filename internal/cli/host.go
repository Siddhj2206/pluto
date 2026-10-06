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
	"strings"
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
	maxRunning := fs.Int("max-running-boxes", 4, "maximum running boxes on this host")
	queueCapacity := fs.Int("queue-capacity", 100, "maximum actionable queue items")
	queueAging := fs.Duration("queue-aging", 5*time.Minute, "time before a queued item is promoted one priority class")
	webhookListen := fs.String("webhook-listen", "", "HTTP address for webhook ingress (put behind your TLS proxy)")
	githubSource := fs.String("github-push-source", "", "GitHub push webhook source ID")
	githubBox := fs.String("github-push-box", "", "registered box ID targeted by GitHub pushes")
	githubSecretEnv := fs.String("github-push-secret-env", "PLUTO_GITHUB_WEBHOOK_SECRET", "environment variable holding the GitHub webhook secret")
	var genericWebhooks stringList
	fs.Var(&genericWebhooks, "generic-webhook", "repeatable generic source config: source,box-id,secret-env")
	if code := parseCommand(fs, args, stderr, "usage: pluto daemon"); code != 0 {
		return code
	}
	if *maxRunning < 1 || *queueCapacity < 1 || *queueAging <= 0 {
		return fail(stderr, errors.New("daemon queue limits and aging interval must be positive"))
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
	srv.MaxRunningBoxes = *maxRunning
	srv.QueueCapacity = *queueCapacity
	srv.QueueAgingInterval = *queueAging
	if *githubSource != "" || *githubBox != "" {
		if *webhookListen == "" {
			return fail(stderr, errors.New("--github-push-source and --github-push-box require --webhook-listen"))
		}
		if err := srv.RegisterGitHubPush(*githubSource, *githubBox, os.Getenv(*githubSecretEnv)); err != nil {
			return fail(stderr, err)
		}
	}
	if len(genericWebhooks) > 0 {
		if *webhookListen == "" {
			return fail(stderr, errors.New("--generic-webhook requires --webhook-listen"))
		}
		for _, raw := range genericWebhooks {
			parts := strings.Split(raw, ",")
			if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
				return fail(stderr, errors.New("--generic-webhook must be source,box-id,secret-env"))
			}
			if err := srv.RegisterGenericWebhook(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), os.Getenv(strings.TrimSpace(parts[2]))); err != nil {
				return fail(stderr, err)
			}
		}
	}
	srv.Logf = func(format string, args ...any) {
		fmt.Fprintf(stderr, "pluto: "+format+"\n", args...)
	}
	if err := srv.Listen(socket); err != nil {
		return fail(stderr, err, "stop the process using the socket, or start it on another socket with 'pluto --socket <path> daemon'")
	}
	var webhookServer *http.Server
	if *webhookListen != "" {
		webhookServer, err = srv.ListenWebhook(*webhookListen)
		if err != nil {
			return fail(stderr, err)
		}
	}
	fmt.Fprintf(stderr, "pluto %s daemon listening on %s (state %s)\n", Version, socket, stateDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()
	go srv.AutoPauseLoop(ctx, daemon.AutoPauseInterval)
	go srv.SchedulerLoop(ctx, daemon.SchedulerInterval)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fail(stderr, err)
		}
		return 0
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if webhookServer != nil {
			_ = webhookServer.Shutdown(shutdownCtx)
		}
		_ = srv.Shutdown(shutdownCtx)
		fmt.Fprintln(stderr, "pluto daemon stopped")
		return 0
	}
}

type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }
func (l *stringList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("value cannot be empty")
	}
	*l = append(*l, value)
	return nil
}

func runInstall(args []string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "install", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	if code := parseCommand(fs, args, stderr, "usage: pluto install"); code != 0 {
		return code
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
	if code := parseCommand(fs, args, stderr, "usage: pluto uninstall"); code != 0 {
		return code
	}
	if err := systemd.Uninstall(stdout); err != nil {
		return fail(stderr, err, "check the systemd user session with 'systemctl --user status'")
	}
	return 0
}
