// Package remotecli is the client half of the pluto CLI: the saved-device
// registry commands and the SSH remote-exec path. It is shared by the Linux
// binary and the Windows client and deliberately imports none of the
// Linux-only host packages (state, daemon, vsock, and friends), so the same
// code cross-compiles to Windows.
package remotecli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/clientcontext"
	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/console"
	"github.com/Siddhj2206/pluto/internal/devices"
	"github.com/Siddhj2206/pluto/internal/shquote"
)

// Run re-executes the command on a device: it resolves the --device value to
// an ssh destination and the pluto path learned there, then runs
// `pluto <argv>` remotely, streaming stdio and returning the remote exit code.
// No local daemon is involved.
func Run(deviceName string, argv []string, stdout, stderr io.Writer) int {
	if deviceName == "" || len(argv) == 0 {
		clientUsage(stderr)
		return 2
	}
	target, remote, err := resolveDevice(deviceName)
	if err != nil {
		if errors.Is(err, errUnknownDevice) {
			return fail(stderr, err, "list saved devices with 'pluto device ls'")
		}
		return fail(stderr, err, deviceFixHint())
	}
	code, err := execRemote(target, remote, argv, clientContextValue(), console.IsTerminal(os.Stdin), os.Stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pluto: ssh: %v\n", err)
		fmt.Fprintf(stderr, "next: check the connection with 'ssh %s pluto version'\n", target)
		return 1
	}
	return code
}

// clientContextValue describes the local checkout for the host's default
// target resolution. It returns "" when the directory is not a project
// checkout. Detection is best effort: a host command still runs when the
// client cannot describe its checkout, and the host names the next step.
func clientContextValue() string {
	ctx, err := clientcontext.Detect(".")
	if err != nil || ctx == nil {
		return ""
	}
	data, err := json.Marshal(ctx)
	if err != nil {
		return ""
	}
	return string(data)
}

// errUnknownDevice marks a --device nickname with no saved entry, so the
// caller can name the right next step.
var errUnknownDevice = errors.New("unknown device")

// resolveDevice turns a --device value into an ssh destination and the pluto
// binary to run there. A value with an '@' is a raw target and works without
// saving; anything else must be a saved nickname. A saved device normally has
// a remote path learned by `device add`; an older entry falls back to the bare
// name.
func resolveDevice(name string) (target, remote string, err error) {
	if strings.Contains(name, "@") {
		return name, "pluto", nil
	}
	reg, err := openDevices()
	if err != nil {
		return "", "", err
	}
	target, ok := reg.Resolve(name)
	if !ok {
		return "", "", fmt.Errorf("%w %q", errUnknownDevice, name)
	}
	remote = "pluto"
	if path, ok := reg.RemotePath(name); ok {
		remote = path
	}
	return target, remote, nil
}

// execRemote runs argv as the remote pluto over ssh with the caller's stdio,
// returning the remote exit code. A tty allocates a remote terminal (`ssh
// -t`) so interactive commands such as attach work. A non-empty context is
// passed as PLUTO_CLIENT_CONTEXT through env(1), so it reaches the remote
// process without relying on the host sshd's AcceptEnv configuration.
func execRemote(target, remote string, argv []string, context string, tty bool, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command("ssh", remoteSSHArgs(target, remote, argv, context, tty)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

// remoteSSHArgs builds the ssh invocation that re-runs pluto on target. Each
// remote word is quoted for the remote shell because ssh joins the command
// into one string; the target is fenced off from option parsing. The trust
// options come from devices.HostOptions so the ssh boundary has one
// definition; the exec path gets a longer connect timeout than the probe.
func remoteSSHArgs(target, remote string, argv []string, context string, tty bool) []string {
	args := []string{}
	if tty {
		args = append(args, "-t")
	}
	args = append(args, devices.HostOptions(10*time.Second)...)
	args = append(args, "--", target)
	if context != "" {
		args = append(args, "env", clientcontext.Env+"="+shquote.Quote(context))
	}
	args = append(args, shquote.Quote(remote))
	for _, arg := range argv {
		args = append(args, shquote.Quote(arg))
	}
	return args
}

// openDevices loads the saved-device registry from its user config path.
func openDevices() (*devices.Registry, error) {
	path, err := devices.DefaultPath()
	if err != nil {
		return nil, err
	}
	return devices.Open(path)
}

// deviceFixHint names the registry file for a failure's next step.
func deviceFixHint() string {
	path, err := devices.DefaultPath()
	if err != nil {
		return "fix the devices file and retry"
	}
	return fmt.Sprintf("fix '%s' and retry", path)
}

// fail prints a failure in the CLI's voice followed by its next steps. Every
// client call site names a step, so there is no fallback here.
func fail(stderr io.Writer, err error, next ...string) int {
	return cliutil.FailText(stderr, err, next...)
}
