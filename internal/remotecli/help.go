package remotecli

import (
	"fmt"
	"io"
	"strings"

	"github.com/Siddhj2206/pluto/internal/cliutil"
)

// clientUsageText is the Windows client's top-level help. The client has no
// daemon, so it lists the device registry commands and points every host
// command at --device.
const clientUsageText = `pluto client: drive a Linux Pluto host over SSH

usage: pluto [--device NAME|user@host] <command> [args]

This client has no daemon. Save a Linux host as a device, then run host
commands with --device:

  pluto device add <nickname> <user@host>
  pluto --device <nickname> up
  pluto --device <nickname> status
  pluto --device <nickname> run test
  pluto --device <nickname> attach
  pluto --device <nickname> ls
  pluto --device <nickname> task ls
  pluto --device <nickname> destroy <box> --yes

From a project checkout, up, pause, status, run, attach, logs, job ls, and
task create use the checkout's project and branch, so no box id or host path
is needed. An explicit box id, <project>@<branch>, or --worktree <host key or
path> always wins. The client sends the checkout's remote, project, branch,
HEAD, and dirty state as metadata, never as authorization, and never sends a
local path as a host worktree path.

device add is the whole setup: it saves the nickname, asks for the account
password once when the host does not accept your key yet, installs the key,
and records where pluto lives on the host. The first connection accepts the
host key; a later key change is rejected.

Saved devices: pluto device ls. Config: %AppData%\pluto\devices.toml.

flags:
  --device NAME|user@host   saved device nickname or raw ssh target
  --socket PATH             forwarded to the host CLI
  --state-dir PATH          forwarded to the host CLI
  --version                 print the version
  help [command]            show this help
`

// deviceUsageLines is the `pluto device` usage, split across its subcommands.
var deviceUsageLines = []string{
	"pluto device add <nickname> <user@host>",
	"pluto device ls",
	"pluto device rm <nickname>",
}

// deviceDoc is one device command's help entry. It mirrors the entries in
// internal/cli's help table so `pluto device -h` reads the same on both
// platforms.
type deviceDoc struct {
	name     string
	usage    []string
	details  string
	examples []string
}

var deviceDocs = []deviceDoc{
	{
		name:    "device",
		usage:   deviceUsageLines,
		details: "Manage the client-side registry of saved ssh destinations used by the\nglobal --device flag. It lives at ~/.config/pluto/devices.toml and holds no\nsecrets.",
		examples: []string{
			"pluto device add neptuno siddhant@neptuno",
			"pluto device ls",
			"pluto device rm neptuno",
		},
	},
	{
		name:    "device add",
		usage:   []string{"pluto device add <nickname> <user@host>"},
		details: "Save a nickname for an ssh destination and make it ready to use.\nIf the host refuses your key, it asks for the account password once, installs\nyour public key (generating ~/.ssh/id_ed25519 if you have none), and records\nthe host's pluto path so later commands need no password and no PATH setup.\nA target that still does not answer is a warning, not a failure.",
		examples: []string{
			"pluto device add neptuno siddhant@neptuno",
		},
	},
	{
		name:     "device ls",
		usage:    []string{"pluto device ls"},
		details:  "List saved devices.",
		examples: []string{"pluto device ls"},
	},
	{
		name:     "device rm",
		usage:    []string{"pluto device rm <nickname>"},
		details:  "Remove a saved device.",
		examples: []string{"pluto device rm neptuno"},
	},
}

// findDeviceDoc returns a device command's help entry.
func findDeviceDoc(name string) (deviceDoc, bool) {
	for _, doc := range deviceDocs {
		if doc.name == name {
			return doc, true
		}
	}
	return deviceDoc{}, false
}

// commandHelp renders one device command's details and examples.
func commandHelp(name string) (string, bool) {
	doc, ok := findDeviceDoc(name)
	if !ok {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "usage: %s\n", strings.Join(doc.usage, "\n       "))
	if doc.details != "" {
		fmt.Fprintf(&b, "\n%s\n", doc.details)
	}
	if len(doc.examples) > 0 {
		fmt.Fprint(&b, "\nexamples:\n")
		for _, example := range doc.examples {
			fmt.Fprintf(&b, "  %s\n", example)
		}
	}
	return b.String(), true
}

// clientUsage writes the top-level client help.
func clientUsage(w io.Writer) {
	fmt.Fprint(w, clientUsageText)
}

// unknownSubcommand prints the closest match and a next step for an unknown
// subcommand, then the parent's usage lines.
func unknownSubcommand(stderr io.Writer, parent, sub string, names []string) int {
	fmt.Fprintf(stderr, "pluto: unknown %s subcommand %q\n", parent, sub)
	if near := cliutil.Closest(sub, names); near != "" {
		fmt.Fprintf(stderr, "next: did you mean 'pluto %s %s'?\n", parent, near)
	} else {
		fmt.Fprintf(stderr, "next: run 'pluto help %s'\n", parent)
	}
	if doc, ok := findDeviceDoc(parent); ok {
		fmt.Fprintln(stderr)
		fmt.Fprintf(stderr, "usage: %s\n", strings.Join(doc.usage, "\n       "))
	}
	return 2
}
