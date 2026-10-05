package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Siddhj2206/pluto/internal/devices"
)

// runDevice manages the client-side registry of saved ssh destinations. It
// needs no daemon: the registry is a file under the XDG config directory.
func runDevice(args []string, stdout, stderr io.Writer) int {
	if maybeHelpAtStart(args, "device", stdout) {
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
	if maybeHelp(args, "device add", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
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
	if maybeHelp(args, "device ls", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
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
	if maybeHelp(args, "device rm", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("device rm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
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
