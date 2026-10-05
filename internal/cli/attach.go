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
)

// runAttach opens an ssh session into a box, waking it first. With a
// command after "--" it runs that command instead of an interactive shell.
func runAttach(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "attach", stdout) {
		return 0
	}
	target := ""
	var command []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			command = args[i+1:]
			break
		}
		if target != "" {
			fmt.Fprintln(stderr, "usage: pluto attach [box-id|worktree] [-- command...]")
			return 2
		}
		target = args[i]
	}
	if target == "" {
		dir, err := os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
		target = dir
	}

	box, err := resolveBox(client.New(socket), target)
	if err != nil {
		return fail(stderr, err)
	}
	info, err := client.New(socket).AttachBox(box.ID)
	if err != nil {
		return fail(stderr, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(stderr, err)
	}

	cmd := exec.Command("ssh", attachArgs(*info, exe, command)...)
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

// attachArgs builds the ssh invocation for a box. The pluto binary itself
// proxies vsock, so attach needs no extra helper on PATH.
func attachArgs(info api.AttachInfo, exe string, command []string) []string {
	proxy := shellQuote(exe) + " vsock connect " + shellQuote(info.UDS) + " " + strconv.FormatUint(uint64(info.Port), 10)
	args := []string{
		"-i", info.Key,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ProxyCommand=" + proxy,
		info.User + "@box",
	}
	return append(args, command...)
}

// shellQuote quotes s for /bin/sh when ssh's ProxyCommand shell would split
// or interpret it.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`;&|<>()*?[]{}~#!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
