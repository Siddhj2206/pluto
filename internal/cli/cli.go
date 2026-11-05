// Package cli is Pluto's command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/Siddhj2206/pluto/internal/engine"
	"github.com/Siddhj2206/pluto/internal/github"
	"github.com/Siddhj2206/pluto/internal/host"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/version"
)

// Exit codes.
const (
	exitOK     = 0
	exitError  = 1
	exitUsage  = 2
	exitNoHost = 3
)

const usage = `Pluto runs CI on machines you own.

  pluto doctor                   Check this machine and print what to do next
  pluto host start|stop|status   Run the host as a background service
  pluto ci connect github        Connect the current repo
  pluto ci ls                    List connected repos and stuck jobs
  pluto runs [--json]            List runs
  pluto logs <run>               Show a finished job's log
  pluto enter <run>              Open a shell in a failed run's box
  pluto version                  Show the version
`

// Main runs the command line and returns an exit code.
func Main(args []string) int {
	if err := dispatch(args); err != nil {
		var ue *userError
		if errors.As(err, &ue) {
			fmt.Fprintf(os.Stderr, "error: %s\n", ue.msg)
			if ue.next != "" {
				fmt.Fprintf(os.Stderr, "  Next: %s\n", ue.next)
			}
			return ue.code
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitError
	}
	return exitOK
}

func usageError(msg string) error { return &userError{msg: msg, next: "pluto help", code: exitUsage} }

func dispatch(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "version", "--version":
		fmt.Println("pluto", version.Version)
		return nil
	case "doctor":
		return doctor()
	case "host":
		return hostCommand(rest)
	case "ci":
		return ciCommand(rest)
	case "runs":
		return runsCommand(rest)
	case "logs":
		return logsCommand(rest)
	case "enter":
		return enterCommand(rest)
	}
	return usageError("unknown command " + cmd)
}

func hostCommand(args []string) error {
	if len(args) != 1 {
		return usageError("usage: pluto host start|stop|status")
	}
	switch args[0] {
	case "run":
		return hostRun()
	case "start":
		return hostStart()
	case "stop":
		return runSystemctl("disable", "--now", "pluto-host.service")
	case "status":
		return hostStatus()
	}
	return usageError("unknown host command " + args[0])
}

// hostRun is the service itself. The unit runs it; people use pluto host start.
func hostRun() error {
	eng, err := engine.Detect()
	if err != nil {
		return &userError{msg: err.Error(), code: exitError}
	}
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := host.Lock(filepath.Join(dir, "host.lock"))
	if err != nil {
		return &userError{msg: "another Pluto host is already running on this machine.", next: "pluto host status", code: exitError}
	}
	defer lock.Close()
	db, err := store.Open(filepath.Join(dir, "pluto.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	h := &host.Host{
		Store:  db,
		Engine: eng,
		GitHub: func(token string) github.API { return github.New(token) },
		Config: host.DefaultConfig(),
		FreeBytes: func(ctx context.Context) (uint64, error) {
			path, err := eng.StoragePath(ctx)
			if err != nil {
				return 0, err
			}
			return host.DiskFree(path)
		},
	}
	sock := socketPath()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	go h.Run(ctx)
	log.Printf("pluto host %s listening on %s, using %s", version.Version, sock, eng.Name())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func hostStart() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath(), []byte(unitFile(self, os.Getenv("PATH"))), 0o644); err != nil {
		return err
	}
	if err := runSystemctl("daemon-reload"); err != nil {
		return err
	}
	if err := runSystemctl("enable", "pluto-host.service"); err != nil {
		return err
	}
	// Restart, not start, so a new binary takes effect. Boxes keep running.
	if err := runSystemctl("restart", "pluto-host.service"); err != nil {
		return err
	}
	c := newClient()
	var info host.Info
	for range 50 {
		if err = c.json(http.MethodGet, "/v1/info", nil, &info); err == nil {
			fmt.Printf("Host running (pluto %s, %s). Nothing listens on the network.\n", info.Version, info.Engine)
			fmt.Println("Next: cd into a repo, then run: pluto ci connect github")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &userError{msg: "the host service started but isn't answering.", next: "journalctl --user -u pluto-host", code: exitError}
}

// systemdQuote quotes a value for a unit file. A % is a specifier to systemd.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

func unitFile(self, path string) string {
	return fmt.Sprintf(`[Unit]
Description=Pluto host

[Service]
ExecStart=%s host run
Environment=%s
Restart=on-failure
RestartSec=3
# Boxes keep running when the host stops or upgrades.
KillMode=process

[Install]
WantedBy=default.target
`, systemdQuote(self), systemdQuote("PATH="+path))
}

func hostStatus() error {
	var info host.Info
	if err := newClient().json(http.MethodGet, "/v1/info", nil, &info); err != nil {
		return err
	}
	fmt.Printf("Host running: pluto %s, engine %s\nLabels: %s\n", info.Version, info.Engine, strings.Join(info.Labels, ", "))
	return nil
}

func ciCommand(args []string) error {
	switch {
	case len(args) == 2 && args[0] == "connect" && args[1] == "github":
		return ciConnect()
	case len(args) == 2 && args[0] == "connect":
		return &userError{msg: "Pluto only connects to GitHub so far, not " + args[1] + ".", code: exitUsage}
	case len(args) == 1 && args[0] == "ls":
		return ciList()
	}
	return usageError("usage: pluto ci connect github | pluto ci ls")
}

var githubRemote = regexp.MustCompile(`^(?:git@github\.com:|ssh://git@github\.com/|https://(?:[^@/]+@)?github\.com/)([^/]+/[^/]+?)(?:\.git)?/?$`)

// repoFromRemote extracts owner/name from a GitHub remote URL.
func repoFromRemote(remote string) (string, bool) {
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil {
		return "", false
	}
	return m[1], true
}

func ciConnect() error {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return &userError{msg: "this directory has no git remote named origin.", next: "cd into a repo cloned from GitHub", code: exitError}
	}
	repo, ok := repoFromRemote(string(out))
	if !ok {
		return &userError{msg: "origin isn't a GitHub repository: " + stripCredentials(string(out)), code: exitError}
	}
	token, err := readToken(repo)
	if err != nil {
		return err
	}
	if token == "" {
		return &userError{msg: "no token was provided.", next: "pluto ci connect github   (and paste a token, or pipe one in)", code: exitUsage}
	}
	c := newClient()
	if err := c.require("ci.github"); err != nil {
		return err
	}
	var st host.RepoStatus
	if err := c.json(http.MethodPost, "/v1/repos", host.ConnectRequest{Repo: repo, Token: token}, &st); err != nil {
		return err
	}
	fmt.Printf("Connected %s.\n\nIn your workflow, change the job's runs-on to:\n\n    runs-on: [self-hosted, pluto, linux]\n\n", repo)
	fmt.Println("Pull requests from forks are refused before any step runs, and show as a failed check.")
	fmt.Println("Follow jobs with: pluto runs")
	return nil
}

var urlCredentials = regexp.MustCompile(`//[^/@]*@`)

// stripCredentials removes any user and password from a URL, so they don't end up
// in terminal scrollback or logs.
func stripCredentials(remote string) string {
	return urlCredentials.ReplaceAllString(strings.TrimSpace(remote), "//")
}

func readToken(repo string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	fmt.Printf("Pluto needs a token that can manage runners on %s.\n", repo)
	fmt.Println("Create a fine-grained one at https://github.com/settings/personal-access-tokens/new")
	fmt.Printf("  Repository access: only %s\n  Permissions: Administration (read and write), Actions (read)\n\n", repo)
	fmt.Print("Paste token (hidden): ")
	data, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func ciList() error {
	var repos []host.RepoStatus
	if err := newClient().json(http.MethodGet, "/v1/repos", nil, &repos); err != nil {
		return err
	}
	if len(repos) == 0 {
		fmt.Println("No repos connected.\nNext: cd into a repo, then run: pluto ci connect github")
		return nil
	}
	for _, r := range repos {
		fmt.Printf("%s: %d queued\n", r.Name, r.Queued)
		if r.Problem != "" {
			fmt.Printf("  problem: %s\n", r.Problem)
		}
		for _, s := range r.Stuck {
			fmt.Printf("  stuck: job %q wants [%s], which this host's runners can't satisfy\n    %s\n", s.Name, strings.Join(s.Labels, ", "), s.URL)
		}
	}
	return nil
}

func runsCommand(args []string) error {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	limit := fs.Int("limit", 30, "how many runs to show")
	if err := fs.Parse(args); err != nil {
		return &userError{msg: err.Error(), code: exitUsage}
	}
	var runs []store.Run
	c := newClient()
	if err := c.require("runs"); err != nil {
		return err
	}
	if err := c.json(http.MethodGet, fmt.Sprintf("/v1/runs?limit=%d", *limit), nil, &runs); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(map[string]any{"schema": 1, "runs": runs})
	}
	if len(runs) == 0 {
		fmt.Println("No runs yet. A run appears when a job with the pluto label is queued.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tREPO\tJOB\tREF\tSTATE\tSTARTED\tTOOK\tNOTE")
	for _, r := range runs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Repo, dash(r.Job), dash(shortRef(r.Ref)), r.State, ago(r.CreatedAt), took(r), note(r))
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortRef(ref string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/", "refs/"} {
		if s, ok := strings.CutPrefix(ref, p); ok {
			return s
		}
	}
	return ref
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func took(r store.Run) string {
	if r.StartedAt.IsZero() {
		return "-"
	}
	end := r.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(r.StartedAt).Round(time.Second).String()
}

func note(r store.Run) string {
	switch {
	case r.BoxKept:
		return "pluto enter " + r.ID
	case r.Reason != "":
		return r.Reason
	}
	return ""
}

func oneRunArg(cmd string, args []string) (string, error) {
	if len(args) != 1 {
		return "", usageError("usage: pluto " + cmd + " <run>")
	}
	return args[0], nil
}

func logsCommand(args []string) error {
	id, err := oneRunArg("logs", args)
	if err != nil {
		return err
	}
	c := newClient()
	if err := c.require("logs"); err != nil {
		return err
	}
	data, err := c.raw(http.MethodGet, "/v1/runs/"+url.PathEscape(id)+"/logs", nil)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func enterCommand(args []string) error {
	id, err := oneRunArg("enter", args)
	if err != nil {
		return err
	}
	var resp host.EnterResponse
	c := newClient()
	if err := c.require("enter"); err != nil {
		return err
	}
	if err := c.json(http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/enter", nil, &resp); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Entering the box. Changes here don't affect the run's result. Exit with Ctrl-D.")
	cmd := exec.Command(resp.Argv[0], resp.Argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return enterResult(cmd.Run())
}

// enterResult separates the engine failing from the shell ending with a status.
// A shell's last command failing is normal while debugging. 125 is the engine's own.
func enterResult(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() != 125 {
		return nil
	}
	return err
}

func doctor() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var next []string
	failed := 0
	check := func(ok bool, what, fix string) {
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			failed++
			if fix != "" {
				next = append(next, fix)
			}
		}
		fmt.Printf("  %s  %s\n", mark, what)
	}
	eng, err := engine.Detect()
	if err != nil {
		check(false, "container engine: not found", "install podman")
	} else if v, err := eng.Version(ctx); err != nil {
		check(false, eng.Name()+": "+err.Error(), "")
	} else {
		check(true, fmt.Sprintf("container engine: %s %s", eng.Name(), v), "")
	}
	if eng != nil {
		if path, err := eng.StoragePath(ctx); err == nil {
			if free, err := host.DiskFree(path); err == nil {
				low := host.DefaultConfig().MinFreeBytes
				check(free >= low, fmt.Sprintf("disk: %d GiB free in %s", free>>30, path), fmt.Sprintf("free up disk space: new boxes need at least %d GiB", low>>30))
			}
		}
	}
	if out, err := exec.CommandContext(ctx, "loginctl", "show-user", os.Getenv("USER"), "-p", "Linger", "--value").Output(); err == nil {
		check(strings.TrimSpace(string(out)) == "yes", "keeps running after you log out: "+strings.TrimSpace(string(out)), "loginctl enable-linger")
	}
	c := newClient()
	var info host.Info
	if err := c.json(http.MethodGet, "/v1/info", nil, &info); err != nil {
		check(false, "host: not running", "pluto host start")
	} else {
		check(true, "host: running, pluto "+info.Version, "")
		var repos []host.RepoStatus
		if err := c.json(http.MethodGet, "/v1/repos", nil, &repos); err == nil {
			check(len(repos) > 0, fmt.Sprintf("connected repos: %d", len(repos)), "cd into a repo, then run: pluto ci connect github")
			for _, r := range repos {
				if r.Problem != "" {
					check(false, r.Name+": "+r.Problem, "")
				}
				for _, s := range r.Stuck {
					check(false, fmt.Sprintf("%s: job %q wants labels this host doesn't have: %s", r.Name, s.Name, strings.Join(s.Labels, ", ")), "change the job's runs-on to [self-hosted, pluto, linux]")
				}
			}
		}
	}
	if len(next) > 0 {
		fmt.Println("\nNext:")
		for _, n := range next {
			fmt.Println("  " + n)
		}
	}
	if failed > 0 {
		return &userError{msg: fmt.Sprintf("%d check(s) need attention.", failed), code: exitError}
	}
	return nil
}
