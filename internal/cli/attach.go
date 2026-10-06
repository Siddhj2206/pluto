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
)

// attachUsage is the one-line usage printed with an attach usage error.
const attachUsage = "usage: pluto attach [box-id|worktree] [--session NAME] [-- command...]"

// runAttach opens an ssh session into a box, waking it first. With a
// command after "--" it runs that command instead of an interactive shell;
// with --session NAME it enters a declared tmux session.
func runAttach(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "attach", stdout) {
		return 0
	}
	target := ""
	session := ""
	var command []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			command = args[i+1:]
			break
		}
		if arg == "--session" {
			if i+1 >= len(args) {
				return usageError(stderr, "--session needs a session name", attachUsage)
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
			return usageError(stderr, "attach takes one box", attachUsage)
		}
		target = arg
	}
	if target == "" {
		dir, err := os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
		target = dir
	}

	remote, err := attachCommand(session, command)
	if err != nil {
		return usageError(stderr, err.Error(), attachUsage)
	}

	box, err := resolveBox(client.New(socket), target)
	if err != nil {
		return fail(stderr, err)
	}
	info, err := client.New(socket).AttachBox(box.ID, session)
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
