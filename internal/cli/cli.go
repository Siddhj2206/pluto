// Package cli implements the pluto command line: one binary in daemon and
// client modes.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/handshake"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/workspace"
)

// Run executes one pluto command and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("pluto", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.Usage = func() {}
	socket := global.String("socket", DefaultSocket(), "daemon unix socket")
	stateDir := global.String("state-dir", DefaultStateDir(), "state directory (daemon only)")
	device := global.String("device", "", "run the command on a saved device nickname or user@host ssh target")
	version := global.Bool("version", false, "print the version and exit")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		usage(stderr)
		return 2
	}
	// --version is an alias of the version command (ADR 0011).
	if *version {
		fmt.Fprintf(stdout, "pluto %s\n", resolvedVersion())
		return 0
	}
	rest := global.Args()
	deviceSet := false
	global.Visit(func(f *flag.Flag) {
		if f.Name == "device" {
			deviceSet = true
		}
	})
	if deviceSet {
		return runRemote(*device, cliutil.RemoteCommand(global), stdout, stderr)
	}
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "pluto: no command given")
		usage(stderr)
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "daemon":
		return runDaemon(cmdArgs, *stateDir, *socket, stdout, stderr)
	case "init":
		return runInit(cmdArgs, stdout, stderr)
	case "setup":
		return runSetup(cmdArgs, os.Stdin, stdout, stderr)
	case "event":
		return runEvent(cmdArgs, *socket, stdout, stderr)
	case "up":
		return runUp(cmdArgs, *socket, stdout, stderr)
	case "project":
		return runProject(cmdArgs, *socket, stdout, stderr)
	case "worktree":
		return runWorktree(cmdArgs, *socket, stdout, stderr)
	case "run":
		if isRunManagerInvocation(cmdArgs) {
			return runTaskRunCommand(cmdArgs, *socket, stdout, stderr)
		}
		return runRun(cmdArgs, *socket, stdout, stderr)
	case "task":
		return runTaskCommand(cmdArgs, *socket, stdout, stderr)
	case "job":
		return runJobCommand(cmdArgs, *socket, stdout, stderr)
	case "queue":
		return runQueue(cmdArgs, *socket, stdout, stderr)
	case "events":
		return runEvents(cmdArgs, *socket, stdout, stderr)
	case "schedule":
		return runScheduleCommand(cmdArgs, *socket, stdout, stderr)
	case "trust":
		return runTrust(cmdArgs, os.Stdin, *socket, stdout, stderr)
	case "webhook":
		return runWebhook(cmdArgs, *socket, stdout, stderr)
	case "timeline":
		return runTimeline(cmdArgs, *socket, stdout, stderr)
	case "attach":
		return runAttach(cmdArgs, *socket, stdout, stderr)
	case "connect":
		return runConnect(cmdArgs, *socket, stdout, stderr)
	case "pause":
		return runPause(cmdArgs, *socket, stdout, stderr)
	case "ls":
		return runLs(cmdArgs, *socket, stdout, stderr)
	case "status":
		return runStatus(cmdArgs, *socket, stdout, stderr)
	case "jobs":
		return runJobs(cmdArgs, *socket, stdout, stderr)
	case "logs":
		return runLogs(cmdArgs, *socket, stdout, stderr)
	case "destroy":
		return runDestroy(cmdArgs, *socket, stdout, stderr)
	case "image":
		return runImage(cmdArgs, *socket, stdout, stderr)
	case "device":
		return runDevice(cmdArgs, stdout, stderr)
	case "provider":
		return runProvider(cmdArgs, *socket, stdout, stderr)
	case "box":
		return runBox(cmdArgs, *stateDir, stdout, stderr)
	case "vsock":
		return runVsock(cmdArgs, stdout, stderr)
	case "install":
		return runInstall(cmdArgs, stdout, stderr)
	case "uninstall":
		return runUninstall(cmdArgs, stdout, stderr)
	case "version":
		if cliutil.MaybeHelp(cmdArgs, "version", commandHelp, stdout) {
			return 0
		}
		if len(cmdArgs) == 1 && cmdArgs[0] == "--json" {
			info := handshake.New(resolvedVersion())
			data, _ := json.Marshal(info)
			fmt.Fprintf(stdout, "%s\n", data)
			return 0
		}
		if len(cmdArgs) != 0 {
			fmt.Fprintln(stderr, "usage: pluto version [--json]")
			return 2
		}
		fmt.Fprintf(stdout, "pluto %s\n", resolvedVersion())
		return 0
	case "help":
		if cliutil.MaybeHelp(cmdArgs, "help", commandHelp, stdout) {
			return 0
		}
		if len(cmdArgs) == 0 {
			usage(stdout)
			return 0
		}
		name := strings.Join(cmdArgs, " ")
		if text, ok := commandHelp(name); ok {
			fmt.Fprint(stdout, text)
			return 0
		}
		return unknownCommand(name, stderr)
	default:
		return unknownCommand(cmd, stderr)
	}
}

// isRunManagerInvocation reports whether `pluto run ...` names the run manager
// rather than a declared job. The bare one-token form (`pluto run ls`) is the
// manager only when the current worktree does not declare a job of that name:
// ADR 0012 keeps the pre-M5 meaning of a successful existing invocation, so a
// declared job named ls/show/logs still runs. The manager's own shapes — a
// run id after show/logs, or flags after ls — always select the manager.
func isRunManagerInvocation(args []string) bool {
	if len(args) == 0 || !isRunManagerSubcommand(args[0]) {
		return false
	}
	if len(args) == 1 && currentWorktreeDeclaresJob(args[0]) {
		return false
	}
	return true
}

func isRunManagerSubcommand(name string) bool {
	return name == "ls" || name == "show" || name == "logs"
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
		box, err := c.Box(target)
		if errors.Is(err, client.ErrNotFound) {
			return nil, &hintError{Err: err, Next: []string{"list boxes with 'pluto ls'"}}
		}
		return box, err
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
					return nil, &hintError{
						Err:  fmt.Errorf("box id prefix %q matches more than one box", target),
						Next: []string{"use a longer prefix, or list boxes with 'pluto ls'"},
					}
				}
				match = b
			}
		}
		if match != nil {
			return match, nil
		}
		return nil, &hintError{Err: fmt.Errorf("no box with id prefix %q", target), Next: []string{"list boxes with 'pluto ls'"}}
	}
	// The exact project@branch form is the unambiguous managed target.
	if i := strings.Index(target, "@"); i > 0 {
		project, branch := workspace.SanitizeName(target[:i]), target[i+1:]
		if project != "" && branch != "" {
			for _, b := range list.Boxes {
				if b.Project == project && b.Branch == branch {
					return b, nil
				}
			}
		}
	}
	// A managed worktree key or path names its box by checkout.
	if wt, err := workspace.Default().FindWorktree(target); err == nil {
		for _, b := range list.Boxes {
			if filepath.Clean(b.Worktree) == filepath.Clean(wt.Path) {
				return b, nil
			}
		}
		return nil, &hintError{Err: fmt.Errorf("no box for worktree %s", wt.Path), Next: []string{"create it with 'pluto up'"}}
	}
	return nil, &hintError{Err: fmt.Errorf("no box for worktree %s", target), Next: []string{"create it with 'pluto up'"}}
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

// fail prints a failure with its next steps. Daemon and agent facts pass
// through as the first line; the CLI adds curated hints where it has them and
// a generic fallback otherwise (ADR 0009).
func fail(stderr io.Writer, err error, next ...string) int {
	if errors.Is(err, client.ErrUnreachable) {
		return failText(stderr, err, "start the daemon with 'pluto daemon' or install it with 'pluto install'")
	}
	var hinted *hintError
	if errors.As(err, &hinted) {
		next = append(next, hinted.Next...)
	}
	return failText(stderr, err, next...)
}

// isContractError reports whether a failure came from the worktree's
// .pluto.toml: a local load, or the daemon's contract fact on the wire.
func isContractError(err error) bool {
	if errors.Is(err, contract.ErrInvalid) {
		return true
	}
	var httpErr *client.HTTPError
	return errors.As(err, &httpErr) && httpErr.Contract
}

// contractRunHint is the way back from a broken contract: fix it and run the
// command again (ADR 0009, docs/contract.md). Empty for unrelated failures.
func contractRunHint(err error, cmd string) []string {
	if !isContractError(err) {
		return nil
	}
	return []string{fmt.Sprintf("fix the contract and run '%s' again", cmd)}
}

// isSessionError reports whether the daemon rejected an attach because the
// box's worktree does not declare the named session.
func isSessionError(err error) bool {
	var httpErr *client.HTTPError
	return errors.As(err, &httpErr) && httpErr.Session
}

// attachHints names the next step for an attach failure: a broken contract is
// an edit, an unknown session is a status look (ADR 0009).
func attachHints(err error, target string) []string {
	hints := contractRunHint(err, "pluto attach")
	if isSessionError(err) {
		hints = append(hints, fmt.Sprintf("list sessions with 'pluto status %s'", target))
	}
	return hints
}

// failText prints the `pluto:` line and the next steps, falling back to the
// daemon log when nothing more specific is known. This is the single
// definition of the fallback; the client's failures always name a step.
func failText(stderr io.Writer, err error, next ...string) int {
	if len(next) == 0 {
		next = []string{"check the daemon log with 'journalctl --user -u pluto -n 50' and retry"}
	}
	return cliutil.FailText(stderr, err, next...)
}

// hintError is an error with CLI-side next steps attached.
type hintError = cliutil.HintError
