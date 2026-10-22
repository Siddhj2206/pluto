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

  pluto device add <nickname> [destination]
  pluto device doctor <nickname>
  pluto --device <nickname> up --repo <url>
  pluto --device <nickname> ls
  pluto --device <nickname> status <box>
  pluto --device <nickname> logs <box>
  pluto --device <nickname> run <box> [job | -- command...]
  pluto --device <nickname> task ls
  pluto --device <nickname> attach <box>
  pluto --device <nickname> pause <box>
  pluto --device <nickname> destroy <box> --yes

device add is the whole setup: it saves an OpenSSH alias or raw target, asks
for the account password once when the host does not accept your key yet,
installs the key with ssh-copy-id, resolves the route with ssh -G, and checks
the host binary, version, daemon, and image. A saved-but-unreachable device is
unverified or offline, never connected. The first connection accepts the host
key; a later key change is rejected. device doctor explains each hop in order
and never changes the host.

Saved devices: pluto device ls. Config: %AppData%\pluto\devices.toml.
Cache: %LocalAppData%\pluto\devices (no secrets).

flags:
  --device NAME|user@host   saved device nickname or raw ssh target
  --socket PATH             forwarded to the host CLI
  --state-dir PATH          forwarded to the host CLI
  --version                 print the version
  help [command]            show this help
`

// deviceUsageLines is the `pluto device` usage, split across its subcommands.
var deviceUsageLines = []string{
	"pluto device add <nickname> [destination] [options]",
	"pluto device doctor [nickname] [--json] [--print-ssh] [--offline]",
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
		usage:   []string{"pluto device add <nickname> [destination] [--port N] [--identity FILE] [--proxy-jump JUMP] [--trust strict|accept-new] [--dry-run] [--no-probe] [--json]"},
		details: "Save a nickname for an ssh destination and verify the whole path.\nThe profile points at OpenSSH: ~/.ssh/config stays the source of truth and\nssh -G resolves the route for display. If the host refuses your key, it asks\nfor the account password once and runs ssh-copy-id; Pluto never stores a\npassword. It checks the host key, remote shell, pluto binary, version and\nprotocol, daemon, state, and image, then prints a connection card. A saved\nhost that does not answer is unverified or offline, never connected.",
		examples: []string{
			"pluto device add neptuno",
			"pluto device add neptuno siddhant@neptuno",
			"pluto device add neptuno siddhant@home.example.net --port 2222",
			"pluto device add neptuno --identity ~/.ssh/id_ed25519 --trust accept-new",
		},
	},
	{
		name:    "device doctor",
		usage:   []string{"pluto device doctor [nickname] [--json] [--print-ssh] [--offline]"},
		details: "Check the full connection path in order: local ssh, config resolution,\nDNS/TCP, proxy, host key, auth, remote shell, binary discovery, version and\nprotocol, daemon, state, and image. Each hop is ok, warn, failed, or skipped\nwith a stable code and exact next steps. It never installs keys, writes ssh\nconfig, or changes host keys. --offline reads the cache and skips network\nhops; --print-ssh shows the exact argv with paths redacted.",
		examples: []string{
			"pluto device doctor neptuno",
			"pluto device doctor neptuno --json",
			"pluto device doctor neptuno --print-ssh",
		},
	},
	{
		name:     "device ls",
		usage:    []string{"pluto device ls"},
		details:  "List saved devices. Shows the resolved route, connected/unverified/offline status, version, and last success.",
		examples: []string{"pluto device ls"},
	},
	{
		name:     "device rm",
		usage:    []string{"pluto device rm <nickname>"},
		details:  "Remove a saved device. Deletes its disposable cache too.",
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
