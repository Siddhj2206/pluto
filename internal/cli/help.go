package cli

import (
	"fmt"
	"io"
	"strings"
)

// This file is the CLI's help surface (ADR 0009): the grouped top-level
// usage, per-command details with examples, and the closest-match suggestion
// for an unknown command. box and vsock are internal and never listed.

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
var groupOrder = []string{"boxes", "work", "images", "devices", "host", "other"}

var commandDocs = []commandDoc{
	{
		name: "init", group: "host",
		summary:  "prepare a repo for Pluto",
		usage:    []string{"pluto init [--with-hooks | --remove-hooks]"},
		details:  "Create a starter .pluto.toml only when one is absent. Hook installation is opt-in; it notifies the daemon after commits and never creates or starts a box.",
		examples: []string{"pluto init", "pluto init --with-hooks", "pluto init --remove-hooks"},
	},
	{
		name: "setup", group: "host",
		summary:  "configure repository automation",
		usage:    []string{"pluto setup github"},
		details:  "Guide a repository owner through mapping supported GitHub events to declared jobs in .pluto.toml. Event work stays disabled until the exact contract revision is approved for unattended work.",
		examples: []string{"pluto setup github"},
	},
	{
		name: "setup github", group: "host",
		usage:    []string{"pluto setup github"},
		details:  "Map push, pull_request, and issues deliveries to declared jobs. The flow writes [events.*] policy to .pluto.toml; GitHub sends signed deliveries to the host's dedicated webhook intake.",
		examples: []string{"pluto setup github"},
	},
	{
		name: "up", group: "boxes",
		summary: "create or wake the box for a worktree",
		usage:   []string{"pluto up [--worktree PATH] [--async]", "pluto up --repo URL [--async]"},
		details: "Create the box for a worktree or clone a remote repository, then ensure it is running. The worktree is\nthe current directory unless --worktree names one. Idempotent: an existing\nbox is woken instead. Git uses the host's configured credential helper or SSH agent.\nWith --async, durably queue the request and return its ID.",
		examples: []string{
			"pluto up",
			"pluto up --worktree ~/src/app",
			"pluto up --async",
		},
	},
	{
		name: "attach", group: "boxes",
		summary: "open an ssh session in a box (wakes it first)",
		usage:   []string{"pluto attach [box-id|worktree] [--session NAME] [-- command...]"},
		details: "Open an interactive ssh session in a box, waking it first. The box is\nthe current worktree's unless a target is given. With '--session NAME', enter\nthe declared [sessions.NAME]; closing the client detaches without ending it.\nWith '-- command', run that command in the box instead of a shell.",
		examples: []string{
			"pluto attach",
			"pluto attach mybox",
			"pluto attach mybox --session agent",
			"pluto attach mybox -- uname -a",
		},
	},
	{
		name: "connect", group: "boxes",
		summary:  "wake a box and prepare access to a declared service",
		usage:    []string{"pluto connect [box-id|worktree] SERVICE [--access-mode ssh-tunnel] [--provider ssh] [--local-port PORT] [--json]"},
		details:  "Ensure the target box is running, wait for the declared service to become active, and return a local endpoint plus instructions for an SSH tunnel. The service keeps ownership of its protocol, authentication, and pairing. Use --json for a structured result.",
		examples: []string{"pluto connect opencode", "pluto connect mybox opencode --json", "pluto connect mybox web --local-port 18080"},
	},
	{
		name: "pause", group: "boxes",
		summary: "stop a box cleanly; its disk stays on the host",
		usage:   []string{"pluto pause <box-id|worktree>"},
		details: "Stop a box cleanly; its disk stays on the host and 'pluto up' wakes it\nagain. Work running in the box stops.",
		examples: []string{
			"pluto pause mybox",
			"pluto pause ~/src/app",
		},
	},
	{
		name: "destroy", group: "boxes",
		summary: "remove a box and its disk",
		usage:   []string{"pluto destroy <box-id|worktree> [--yes]"},
		details: "Remove a box: its record, its disk, and its job history. Asks for\nconfirmation on a terminal; --yes skips the prompt. Never automatic.",
		examples: []string{
			"pluto destroy mybox --yes",
		},
	},
	{
		name: "status", group: "boxes",
		summary: "show one box (by id or worktree)",
		usage:   []string{"pluto status <box-id|worktree>"},
		details: "Show one box: state, image, phases, services, contract staleness, the\nlatest job, and auto-pause. A box id prefix works too.",
		examples: []string{
			"pluto status mybox",
			"pluto status ~/src/app",
		},
	},
	{
		name: "ls", group: "boxes",
		summary: "list boxes",
		usage:   []string{"pluto ls"},
		details: "List boxes with their project/branch, state, and provision status.",
		examples: []string{
			"pluto ls",
		},
	},
	{
		name: "run", group: "work",
		summary: "run a declared job, or a one-off command, in a box",
		usage: []string{
			"pluto run [--async] [box-id|worktree] [job]",
			"pluto run [--async] [box-id|worktree] -- <command> [args...]",
		},
		details: "Run a declared job from the worktree's .pluto.toml, or a one-off command.\nWith --async, durably queue the request and return its ID. With no arguments,\nlist the current worktree's declared jobs. A lone argument is a job name; a\ndeclared job named ls, show, or logs keeps that meaning, and the run manager\nselects otherwise ('pluto run ls --json', or show/logs with a run id).",
		examples: []string{
			"pluto run",
			"pluto run test",
			"pluto run mybox test",
			"pluto run -- pnpm test",
			"pluto run --async test",
		},
	},
	{
		name: "task", group: "work",
		summary:  "start and inspect durable tasks",
		usage:    []string{"pluto task create --job NAME --prompt TEXT [--isolate] [box-id|worktree] [--json]", "pluto task ls [--json]", "pluto task show <task-id> [--json]", "pluto task follow <task-id> --prompt TEXT [--job NAME] [--json]", "pluto task retry <task-id> [run-id] [--json]", "pluto task logs <task-id> [run-id] [--lines N] [--follow]", "pluto task changes <task-id> [--json]"},
		details:  "A task is durable requested work; each prompt is one run. Creation and follow-up are queued by the host daemon. A target defaults to the current worktree. Task IDs accept unambiguous prefixes. Use --isolate to give the task its own worktree and box.",
		examples: []string{"pluto task create --job agent --prompt 'fix the failing test'", "pluto task ls --json", "pluto task show 12ab34cd", "pluto task follow 12ab34cd --prompt 'add a regression test'", "pluto task retry 12ab34cd", "pluto task changes 12ab34cd"},
	},
	{
		name: "job", group: "work", summary: "discover declared jobs",
		usage: []string{"pluto job ls"}, details: "List declared jobs from the current worktree's .pluto.toml. Existing 'pluto run' behavior is unchanged.", examples: []string{"pluto job ls"},
	},
	{name: "task create", group: "work", usage: []string{"pluto task create --job NAME --prompt TEXT [--isolate] [box-id|worktree] [--json]"}, details: "Queue the first run for a task. The current worktree is the default; Pluto uses its box or registers one for the worktree.", examples: []string{"pluto task create --job agent --prompt 'fix the failing test'"}},
	{name: "task ls", group: "work", usage: []string{"pluto task ls [--json]"}, details: "List durable tasks and their current state. JSON uses schema_version 1 and a tasks array.", examples: []string{"pluto task ls", "pluto task ls --json"}},
	{name: "task show", group: "work", usage: []string{"pluto task show <task-id> [--json]"}, details: "Show task context and run history. A task ID prefix must resolve to one task.", examples: []string{"pluto task show 12ab34cd"}},
	{name: "task follow", group: "work", usage: []string{"pluto task follow <task-id> --prompt TEXT [--job NAME] [--json]"}, details: "Queue a serialized follow-up run. The latest run's job is used unless --job is given.", examples: []string{"pluto task follow 12ab34cd --prompt 'add a regression test'"}},
	{name: "task retry", group: "work", usage: []string{"pluto task retry <task-id> [run-id] [--json]"}, details: "Queue a new run under the existing task with a prior run's job and prompt, to recover failed or blocked work. Defaults to the latest run.", examples: []string{"pluto task retry 12ab34cd", "pluto task retry 12ab34cd 9f8e7d6c"}},
	{name: "task logs", group: "work", usage: []string{"pluto task logs <task-id> [run-id] [--lines N] [--follow] [--json]"}, details: "Read a task run's recorded job output. Defaults to the latest run. With --follow, print the output and keep polling until the run reaches a terminal state.", examples: []string{"pluto task logs 12ab34cd", "pluto task logs 12ab34cd --follow"}},
	{name: "task changes", group: "work", usage: []string{"pluto task changes <task-id> [--json]"}, details: "Show the task's box and its current working-tree changes. An isolated task box's changes are the task's; a shared branch box's changes are current box state and are not attributed to the task. JSON uses schema_version 1 and a changes object.", examples: []string{"pluto task changes 12ab34cd", "pluto task changes 12ab34cd --json"}},
	{name: "run ls", group: "work", usage: []string{"pluto run ls [--json]"}, details: "List runs across durable tasks, newest first. JSON uses schema_version 1 and a runs array.", examples: []string{"pluto run ls --json"}},
	{name: "run show", group: "work", usage: []string{"pluto run show <run-id> [--json]"}, details: "Show one durable run. Run IDs accept unambiguous prefixes.", examples: []string{"pluto run show 12ab34cd"}},
	{name: "run logs", group: "work", usage: []string{"pluto run logs <run-id> [--lines N] [--follow] [--json]"}, details: "Read a durable run's recorded job output. With --follow, print the output and keep polling until the run reaches a terminal state.", examples: []string{"pluto run logs 12ab34cd", "pluto run logs 12ab34cd --follow"}},
	{name: "job ls", group: "work", usage: []string{"pluto job ls"}, details: "List declared jobs in the current worktree. This does not contact the daemon.", examples: []string{"pluto job ls"}},
	{
		name: "queue", group: "work",
		summary:  "inspect queued and completed work",
		usage:    []string{"pluto queue"},
		details:  "Show queue ID, source, repo/ref, job, priority, age, state, resulting box/job,\nand any rejection or failure reason.",
		examples: []string{"pluto queue"},
	},
	{
		name: "jobs", group: "work",
		summary: "list a box's recent jobs",
		usage:   []string{"pluto jobs <box-id|worktree>"},
		details: "List the box's recent runs, newest first: id, state, exit code, duration,\nstart time, and command. A box keeps its last 20 runs.",
		examples: []string{
			"pluto jobs mybox",
			"pluto jobs ~/src/app",
		},
	},
	{
		name: "logs", group: "work",
		summary: "show a box's provision, wake, service, or job logs",
		usage:   []string{"pluto logs <box-id|worktree> [--phase provision|wake] [--service NAME] [--job ID|last] [--lines N]"},
		details: "Show a box's provision or wake log, a service's journal, or a recorded\njob's output. With no selector, the phase logs and the latest job are shown;\n'--job last', an id, or an id prefix picks any retained job.",
		examples: []string{
			"pluto logs mybox",
			"pluto logs mybox --phase provision",
			"pluto logs mybox --service web",
			"pluto logs mybox --job last",
		},
	},
	{
		name: "image", group: "images",
		summary: "import or list base images",
		usage: []string{
			"pluto image import <artifact-dir>",
			"pluto image ls",
		},
		details: "Import a built image artifact (the output of the image builder,\n`go run ./cmd/pluto-image-builder`) into the daemon's image store, or list\nimported images.",
		examples: []string{
			"pluto image import images/out",
			"pluto image ls",
		},
	},
	{
		name: "image import", group: "images",
		usage:   []string{"pluto image import <artifact-dir>"},
		details: "Verify an artifact directory's manifest and hashes and install it into\nthe daemon's image store. The content-derived version is printed.",
		examples: []string{
			"pluto image import images/out",
		},
	},
	{
		name: "image ls", group: "images",
		usage:   []string{"pluto image ls"},
		details: "List imported images with their version, build time, and artifact hashes.",
		examples: []string{
			"pluto image ls",
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
		details: "Manage the client-side registry of saved ssh destinations used by the\nglobal --device flag. It lives at ~/.config/pluto/devices.toml and holds no\nsecrets.",
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
		usage:   []string{"pluto daemon [--max-running-boxes N] [--queue-capacity N] [--queue-aging DURATION]"},
		details: "Run the host daemon in the foreground: it owns this machine's boxes,\njobs, schedules, and durable work queue and listens on the unix socket. Queue\nsettings control running-box capacity, actionable queue size, and aging interval.\n'pluto install' runs it as a systemd user service instead.",
		examples: []string{
			"pluto daemon",
			"pluto install",
		},
	},
	{
		name: "install", group: "host",
		summary: "install the daemon as a systemd user service with linger",
		usage:   []string{"pluto install"},
		details: "Write a systemd user unit for this pluto binary, enable it now, and\nenable linger so the daemon starts at login. Needs a systemd user session.",
		examples: []string{
			"pluto install",
			"systemctl --user status pluto",
		},
	},
	{
		name: "uninstall", group: "host",
		summary: "remove the systemd user service",
		usage:   []string{"pluto uninstall"},
		details: "Disable the pluto user unit and remove it. Boxes and their disks stay.",
		examples: []string{
			"pluto uninstall",
		},
	},
	{
		name: "provider", group: "host",
		summary:  "manage optional host access providers",
		usage:    []string{"pluto provider list", "pluto provider status [ID]", "pluto provider install ID [--approve]", "pluto provider enable ID [--approve]", "pluto provider disable ID", "pluto provider remove ID", "pluto provider route add BOX SERVICE [--provider ID] --approve --confirm-auth", "pluto provider route remove ROUTE_ID [--provider ID]"},
		details:  "Providers run under the host owner's account. Review each provider's capabilities and external dependencies before approving install or enable. Tailscale requires its host client to be installed with the operating system's package manager and signed in by the owner. OpenTunnel requires Bun, a Cloudflare account, ZeroSSL credentials, and currently an AWS relay; create its tunnel identity with `opentunnel create` before enabling it. Provider remove signs Tailscale out or removes the OpenTunnel tunnel identity. Public routes select one declared service and require separate approval plus confirmation that the service keeps its own authentication enabled. Provider access never publishes Pluto's task/control API.",
		examples: []string{"pluto provider list", "pluto provider install tailscale --approve", "pluto provider enable tailscale --approve", "pluto provider install opentunnel --approve", "opentunnel create", "pluto provider enable opentunnel --approve", "pluto provider route add BOX web --approve --confirm-auth", "pluto provider route remove ROUTE_ID", "pluto provider status opentunnel"},
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
