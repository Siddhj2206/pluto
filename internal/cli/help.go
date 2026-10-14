package cli

import (
	"fmt"
	"io"
	"strings"
)

// This file is the CLI's help surface (ADR 0009): the grouped top-level
// usage, per-command details with examples, and the closest-match suggestion
// for an unknown command.

// globalFlagsHelp documents the flags every command accepts.
const globalFlagsHelp = `global flags:
  --socket PATH        daemon unix socket (default: $PLUTO_SOCKET or
                       $XDG_RUNTIME_DIR/pluto/pluto.sock)
  --state-dir PATH     state directory (daemon only; default:
                       $PLUTO_STATE_DIR or ~/.local/state/pluto)
  --device NAME|HOST   run the command on a saved device nickname or a
                       user@host ssh target (saved devices: 'pluto device ls')

run 'pluto help <command>' for one command's usage and examples.
`

// commandDoc is one visible command's help entry.
type commandDoc struct {
	name     string
	group    string
	summary  string
	usage    []string
	details  string
	examples []string
}

// groupOrder fixes the top-level listing order.
var groupOrder = []string{"boxes", "work", "devices", "host", "other"}

var commandDocs = []commandDoc{
	{
		name: "box", group: "boxes",
		summary: "manage a workspace's box",
		usage: []string{
			"pluto box ls",
			"pluto box status <box-id|workspace>",
			"pluto box create [--workspace PATH] [--image REF]",
			"pluto box rm <box-id|workspace> [--yes]",
			"pluto box pin <box-id|workspace>",
			"pluto box unpin <box-id|workspace>",
		},
		details: "A workspace is a repo checkout on a host; it resolves to exactly one box through the vcs adapter. A box is created and started from a base image, keeps its durable volumes, and is listed, inspected, and removed here.",
		examples: []string{
			"pluto box create --workspace ~/src/app",
			"pluto box ls",
			"pluto box status ~/src/app",
			"pluto box rm ~/src/app --yes",
		},
	},
	{
		name: "box ls", group: "boxes",
		usage:    []string{"pluto box ls"},
		details:  "List boxes with their workspace and state.",
		examples: []string{"pluto box ls"},
	},
	{
		name: "box status", group: "boxes",
		usage:    []string{"pluto box status <box-id|workspace>"},
		details:  "Show one box: its workspace, repo and revision, state, layers, volumes, and container handle. A box id prefix works too.",
		examples: []string{"pluto box status ~/src/app", "pluto box status 12ab34cd"},
	},
	{
		name: "box create", group: "boxes",
		usage:    []string{"pluto box create [--workspace PATH] [--image REF]"},
		details:  "Resolve a workspace to its one box, creating and starting it on first use. The workspace is the current directory unless --workspace names one.",
		examples: []string{"pluto box create", "pluto box create --workspace ~/src/app"},
	},
	{
		name: "box rm", group: "boxes",
		usage:    []string{"pluto box rm <box-id|workspace> [--yes]"},
		details:  "Destroy a box: its container, its durable volumes, and its record. Asks for confirmation on a terminal; --yes skips the prompt.",
		examples: []string{"pluto box rm ~/src/app --yes"},
	},
	{
		name: "box pin", group: "boxes",
		usage:    []string{"pluto box pin <box-id|workspace>"},
		details:  "Exempt a box from retention eviction. A retained failed CI box is otherwise evicted at the earlier of 48 hours or 5 boxes; a pinned box is never evicted.",
		examples: []string{"pluto box pin 12ab34cd"},
	},
	{
		name: "box unpin", group: "boxes",
		usage:    []string{"pluto box unpin <box-id|workspace>"},
		details:  "Lift a box's retention exemption, so it is evictable again.",
		examples: []string{"pluto box unpin 12ab34cd"},
	},
	{
		name: "attach", group: "boxes",
		summary: "open a shared terminal in a box (wakes it first)",
		usage:   []string{"pluto attach [box-id|workspace] [--watch]"},
		details: "Open the box's terminal, waking it first. The box is the current\nworkspace's unless a target is given. The terminal is shared: several clients\nsee the same output and any may type, and recent scrollback is replayed on\njoin. With --watch the connection is read-only.",
		examples: []string{
			"pluto attach",
			"pluto attach mybox",
			"pluto attach mybox --watch",
		},
	},
	{
		name: "run", group: "work",
		summary: "run a declared task in a box",
		usage:   []string{"pluto run <task>"},
		details: "Run a task declared in the workspace's pluto.yml in its durable box,\nrecording each invocation as a run. A single-command task runs as one implicit\nstep. The workspace is the current directory's repo checkout.",
		examples: []string{
			"pluto run dev",
			"pluto run test",
		},
	},
	{
		name: "ci", group: "work",
		summary: "run one CI execution in a fresh ephemeral box",
		usage:   []string{"pluto ci <task> [--workspace PATH]"},
		details: "Run a task declared in pluto.yml once, in a fresh ephemeral box seeded from the workspace's provisioned package layer. The box mounts no durable volumes, so CI never reads or mutates the workspace's durable box. A successful run discards its box; a failed run retains it for inspection and names the attach path. The manual trigger this stint ships; the GitHub runner worker is a later area.",
		examples: []string{
			"pluto ci test",
			"pluto ci build --workspace ~/src/app",
		},
	},
	{
		name: "ls", group: "work",
		summary: "list recorded runs",
		usage:   []string{"pluto ls runs [--json]"},
		details: "List the host's recorded runs, newest first.",
		examples: []string{
			"pluto ls runs",
		},
	},
	{
		name: "ls runs", group: "work",
		usage:    []string{"pluto ls runs [--json]"},
		details:  "List recorded runs newest first: id, task, state, exit code, duration, and start time. With --json, emit the versioned run schema (schema_version 1) as a runs array.",
		examples: []string{"pluto ls runs", "pluto ls runs --json"},
	},
	{
		name: "show", group: "work",
		summary: "show one run",
		usage:   []string{"pluto show <run-id> [--json]"},
		details: "Show one recorded run: its task, box, workspace, trigger, state, timing, exit code, and steps. A run id prefix works too. With --json, emit the versioned run schema (schema_version 1) as a run object.",
		examples: []string{
			"pluto show 12ab34cd",
			"pluto show 12ab34cd --json",
		},
	},
	{
		name: "logs", group: "work",
		summary: "show a run's retained output",
		usage:   []string{"pluto logs <run-id> [--follow] [--lines N]"},
		details: "Show a run's retained per-step output, following a live run with\n--follow. A run id (or an unambiguous prefix) selects a recorded run.",
		examples: []string{
			"pluto logs 12ab34cd",
			"pluto logs 12ab34cd --follow",
		},
	},
	{
		name: "device", group: "devices",
		summary: "manage saved ssh devices",
		usage: []string{
			"pluto device add <nickname> <user@host>",
			"pluto device ls",
			"pluto device rm <nickname>",
		},
		details: "Manage the client-side registry of saved ssh destinations used by the\nglobal --device flag. It lives at ~/.config/pluto/devices.yml and holds no\nsecrets.",
		examples: []string{
			"pluto device add neptuno siddhant@neptuno",
			"pluto device ls",
			"pluto device rm neptuno",
		},
	},
	{
		name: "device add", group: "devices",
		usage:   []string{"pluto device add <nickname> <user@host>"},
		details: "Save a nickname for an ssh destination, then probe it with 'ssh <target>\npluto version'. A target that does not answer is a warning, not a failure.",
		examples: []string{
			"pluto device add neptuno siddhant@neptuno",
		},
	},
	{
		name: "device ls", group: "devices",
		usage:   []string{"pluto device ls"},
		details: "List saved devices.",
		examples: []string{
			"pluto device ls",
		},
	},
	{
		name: "device rm", group: "devices",
		usage:   []string{"pluto device rm <nickname>"},
		details: "Remove a saved device.",
		examples: []string{
			"pluto device rm neptuno",
		},
	},
	{
		name: "daemon", group: "host",
		summary: "run the host daemon in the foreground",
		usage:   []string{"pluto daemon [--run-retention DURATION] [--freeze-idle DURATION] [--stop-idle DURATION]"},
		details: "Run the host daemon in the foreground: it owns this machine's boxes,\nruns, and schedules and listens on the unix socket. --run-retention bounds how\nlong run records and their step logs are kept (default 30 days; 0 keeps\neverything; PLUTO_RUN_RETENTION sets the default). --freeze-idle and --stop-idle\ncontrol how long an idle box is kept running before it freezes and stops.",
		examples: []string{
			"pluto daemon",
			"pluto daemon --run-retention 720h",
		},
	},
	{
		name: "init", group: "host",
		summary:  "prepare a repo for Pluto",
		usage:    []string{"pluto init"},
		details:  "Create a starter pluto.yml only when one is absent; an existing contract is never overwritten.",
		examples: []string{"pluto init"},
	},
	{
		name: "provider", group: "host",
		summary:  "manage optional host access providers",
		usage:    []string{"pluto provider list", "pluto provider status [ID]", "pluto provider install ID [--approve]", "pluto provider enable ID [--approve]", "pluto provider disable ID", "pluto provider remove ID"},
		details:  "Providers run under the host owner's account. Review each provider's capabilities and external dependencies before approving install or enable. Tailscale requires its host client to be installed with the operating system's package manager and signed in by the owner. OpenTunnel requires Bun, a Cloudflare account, ZeroSSL credentials, and currently an AWS relay; create its tunnel identity with `opentunnel create` before enabling it. Provider remove signs Tailscale out or removes the OpenTunnel tunnel identity. Provider access never publishes Pluto's task/control API.",
		examples: []string{"pluto provider list", "pluto provider install tailscale --approve", "pluto provider enable tailscale --approve", "pluto provider install opentunnel --approve", "opentunnel create", "pluto provider enable opentunnel --approve", "pluto provider status opentunnel"},
	},
	{
		name: "help", group: "other",
		summary: "show help for a command or the top level",
		usage:   []string{"pluto help [command]"},
		details: "Print the grouped command list, or one command's usage and examples.",
		examples: []string{
			"pluto help",
			"pluto help run",
		},
	},
	{
		name: "version", group: "other",
		summary: "print the version",
		usage:   []string{"pluto version"},
		details: "Print the pluto version.",
		examples: []string{
			"pluto version",
		},
	},
}

// findCommand returns a command's help entry; name may be a subcommand pair
// such as "device add".
func findCommand(name string) (commandDoc, bool) {
	for _, doc := range commandDocs {
		if doc.name == name {
			return doc, true
		}
	}
	return commandDoc{}, false
}

// usage prints the grouped top-level listing.
func usage(w io.Writer) {
	fmt.Fprint(w, "pluto - durable work machines\n\nusage: pluto [--socket PATH] [--state-dir PATH] [--device NAME|user@host] <command> [args]\n\n")
	for _, group := range groupOrder {
		fmt.Fprintf(w, "%s:\n", group)
		for _, doc := range commandDocs {
			if doc.group != group || strings.Contains(doc.name, " ") {
				continue
			}
			fmt.Fprintf(w, "  %-9s %s\n", doc.name, doc.summary)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprint(w, globalFlagsHelp)
}

// commandHelp renders one command's details and examples.
func commandHelp(name string) (string, bool) {
	doc, ok := findCommand(name)
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

// maybeHelp prints a command's help when args ask for it and reports whether
// it handled the invocation. A name with no doc is left to the command.
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
// argument. Parent commands with subcommands use it so `device add -h` flows
// to the subcommand and prints its own details; only `device -h` is the
// parent's to answer.
func maybeHelpAtStart(args []string, name string, stdout io.Writer) bool {
	if len(args) == 0 {
		return false
	}
	return maybeHelp(args[:1], name, stdout)
}

// unknownCommand prints the closest match and a next step, then usage.
func unknownCommand(cmd string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "pluto: unknown command %q\n", cmd)
	if near := closest(cmd, visibleCommandNames()); near != "" {
		fmt.Fprintf(stderr, "next: did you mean 'pluto %s'?\n", near)
	} else {
		fmt.Fprintln(stderr, "next: run 'pluto help' to see the available commands")
	}
	fmt.Fprintln(stderr)
	usage(stderr)
	return 2
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
	if doc, ok := findCommand(parent); ok {
		fmt.Fprintln(stderr)
		fmt.Fprintf(stderr, "usage: %s\n", strings.Join(doc.usage, "\n       "))
	}
	return 2
}

// usageError prints a usage error in the CLI's voice and returns the usage
// exit code.
func usageError(stderr io.Writer, msg string, usageLine string) int {
	fmt.Fprintf(stderr, "pluto: %s\n", msg)
	fmt.Fprintln(stderr, usageLine)
	return 2
}

// visibleCommandNames returns the top-level command names the help surface
// lists.
func visibleCommandNames() []string {
	names := make([]string, 0, len(commandDocs))
	for _, doc := range commandDocs {
		if !strings.Contains(doc.name, " ") {
			names = append(names, doc.name)
		}
	}
	return names
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
