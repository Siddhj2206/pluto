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

// runDeviceAdd saves a nickname, then probes the target with `ssh <target>
// pluto version`. A target that does not answer is a warning, not a failure:
// it may simply be offline, and the typo the warning catches is still worth
// fixing later.
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
	if err := devices.Verify(context.Background(), devices.SSH{}, target); err != nil {
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
