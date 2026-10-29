package remotecli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/handshake"
)

// RunClient is the Windows client entry point. The Windows binary links no
// host packages, so instead of starting a daemon it drives a Linux host over
// SSH: `device ...` manages the saved-device registry locally, `version` and
// help are local, and every other command requires --device and is replayed
// on the host exactly as the Linux CLI replays it. An explicit --device wins
// over the local commands, as it does on Linux.
func RunClient(args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("pluto", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.Usage = func() {}
	// The defaults are empty because the client only forwards flags the user
	// set explicitly; the host CLI supplies its own defaults.
	global.String("socket", "", "daemon unix socket on the host")
	global.String("state-dir", "", "state directory on the host (daemon only)")
	device := global.String("device", "", "saved device nickname or user@host ssh target")
	version := global.Bool("version", false, "print the version and exit")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			clientUsage(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		clientUsage(stderr)
		return 2
	}
	// --version is an alias of the version command (ADR 0011).
	if *version {
		fmt.Fprintf(stdout, "pluto %s\n", resolveVersion())
		return 0
	}
	rest := global.Args()
	deviceSet := false
	global.Visit(func(f *flag.Flag) {
		if f.Name == "device" {
			deviceSet = true
		}
	})
	if deviceSet {
		return Run(*device, cliutil.RemoteCommand(global), stdout, stderr)
	}
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "pluto: no command given")
		clientUsage(stderr)
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "version":
		if len(cmdArgs) == 1 && cmdArgs[0] == "--json" {
			info := handshake.New(resolveVersion())
			data, _ := json.Marshal(info)
			fmt.Fprintf(stdout, "%s\n", data)
			return 0
		}
		if len(cmdArgs) != 0 {
			fmt.Fprintln(stderr, "usage: pluto version [--json]")
			return 2
		}
		fmt.Fprintf(stdout, "pluto %s\n", resolveVersion())
		return 0
	case "device":
		return RunDevice(cmdArgs, stdout, stderr)
	case "help":
		return runHelp(cmdArgs, stdout)
	}
	return usageDeviceRequired(stderr)
}

// runHelp answers `pluto help [command]`: a client command prints its details,
// anything else prints the client help plus the way to a host command's help.
func runHelp(args []string, stdout io.Writer) int {
	if len(args) > 0 {
		name := strings.Join(args, " ")
		if text, ok := commandHelp(name); ok {
			fmt.Fprint(stdout, text)
			return 0
		}
		clientUsage(stdout)
		fmt.Fprintf(stdout, "\nnext: for a host command, run 'pluto --device <nickname> help %s'\n", name)
		return 0
	}
	clientUsage(stdout)
	return 0
}

// usageDeviceRequired explains that a host command needs a device, and that
// the Windows client reaches the host over SSH.
func usageDeviceRequired(stderr io.Writer) int {
	fmt.Fprintln(stderr, "pluto: host commands need --device")
	fmt.Fprintln(stderr, "usage: pluto [--device NAME|user@host] <command> [args]")
	fmt.Fprintln(stderr, "next: save a Linux host as a device with 'pluto device add <nickname> <user@host>', then run 'pluto --device <nickname> <command>'")
	fmt.Fprintln(stderr, "next: this client drives a Linux Pluto host over SSH and has no local daemon")
	return 2
}
