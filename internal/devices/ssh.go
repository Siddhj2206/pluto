package devices

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/shquote"
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

// HostOptions is the ssh trust boundary every route shares: BatchMode never
// prompts, so a missing key fails instead of hanging; the connect timeout
// bounds a dead host; StrictHostKeyChecking=accept-new accepts a host key on
// first use but a changed key still fails. There is deliberately no
// StrictHostKeyChecking=no and no throw-away known_hosts, which would silently
// accept a swapped host key on every connection. Callers keep their own
// timeout: a probe only gates a warning, while a command's failure is the
// user's.
func HostOptions(connectTimeout time.Duration) []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(connectTimeout/time.Second)),
		"-o", "StrictHostKeyChecking=accept-new",
	}
}

// sshArgs builds the ssh invocation: non-interactive, bounded connect, the
// target fenced off from option parsing, and each remote word shell-quoted
// because ssh joins the command for the remote shell. The connect timeout is
// tighter than the exec path's 10s because a probe only gates a warning.
func sshArgs(target string, argv []string) []string {
	args := append(HostOptions(5*time.Second), "--", target)
	for _, arg := range argv {
		args = append(args, shquote.Quote(arg))
	}
	return args
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
