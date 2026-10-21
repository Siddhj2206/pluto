package remotecli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/devices"
)

// RunDevice manages the client-side registry of saved ssh destinations. It
// needs no daemon: the registry is a file under the user config directory.
func RunDevice(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelpAtStart(args, "device", commandHelp, stdout) {
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pluto device <add <nickname> <user@host>|ls|rm <nickname>>")
		return 2
	}
	switch args[0] {
	case "add":
		return runDeviceAdd(args[1:], stdout, stderr)
	case "ls":
		return runDeviceLs(args[1:], stdout, stderr)
	case "rm":
		return runDeviceRm(args[1:], stdout, stderr)
	default:
		return unknownSubcommand(stderr, "device", args[0], []string{"add", "ls", "rm"})
	}
}

// runDeviceAdd saves a nickname, then makes the host usable without a
// password: it checks for a key, offers to authorize one with the account
// password when the host refuses the key, and records where pluto lives so
// later commands do not depend on the PATH a non-interactive ssh gets. A
// target that still does not answer is a warning, not a failure: it may simply
// be offline, and the typo the warning catches is still worth fixing later.
func runDeviceAdd(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "device add", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device add", flag.ContinueOnError)
	if code := cliutil.ParseCommand(fs, args, stderr, "usage: pluto device add <nickname> <user@host>"); code != 0 {
		return code
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: pluto device add <nickname> <user@host>")
		return 2
	}
	nickname, target := fs.Arg(0), fs.Arg(1)
	reg, err := openDevices()
	if err != nil {
		return fail(stderr, err, deviceFixHint())
	}
	if err := reg.Add(nickname, target); err != nil {
		if errors.Is(err, devices.ErrInvalid) {
			fmt.Fprintf(stderr, "pluto: %v\n", err)
			fmt.Fprintln(stderr, "usage: pluto device add <nickname> <user@host>")
			return 2
		}
		return fail(stderr, err, deviceFixHint())
	}
	fmt.Fprintf(stdout, "saved device %s (%s)\n", nickname, target)

	ctx := context.Background()
	path, err := devices.Locate(ctx, target)
	if errors.Is(err, devices.ErrAuth) {
		fmt.Fprintf(stderr, "installing an ssh key on %s (enter the account password once)\n", target)
		pub, keyErr := devices.PublicKey()
		if keyErr == nil {
			keyErr = devices.InstallKey(target, pub, stdout, stderr)
		}
		if keyErr != nil {
			fmt.Fprintf(stderr, "warning: %v\n", keyErr)
			fmt.Fprintln(stderr, "next: fix ssh access, then run 'pluto device add' again")
			return 0
		}
		path, err = devices.Locate(ctx, target)
	}
	switch {
	case err == nil:
		if setErr := reg.SetRemotePath(nickname, path); setErr != nil {
			return fail(stderr, setErr, deviceFixHint())
		}
		fmt.Fprintf(stdout, "connected: pluto at %s\n", path)
	case errors.Is(err, devices.ErrPlutoMissing):
		fmt.Fprintf(stderr, "warning: %v\n", err)
		fmt.Fprintf(stderr, "next: install pluto on %s at ~/.local/bin/pluto or on its ssh PATH\n", target)
	default:
		fmt.Fprintf(stderr, "warning: %v\n", err)
	}
	return 0
}

func runDeviceLs(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "device ls", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device ls", flag.ContinueOnError)
	if code := cliutil.ParseCommand(fs, args, stderr, "usage: pluto device ls"); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: pluto device ls")
		return 2
	}
	reg, err := openDevices()
	if err != nil {
		return fail(stderr, err, deviceFixHint())
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NICKNAME\tDESTINATION")
	for _, d := range reg.List() {
		fmt.Fprintf(w, "%s\t%s\n", d.Nickname, d.Target)
	}
	w.Flush()
	return 0
}

func runDeviceRm(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "device rm", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device rm", flag.ContinueOnError)
	if code := cliutil.ParseCommand(fs, args, stderr, "usage: pluto device rm <nickname>"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto device rm <nickname>")
		return 2
	}
	nickname := fs.Arg(0)
	reg, err := openDevices()
	if err != nil {
		return fail(stderr, err, deviceFixHint())
	}
	if err := reg.Remove(nickname); err != nil {
		return fail(stderr, err, "list saved devices with 'pluto device ls'")
	}
	fmt.Fprintf(stdout, "removed device %s\n", nickname)
	return 0
}
