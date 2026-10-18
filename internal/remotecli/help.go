package remotecli

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// clientUsageText is the Windows client's top-level help. The client has no
// daemon, so it lists the device registry commands and points every host
// command at --device.
const clientUsageText = `pluto client: drive a Linux Pluto host over SSH

usage: pluto [--device NAME|user@host] <command> [args]

This client has no daemon. Save a Linux host, then run host commands with
--device:

  pluto device add <nickname> <user@host>
  pluto --device <nickname> up --repo <url>
  pluto --device <nickname> ls
  pluto --device <nickname> status <box>
  pluto --device <nickname> logs <box>
  pluto --device <nickname> run <box> [job | -- command...]
  pluto --device <nickname> task ls
  pluto --device <nickname> attach <box>
  pluto --device <nickname> pause <box>
  pluto --device <nickname> destroy <box> --yes

The host needs pluto on the PATH that ssh sees: check with
'ssh user@host pluto version'. The first connection to a host accepts its
key; a later key change is rejected.

Saved hosts: pluto device ls. Config: %AppData%\pluto\devices.toml.

flags:
  --device NAME|user@host   saved host nickname or raw ssh target
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
		details: "Save a nickname for an ssh destination, then probe it with 'ssh <target>\npluto version'. A target that does not answer is a warning, not a failure.",
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

// wantsHelp reports whether args ask for a command's help: -h or --help
// before any '--', after which the words belong to the work being run.
func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// maybeHelp prints a device command's help when args ask for it and reports
// whether it handled the invocation.
func maybeHelp(args []string, name string, stdout io.Writer) bool {
	if !wantsHelp(args) {
		return false
	}
	text, ok := commandHelp(name)
	if !ok {
		return false
	}
	fmt.Fprint(stdout, text)
	return true
}

// maybeHelpAtStart prints name's help only when the help flag is the first
// argument, so `device add -h` flows to the subcommand.
func maybeHelpAtStart(args []string, name string, stdout io.Writer) bool {
	if len(args) == 0 {
		return false
	}
	return maybeHelp(args[:1], name, stdout)
}

// parseCommand parses a command's flags in the CLI's voice: a parse error is
// a usage error, printed as 'pluto: <what>' followed by the command's usage
// lines, and returns 2. Success returns 0.
func parseCommand(fs *flag.FlagSet, args []string, stderr io.Writer, usageLines ...string) int {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		for _, line := range usageLines {
			fmt.Fprintln(stderr, line)
		}
		return 2
	}
	return 0
}

// unknownSubcommand prints the closest match and a next step for an unknown
// subcommand, then the parent's usage lines.
func unknownSubcommand(stderr io.Writer, parent, sub string, names []string) int {
	fmt.Fprintf(stderr, "pluto: unknown %s subcommand %q\n", parent, sub)
	if near := closest(sub, names); near != "" {
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

// closest returns the name nearest to target, or "" when nothing is within
// two edits.
func closest(target string, names []string) string {
	best, bestDist := "", 3
	for _, name := range names {
		if name == target {
			continue
		}
		if d := editDistance(target, name); d < bestDist {
			best, bestDist = name, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
