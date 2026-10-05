package devices

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// VerifyTimeout bounds a probe: a dead host must not hang `device add`.
const VerifyTimeout = 10 * time.Second

// Runner runs a command on a remote ssh destination. Tests inject a fake so
// they never touch the network.
type Runner interface {
	Run(ctx context.Context, target string, argv ...string) error
}

// SSH is the real Runner: the user's ssh binary handles authentication and
// configuration.
type SSH struct{}

// Run executes argv on target over ssh.
func (SSH) Run(ctx context.Context, target string, argv ...string) error {
	cmd := exec.CommandContext(ctx, "ssh", sshArgs(target, argv)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := lastLine(stderr.String()); msg != "" {
			return fmt.Errorf("ssh %s: %s", target, msg)
		}
		return fmt.Errorf("ssh %s: %w", target, err)
	}
	return nil
}

// Verify checks that target answers `pluto version`. A failure is returned,
// not fatal: the caller warns and keeps the saved device.
func Verify(ctx context.Context, runner Runner, target string) error {
	ctx, cancel := context.WithTimeout(ctx, VerifyTimeout)
	defer cancel()
	if err := runner.Run(ctx, target, "pluto", "version"); err != nil {
		return fmt.Errorf("%s did not answer: %w", target, err)
	}
	return nil
}

// sshArgs builds the ssh invocation: non-interactive, bounded connect, the
// target fenced off from option parsing, and each remote word shell-quoted
// because ssh joins the command for the remote shell.
func sshArgs(target string, argv []string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=accept-new",
		"--", target,
	}
	for _, arg := range argv {
		args = append(args, shellQuote(arg))
	}
	return args
}

// shellQuote quotes a word for the remote /bin/sh, only when needed.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`;&|<>()*?[]{}~#!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// lastLine returns the last non-empty line of s, so a warning carries ssh's
// actual complaint rather than a banner.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
