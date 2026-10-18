// Package remotecli is the client half of the pluto CLI: the saved-device
// registry commands and the SSH remote-exec path. It is shared by the Linux
// binary and the Windows client and deliberately imports none of the
// Linux-only host packages (state, daemon, vsock, and friends), so the same
// code cross-compiles to Windows.
package remotecli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Siddhj2206/pluto/internal/console"
	"github.com/Siddhj2206/pluto/internal/devices"
	"github.com/Siddhj2206/pluto/internal/shquote"
)

// Run re-executes the command on a device: it resolves the --device value to
// an ssh destination and runs `pluto <argv>` there, streaming stdio and
// returning the remote exit code. No local daemon is involved.
func Run(deviceName string, argv []string, stdout, stderr io.Writer) int {
	if deviceName == "" || len(argv) == 0 {
		clientUsage(stderr)
		return 2
	}
	target, err := resolveDevice(deviceName)
	if err != nil {
		if errors.Is(err, errUnknownDevice) {
			return fail(stderr, err, "list saved devices with 'pluto device ls'")
		}
		return fail(stderr, err, deviceFixHint())
	}
	code, err := execRemote(target, argv, console.IsTerminal(os.Stdin), os.Stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pluto: ssh: %v\n", err)
		fmt.Fprintf(stderr, "next: check the connection with 'ssh %s pluto version'\n", target)
		return 1
	}
	return code
}

// remoteCommand is the command replayed on a device: the global flags that
// were set explicitly (minus --device, which selects the machine) followed by
// the command and its arguments exactly as given, `--` separators included.
func remoteCommand(global *flag.FlagSet) []string {
	var out []string
	global.Visit(func(f *flag.Flag) {
		if f.Name == "device" {
			return
		}
		out = append(out, "--"+f.Name, f.Value.String())
	})
	return append(out, global.Args()...)
}

// errUnknownDevice marks a --device nickname with no saved entry, so the
// caller can name the right next step.
var errUnknownDevice = errors.New("unknown device")

// resolveDevice turns a --device value into an ssh destination. A value with
// an '@' is a raw target and works without saving; anything else must be a
// saved nickname.
func resolveDevice(name string) (string, error) {
	if strings.Contains(name, "@") {
		return name, nil
	}
	reg, err := openDevices()
	if err != nil {
		return "", err
	}
	target, ok := reg.Resolve(name)
	if !ok {
		return "", fmt.Errorf("%w %q", errUnknownDevice, name)
	}
	return target, nil
}

// execRemote runs argv as the remote pluto over ssh with the caller's stdio,
// returning the remote exit code. A tty allocates a remote terminal (`ssh
// -t`) so interactive commands such as attach work.
func execRemote(target string, argv []string, tty bool, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command("ssh", remoteSSHArgs(target, argv, tty)...)
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
// into one string; the target is fenced off from option parsing.
//
// The fixed options are the trust boundary. BatchMode never prompts, so a
// missing key fails instead of hanging; ConnectTimeout bounds a dead host;
// StrictHostKeyChecking=accept-new accepts a host key on first use but a
// changed key still fails. There is deliberately no StrictHostKeyChecking=no
// and no throw-away known_hosts, which would silently accept a swapped host
// key on every connection.
func remoteSSHArgs(target string, argv []string, tty bool) []string {
	args := []string{}
	if tty {
		args = append(args, "-t")
	}
	args = append(args,
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		"--", target, "pluto",
	)
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

// fail prints a failure in the CLI's voice followed by its next steps. The
// remote and device paths always have a curated next step, so the fallback
// stays a generic retry note.
func fail(stderr io.Writer, err error, next ...string) int {
	fmt.Fprintf(stderr, "pluto: %v\n", err)
	if len(next) == 0 {
		next = []string{"check the connection and retry"}
	}
	for _, step := range next {
		fmt.Fprintf(stderr, "next: %s\n", step)
	}
	return 1
}
