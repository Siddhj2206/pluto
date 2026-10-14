package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/shquote"
	"github.com/Siddhj2206/pluto/internal/state"
)

// attachUsage is the one-line usage printed with an attach usage error.
const attachUsage = "usage: pluto attach [box-id|worktree] [--watch] [--session NAME] [-- command...]"

// runAttach opens a terminal into a box, waking it first. A container box uses
// the daemon's shared, masked terminal: `--watch` streams it read-only and any
// other client may type. An earlier worktree box keeps the ssh path
// (ADR 0016 supersedes it; #150 retires it).
func runAttach(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "attach", stdout) {
		return 0
	}
	target, session, watch, command, err := parseAttachArgs(args)
	if err != nil {
		return usageError(stderr, err.Error(), attachUsage)
	}
	if target == "" {
		dir, err := os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
		target = dir
	}

	c := client.New(socket)
	box, err := resolveAttachBox(c, target)
	if err != nil {
		return fail(stderr, err)
	}
	if containerBox(box) {
		if session != "" || len(command) > 0 {
			return usageError(stderr, "the box terminal takes no --session or command", attachUsage)
		}
		return runTerminalAttach(c, box, watch, stdout, stderr)
	}
	if watch {
		return usageError(stderr, "--watch needs a box with a daemon terminal", attachUsage)
	}
	return runSSHAttach(c, box, session, command, stdout, stderr)
}

// parseAttachArgs reads attach's target, --watch, --session, and any command
// after "--".
func parseAttachArgs(args []string) (target, session string, watch bool, command []string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			command = args[i+1:]
			break
		}
		if arg == "--watch" {
			watch = true
			continue
		}
		if arg == "--session" {
			if i+1 >= len(args) {
				return "", "", false, nil, errors.New("--session needs a session name")
			}
			session = args[i+1]
			i++
			continue
		}
		if name, ok := strings.CutPrefix(arg, "--session="); ok {
			session = name
			continue
		}
		if target != "" {
			return "", "", false, nil, errors.New("attach takes one box")
		}
		target = arg
	}
	return target, session, watch, command, nil
}

// resolveAttachBox resolves an attach target through the container box
// lifecycle when it is available, falling back to the earlier worktree-based
// surface (#150 retires the latter).
func resolveAttachBox(c *client.Client, target string) (*state.Box, error) {
	if box, err := resolveContainerBox(c, target); err == nil {
		return box, nil
	}
	return resolveBox(c, target)
}

// containerBox reports whether a record belongs to the container box lifecycle
// rather than the earlier worktree-based path.
func containerBox(b *state.Box) bool { return b.WorkspacePath != "" || b.EngineID != "" }

// runTerminalAttach opens a box's daemon-owned terminal. The terminal is shared
// and masked; watch makes the connection read-only.
func runTerminalAttach(c *client.Client, box *state.Box, watch bool, stdout, stderr io.Writer) int {
	code, err := c.AttachTerminal(box.ID, watch, os.Stdin, stdout)
	if err != nil {
		return fail(stderr, err, fmt.Sprintf("check the box with 'pluto box status %s' and retry", short(box.ID)))
	}
	return code
}

// runSSHAttach opens the earlier ssh session into a worktree box.
func runSSHAttach(c *client.Client, box *state.Box, session string, command []string, stdout, stderr io.Writer) int {
	remote, err := attachCommand(session, command)
	if err != nil {
		return usageError(stderr, err.Error(), attachUsage)
	}
	info, err := c.AttachBox(box.ID, session)
	if err != nil {
		return fail(stderr, err, attachHints(err, short(box.ID))...)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(stderr, err)
	}

	cmd := exec.Command("ssh", attachArgs(*info, exe, remote, attachTTY(remote, isTerminal(os.Stdin)))...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "pluto: ssh: %v\n", err)
		fmt.Fprintf(stderr, "next: check the box with 'pluto status %s' and retry\n", short(box.ID))
		return 1
	}
	return 0
}

// attachCommand builds the remote command for an attach. A named session
// enters the declared tmux session without starting one; an explicit command
// passes through; the two ways of choosing the command are mutually exclusive.
func attachCommand(session string, command []string) ([]string, error) {
	if session == "" {
		return command, nil
	}
	if len(command) > 0 {
		return nil, errors.New("--session cannot be combined with an explicit command")
	}
	return []string{"tmux", "attach-session", "-t", session}, nil
}

// attachTTY decides whether the attach ssh invocation should request a remote
// pty. A remote command (--session or an explicit command) is run without a
// tty by default, and tmux then fails with "open terminal failed: not a
// terminal"; on an interactive stdin we ask ssh for one. A bare attach already
// gets an interactive shell, and adding -t there would change how a piped
// stdin behaves, so it is left alone.
func attachTTY(remote []string, stdinIsTTY bool) bool {
	return len(remote) > 0 && stdinIsTTY
}

// attachArgs builds the ssh invocation for a box. The pluto binary itself
// proxies vsock, so attach needs no extra helper on PATH. When tty is set the
// pty request comes first, before the destination.
func attachArgs(info api.AttachInfo, exe string, command []string, tty bool) []string {
	proxy := shquote.Quote(exe) + " vsock connect " + shquote.Quote(info.UDS) + " " + strconv.FormatUint(uint64(info.Port), 10)
	args := []string{}
	if tty {
		args = append(args, "-t")
	}
	args = append(args,
		"-i", info.Key,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ProxyCommand="+proxy,
		info.User+"@box",
	)
	return append(args, command...)
}
