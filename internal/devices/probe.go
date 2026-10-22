package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/handshake"
	"github.com/Siddhj2206/pluto/internal/shquote"
)

// ErrPlatform means the host answered but is not Linux, which the daemon
// requires.
var ErrPlatform = errors.New("host is not Linux")

// ErrHandshake means the host has no usable pluto version answer.
var ErrHandshake = errors.New("pluto version handshake failed")

// ErrDaemon means the binary answered but the daemon is not reachable.
var ErrDaemon = errors.New("pluto daemon is not reachable")

// ErrImage means the daemon answered but has no base image.
var ErrImage = errors.New("no base image is imported")

// ScriptCommand is the ssh argv for one POSIX script run through sh -c. The
// script is always single-quoted so the remote login shell passes it whole.
func (p Profile) ScriptCommand(o SSHOptions, script string) []string {
	args := p.SSHArgs(o)
	return append(args, "sh", "-c", shquote.QuoteAlways(script))
}

// sshResult is one finished ssh invocation.
type sshResult struct {
	stdout string
	stderr string
	code   int
}

// runSSH executes an ssh argv with a bounded timeout and captures both
// streams separately.
func runSSH(ctx context.Context, ssh string, args []string, timeout time.Duration) (sshResult, error) {
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ssh, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := sshResult{stdout: stdout.String(), stderr: stderr.String()}
	if err == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.code = exitErr.ExitCode()
		return res, nil
	}
	if ctx.Err() != nil {
		return res, fmt.Errorf("ssh timed out after %s", timeout)
	}
	return res, err
}

// probeSystemScript reports the host facts the profile cares about without
// assuming the login shell is POSIX.
const probeSystemScript = `printf 'os=%s\n' "$(uname -s 2>/dev/null)"
printf 'arch=%s\n' "$(uname -m 2>/dev/null)"
printf 'shell=%s\n' "${SHELL:-}"
printf 'home=%s\n' "${HOME:-}"`

// ProbeSystem runs the one remote shell probe and parses its key=value lines.
func ProbeSystem(ctx context.Context, ssh string, p Profile) (Remote, error) {
	args := p.ScriptCommand(SSHOptions{BatchMode: true}, probeSystemScript)
	res, err := runSSH(ctx, ssh, args, 15*time.Second)
	if err != nil {
		return Remote{}, err
	}
	if res.code != 0 {
		if line := lastLine(res.stderr); line != "" {
			return Remote{}, fmt.Errorf("%w: %s", ErrShell, line)
		}
		return Remote{}, fmt.Errorf("%w: ssh exited %d", ErrShell, res.code)
	}
	var r Remote
	for _, line := range strings.Split(res.stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "os":
			r.OS = value
		case "arch":
			r.Arch = value
		case "shell":
			r.Shell = value
		}
	}
	if r.OS == "" {
		return Remote{}, fmt.Errorf("%w: probe printed no OS", ErrShell)
	}
	if !strings.EqualFold(r.OS, "Linux") {
		return r, fmt.Errorf("%w: host reports %s", ErrPlatform, r.OS)
	}
	return r, nil
}

// ErrShell means the host accepted the login but could not run sh.
var ErrShell = errors.New("remote shell is not usable")

// executableCheck is a script that prints path only when it is an executable
// regular file. Aliases and shell functions fail the file test.
func executableCheck(path string) string {
	q := shquote.Quote(path)
	return `p=` + q + `; if [ -f "$p" ] && [ -x "$p" ]; then printf '%s\n' "$p"; else exit 127; fi`
}

func checkExecutable(ctx context.Context, ssh string, p Profile, path string) bool {
	res, err := runSSH(ctx, ssh, p.ScriptCommand(SSHOptions{BatchMode: true}, executableCheck(path)), 10*time.Second)
	return err == nil && res.code == 0 && strings.TrimSpace(res.stdout) == path
}

const nonInteractiveLookup = `p=$(command -v pluto 2>/dev/null || true); if [ -n "$p" ] && [ -f "$p" ] && [ -x "$p" ]; then printf '%s\n' "$p"; else exit 127; fi`

const loginShellLookup = `p=$(sh -lc "command -v pluto 2>/dev/null" 2>/dev/null || true); if [ -n "$p" ] && [ -f "$p" ] && [ -x "$p" ]; then printf '%s\n' "$p"; else exit 127; fi`

const knownPathLookup = `for p in "$HOME/.local/bin/pluto" "/usr/local/bin/pluto" "/usr/bin/pluto" "$HOME/.cargo/bin/pluto" "$HOME/bin/pluto"; do if [ -f "$p" ] && [ -x "$p" ]; then printf '%s\n' "$p"; exit 0; fi; done; exit 127`

// DiscoverBinary finds the host's pluto in the documented order: explicit
// path, cache, non-interactive command -v, login-shell command -v, known
// absolute paths. Every candidate must be an executable regular file.
func DiscoverBinary(ctx context.Context, ssh string, p Profile, cached string) (path, source string, err error) {
	p.applyDefaults()
	if p.RemotePath != RemotePathAuto {
		if checkExecutable(ctx, ssh, p, p.RemotePath) {
			return p.RemotePath, "explicit", nil
		}
		return "", "", fmt.Errorf("%w: explicit remote path %s is not an executable file", ErrPlutoMissing, p.RemotePath)
	}
	if cached != "" && checkExecutable(ctx, ssh, p, cached) {
		return cached, "cache", nil
	}
	for _, step := range []struct {
		source string
		script string
	}{
		{"command-v", nonInteractiveLookup},
		{"login-shell", loginShellLookup},
		{"known-path", knownPathLookup},
	} {
		res, runErr := runSSH(ctx, ssh, p.ScriptCommand(SSHOptions{BatchMode: true}, step.script), 10*time.Second)
		if runErr != nil || res.code != 0 {
			continue
		}
		candidate := strings.TrimSpace(res.stdout)
		if validRemotePath(candidate) {
			return candidate, step.source, nil
		}
	}
	return "", "", fmt.Errorf("%w: no executable pluto on the host", ErrPlutoMissing)
}

// Handshake asks the host binary for its machine-readable version. A legacy
// text version is accepted with legacy=true so the caller can warn instead of
// rejecting an otherwise usable host.
func Handshake(ctx context.Context, ssh string, p Profile, path string) (info handshake.Info, legacy bool, err error) {
	if path == "" {
		return handshake.Info{}, false, fmt.Errorf("%w: no binary path", ErrPlutoMissing)
	}
	args := p.Command(SSHOptions{BatchMode: true}, path, []string{"version", "--json"})
	res, runErr := runSSH(ctx, ssh, args, 15*time.Second)
	if runErr == nil && res.code == 0 {
		if json.Unmarshal([]byte(res.stdout), &info) == nil && info.Version != "" {
			return info, false, nil
		}
	}
	// Legacy binaries print `pluto <version>` and do not know --json.
	args = p.Command(SSHOptions{BatchMode: true}, path, []string{"version"})
	res, runErr = runSSH(ctx, ssh, args, 15*time.Second)
	if runErr == nil && res.code == 0 {
		if version := parseTextVersion(res.stdout); version != "" {
			return handshake.Info{Version: version, Protocol: 0}, true, nil
		}
	}
	line := lastLine(res.stderr)
	if line == "" && runErr != nil {
		line = runErr.Error()
	}
	if line == "" {
		line = strings.TrimSpace(res.stdout)
	}
	return handshake.Info{}, false, fmt.Errorf("%w: %s", ErrHandshake, line)
}

// parseTextVersion reads `pluto 0.1.5` from a legacy binary.
func parseTextVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 2 && fields[0] == "pluto" {
			return fields[1]
		}
	}
	return ""
}

// ProbeConnection tests host key and auth in one non-interactive attempt and
// classifies the result. allowPrompt permits the account's password prompt
// for the one key-install path; doctor and probes pass false.
func ProbeConnection(ctx context.Context, ssh string, p Profile, allowPrompt bool) (hostkey, auth Hop) {
	o := SSHOptions{BatchMode: !allowPrompt || p.Auth == AuthKey, Strict: p.Trust == TrustStrict}
	res, err := runSSH(ctx, ssh, p.ScriptCommand(o, "printf ok"), 20*time.Second)
	return classifyProbe(res, err)
}

// DaemonReachable runs the read-only `ls` command. A reachable daemon answers
// it; a down daemon does not.
func DaemonReachable(ctx context.Context, ssh string, p Profile, path string) (bool, error) {
	args := p.Command(SSHOptions{BatchMode: true}, path, []string{"ls"})
	res, err := runSSH(ctx, ssh, args, 20*time.Second)
	if err != nil {
		return false, err
	}
	if res.code != 0 {
		return false, fmt.Errorf("%w: %s", ErrDaemon, lastLine(res.stderr))
	}
	return true, nil
}

// ImageAvailable checks the daemon's imported images with a read-only
// `image ls`. The header alone means none are imported.
func ImageAvailable(ctx context.Context, ssh string, p Profile, path string) (bool, error) {
	args := p.Command(SSHOptions{BatchMode: true}, path, []string{"image", "ls"})
	res, err := runSSH(ctx, ssh, args, 20*time.Second)
	if err != nil {
		return false, err
	}
	if res.code != 0 {
		return false, fmt.Errorf("%w: %s", ErrImage, lastLine(res.stderr))
	}
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	return lines > 1, nil
}
