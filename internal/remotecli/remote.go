// Package remotecli is the client half of the pluto CLI: the saved-device
// registry commands and the SSH remote-exec path. It is shared by the Linux
// binary and the Windows client and deliberately imports none of the
// Linux-only host packages (state, daemon, vsock, and friends), so the same
// code cross-compiles to Windows.
package remotecli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/console"
	"github.com/Siddhj2206/pluto/internal/devices"
)

// Run re-executes the command on a device: it resolves the --device value to
// an OpenSSH profile and the pluto path learned there, then runs
// `pluto <argv>` remotely, streaming stdio and returning the remote exit code.
// No local daemon is involved.
func Run(deviceName string, argv []string, stdout, stderr io.Writer) int {
	if deviceName == "" || len(argv) == 0 {
		clientUsage(stderr)
		return 2
	}
	profile, remote, err := resolveDevice(deviceName)
	if err != nil {
		if errors.Is(err, errUnknownDevice) {
			return fail(stderr, err, "list saved devices with 'pluto device ls'")
		}
		return fail(stderr, err, deviceFixHint())
	}
	interactive := len(argv) > 0 && argv[0] == "attach" && console.IsTerminal(os.Stdin)
	code, err := execRemote(profile, remote, argv, interactive, os.Stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pluto: ssh: %v\n", err)
		if strings.Contains(deviceName, "@") {
			fmt.Fprintf(stderr, "next: check the connection with 'ssh %s pluto version'\n", profile.Destination)
		} else {
			fmt.Fprintf(stderr, "next: check the connection with 'pluto device doctor %s'\n", deviceName)
		}
		return 1
	}
	return code
}

// errUnknownDevice marks a --device nickname with no saved entry, so the
// caller can name the right next step.
var errUnknownDevice = errors.New("unknown device")

// resolveDevice turns a --device value into a profile and the pluto binary to
// run there. A value with an '@' is a raw target and works without saving;
// anything else must be a saved nickname. A saved profile normally has a
// learned path; an older entry falls back to the bare name.
func resolveDevice(name string) (devices.Profile, string, error) {
	if strings.Contains(name, "@") {
		return devices.Profile{Nickname: name, Destination: name}, "pluto", nil
	}
	reg, err := openDevices()
	if err != nil {
		return devices.Profile{}, "", err
	}
	profile, ok := reg.Profile(name)
	if !ok {
		return devices.Profile{}, "", fmt.Errorf("%w %q", errUnknownDevice, name)
	}
	remote := "pluto"
	if path, ok := reg.RemotePath(name); ok {
		remote = path
	} else if c, _, err := loadDeviceCache(name); err == nil && c.Remote.Path != "" {
		remote = c.Remote.Path
	}
	return profile, remote, nil
}

// execRemote runs argv as the remote pluto over ssh with the caller's stdio,
// returning the remote exit code. A tty allocates a remote terminal (`ssh
// -tt`) so interactive commands such as attach work.
func execRemote(profile devices.Profile, remote string, argv []string, tty bool, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	opts := devices.SSHOptions{
		TTY:       tty,
		BatchMode: profile.Auth != devices.AuthInteractive,
		Strict:    profile.Trust == devices.TrustStrict,
	}
	ssh, err := devices.FindSSH()
	if err != nil {
		return 1, err
	}
	cmd := exec.Command(ssh, profile.Command(opts, remote, argv)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 1, err
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
