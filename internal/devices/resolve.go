package devices

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/shquote"
)

// ErrConfig means OpenSSH could not resolve the destination's configuration.
var ErrConfig = errors.New("ssh config does not resolve")

// DefaultTimeouts bound every connection. Connect is short enough to fail
// visibly; keepalives keep a long attach from looking dead.
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultResolveTimeout = 5 * time.Second
)

// Resolve asks OpenSSH what a profile means: ssh -G prints the effective
// host, user, port, identity, and proxy without connecting. The profile keeps
// the alias; this result is display and diagnostic metadata.
func Resolve(ctx context.Context, ssh string, p Profile) (Resolved, error) {
	p.applyDefaults()
	ctx, cancel := context.WithTimeout(ctx, DefaultResolveTimeout)
	defer cancel()
	args := []string{"-G"}
	args = append(args, p.overrideArgs()...)
	args = append(args, "--", p.Destination)
	cmd := exec.CommandContext(ctx, ssh, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if line := lastLine(stderr.String()); line != "" {
			return Resolved{}, fmt.Errorf("%w: %s", ErrConfig, line)
		}
		return Resolved{}, fmt.Errorf("%w: %v", ErrConfig, err)
	}
	resolved, err := parseG(stdout.String())
	if err != nil {
		return Resolved{}, fmt.Errorf("%w: %v", ErrConfig, err)
	}
	resolved.Destination = p.Destination
	if p.ProxyJump != "" {
		resolved.ProxyJump = p.ProxyJump
	}
	if p.IdentityFile != "" {
		resolved.IdentityFile = p.IdentityFile
	}
	return resolved, nil
}

// parseG reads the `key value` lines ssh -G prints. Repeated keys keep the
// first value, except identityfile where the explicit key wins later.
func parseG(out string) (Resolved, error) {
	var r Resolved
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "host":
			if r.Host == "" {
				r.Host = value
			}
		case "hostname":
			r.Host = value
		case "user":
			r.User = value
		case "port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return Resolved{}, fmt.Errorf("bad port %q", value)
			}
			r.Port = port
		case "proxyjump":
			if value != "none" {
				r.ProxyJump = value
			}
		case "identityfile":
			if r.IdentityFile == "" {
				r.IdentityFile = value
			}
		}
	}
	if r.Host == "" {
		return Resolved{}, errors.New("ssh -G printed no hostname")
	}
	if r.Port == 0 {
		r.Port = 22
	}
	return r, nil
}

// Display is the one-line route shown in the connection card and doctor
// header.
func (r Resolved) Display() string {
	target := r.Host
	if r.User != "" {
		target = r.User + "@" + target
	}
	if r.Port != 0 && r.Port != 22 {
		target += ":" + strconv.Itoa(r.Port)
	}
	if r.ProxyJump != "" {
		target += " via " + r.ProxyJump
	}
	return target
}

// SSHOptions selects the non-route ssh behaviour for one invocation.
type SSHOptions struct {
	// TTY allocates a remote terminal (-tt). Non-interactive work uses -T.
	TTY bool
	// BatchMode forbids prompts. It is true for probes and key-only auth.
	BatchMode bool
	// Strict asks for StrictHostKeyChecking=yes. Otherwise the profile's
	// explicit accept-new applies.
	Strict bool
	// ConnectTimeout overrides the default connect bound.
	ConnectTimeout time.Duration
}

// TransportArgs is the invocation every non-interactive remote command uses:
// a bounded connect, keepalives, the profile's auth and trust mode, and only
// the route overrides the user actually set.
func (p Profile) TransportArgs(tty bool) []string {
	return p.SSHArgs(SSHOptions{
		TTY:       tty,
		BatchMode: p.Auth != AuthInteractive,
		Strict:    p.Trust == TrustStrict,
	})
}

// SSHArgs builds ssh's option list. The destination is fenced with `--`, so a
// raw target can never be read as an option.
func (p Profile) SSHArgs(o SSHOptions) []string {
	p.applyDefaults()
	timeout := o.ConnectTimeout
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	args := []string{}
	if o.TTY {
		args = append(args, "-tt")
	} else {
		args = append(args, "-T")
	}
	if o.BatchMode {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args,
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(timeout/time.Second)),
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	)
	if o.Strict {
		args = append(args, "-o", "StrictHostKeyChecking=yes")
	} else {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	if p.ControlMaster && runtime.GOOS != "windows" {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath=~/.ssh/pluto-%C",
			"-o", "ControlPersist=60s",
		)
	}
	args = append(args, p.overrideArgs()...)
	args = append(args, "--", p.Destination)
	return args
}

// overrideArgs are the explicit route escape hatches. Empty values are not
// passed, so ~/.ssh/config keeps winning.
func (p Profile) overrideArgs() []string {
	var args []string
	if p.Port > 0 {
		args = append(args, "-p", strconv.Itoa(p.Port))
	}
	if p.IdentityFile != "" {
		args = append(args, "-i", p.IdentityFile)
	}
	if p.ProxyJump != "" {
		args = append(args, "-J", p.ProxyJump)
	}
	return args
}

// RemoteCommand builds the single remote command string ssh execs. The host
// login shell only parses `sh -c '<quoted command>'`; the binary and every
// argv word inside are POSIX-quoted. This keeps a fish or csh login shell out
// of the way and avoids relying on ssh joining words itself.
func RemoteCommand(remote string, argv []string) []string {
	words := make([]string, 0, len(argv)+1)
	words = append(words, shquote.Quote(remote))
	for _, arg := range argv {
		words = append(words, shquote.Quote(arg))
	}
	return []string{"sh", "-c", shquote.QuoteAlways(strings.Join(words, " "))}
}

// Command is the ssh argv for one remote invocation: options, the fenced
// destination, then the sh -c wrapper.
func (p Profile) Command(o SSHOptions, remote string, argv []string) []string {
	args := p.SSHArgs(o)
	return append(args, RemoteCommand(remote, argv)...)
}
