package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/state"
)

const boxUsage = "usage: pluto box <ls|status|create|rm|pin|unpin> [args]"

// runBoxCommand is the user-facing box noun. It drives the redrawn container
// lifecycle: a workspace resolves to one box, and the box can be created,
// inspected, and removed.
func runBoxCommand(args []string, socket string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, boxUsage)
		return 2
	}
	switch args[0] {
	case "ls":
		return runBoxLs(args[1:], socket, stdout, stderr)
	case "status":
		return runBoxStatus(args[1:], socket, stdout, stderr)
	case "create":
		return runBoxCreate(args[1:], socket, stdout, stderr)
	case "rm", "remove":
		return runBoxRm(args[1:], socket, stdout, stderr)
	case "pin":
		return runBoxPin(args[1:], socket, stdout, stderr)
	case "unpin":
		return runBoxUnpin(args[1:], socket, stdout, stderr)
	case "-h", "--help", "help":
		if text, ok := commandHelp("box"); ok {
			fmt.Fprint(stdout, text)
			return 0
		}
		fmt.Fprintln(stdout, boxUsage)
		return 0
	default:
		return unknownSubcommand(stderr, "box", args[0], []string{"ls", "status", "create", "rm", "pin", "unpin"})
	}
}

func runBoxLs(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "box ls", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("box ls", flag.ContinueOnError)
	if code := parseCommand(fs, args, stderr, "usage: pluto box ls"); code != 0 {
		return code
	}
	list, err := client.New(socket).ListContainerBoxes()
	if err != nil {
		return fail(stderr, err)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWORKSPACE\tSTATE\tCREATED")
	for _, b := range list.Boxes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", short(b.ID), boxWorkspace(b), b.State, b.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	w.Flush()
	for _, e := range list.Errors {
		fmt.Fprintf(stderr, "warning: unreadable box record %s: %s\n", e.Path, e.Err)
	}
	return 0
}

func runBoxStatus(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "box status", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("box status", flag.ContinueOnError)
	if code := parseCommand(fs, args, stderr, "usage: pluto box status <box-id|workspace>"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto box status <box-id|workspace>")
		return 2
	}
	box, err := resolveContainerBox(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "id:        %s\n", box.ID)
	fmt.Fprintf(stdout, "workspace: %s\n", boxWorkspace(box))
	if box.Repo != "" {
		fmt.Fprintf(stdout, "repo:      %s\n", box.Repo)
	}
	if box.Revision != "" {
		fmt.Fprintf(stdout, "revision:  %s\n", box.Revision)
	}
	fmt.Fprintf(stdout, "state:     %s\n", box.State)
	if box.Phases != nil && box.Phases.Wake.State != "" {
		fmt.Fprintf(stdout, "wake:      %s\n", boxWakeLine(box.Phases.Wake))
	}
	if box.EngineID != "" {
		fmt.Fprintf(stdout, "engine:    %s\n", box.EngineID)
	}
	for _, l := range box.Layers {
		fmt.Fprintf(stdout, "layer:     %s %s\n", l.Kind, l.Key)
	}
	for _, v := range box.Volumes {
		fmt.Fprintf(stdout, "volume:    %s %s\n", v.Kind, v.Name)
	}
	fmt.Fprintf(stdout, "created:   %s\n", box.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(stdout, "updated:   %s\n", box.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	return 0
}

func runBoxCreate(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "box create", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("box create", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "workspace path (default: current directory)")
	image := fs.String("image", "", "base image to seed the box from")
	if code := parseCommand(fs, args, stderr, "usage: pluto box create [--workspace PATH] [--image REF]"); code != 0 {
		return code
	}
	dir := *workspace
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
	}
	box, created, err := client.New(socket).CreateContainerBox(api.CreateContainerBoxRequest{Workspace: dir, Image: *image})
	if err != nil {
		return fail(stderr, err, "run 'pluto box create --workspace <path>' with the path to a git workspace")
	}
	if created {
		fmt.Fprintf(stdout, "created box %s for %s\n", short(box.ID), boxWorkspace(box))
	} else {
		fmt.Fprintf(stdout, "box %s already exists for %s\n", short(box.ID), boxWorkspace(box))
	}
	fmt.Fprintf(stdout, "box %s %s\n", short(box.ID), box.State)
	return 0
}

func runBoxRm(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "box rm", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("box rm", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if code := parseCommand(fs, splitFlags(args), stderr, "usage: pluto box rm <box-id|workspace> [--yes]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto box rm <box-id|workspace> [--yes]")
		return 2
	}
	box, err := resolveContainerBox(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	if !*yes {
		if !isTerminal(os.Stdin) {
			return fail(stderr, errors.New("refusing to remove without confirmation"),
				fmt.Sprintf("confirm with 'pluto box rm %s --yes'", short(box.ID)))
		}
		fmt.Fprintf(stdout, "remove box %s (%s)? [y/N] ", short(box.ID), boxWorkspace(box))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stderr, "aborted")
			return 1
		}
	}
	if err := client.New(socket).RemoveContainerBox(box.ID); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "destroyed box %s (%s)\n", short(box.ID), boxWorkspace(box))
	return 0
}

// runBoxPin exempts a box from retention eviction, so a failed CI box under
// investigation stays put (#148).
func runBoxPin(args []string, socket string, stdout, stderr io.Writer) int {
	return runBoxPinOp(args, socket, stdout, stderr, true)
}

// runBoxUnpin lifts a box's retention exemption.
func runBoxUnpin(args []string, socket string, stdout, stderr io.Writer) int {
	return runBoxPinOp(args, socket, stdout, stderr, false)
}

func runBoxPinOp(args []string, socket string, stdout, stderr io.Writer, pin bool) int {
	verb, helpName := "pin", "box pin"
	if !pin {
		verb, helpName = "unpin", "box unpin"
	}
	if maybeHelp(args, helpName, stdout) {
		return 0
	}
	fs := flag.NewFlagSet(helpName, flag.ContinueOnError)
	if code := parseCommand(fs, args, stderr, "usage: pluto box "+verb+" <box-id|workspace>"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto box "+verb+" <box-id|workspace>")
		return 2
	}
	c := client.New(socket)
	box, err := resolveContainerBox(c, fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	var updated *state.Box
	if pin {
		updated, err = c.PinContainerBox(box.ID)
	} else {
		updated, err = c.UnpinContainerBox(box.ID)
	}
	if err != nil {
		return fail(stderr, err)
	}
	if updated.Pinned {
		fmt.Fprintf(stdout, "pinned box %s (%s); it will not be evicted\n", short(updated.ID), boxWorkspace(updated))
	} else {
		fmt.Fprintf(stdout, "unpinned box %s (%s); it is evictable again\n", short(updated.ID), boxWorkspace(updated))
	}
	return 0
}

// resolveContainerBox resolves a box target: an id, an id prefix, or a
// workspace path.
func resolveContainerBox(c *client.Client, target string) (*state.Box, error) {
	if state.ValidID(target) {
		box, err := c.ContainerBox(target)
		if errors.Is(err, client.ErrNotFound) {
			return nil, &hintError{err, []string{"list boxes with 'pluto box ls'"}}
		}
		return box, err
	}
	list, err := c.ListContainerBoxes()
	if err != nil {
		return nil, err
	}
	if abs, err := filepath.Abs(target); err == nil {
		for _, b := range list.Boxes {
			if boxWorkspace(b) == abs {
				return b, nil
			}
		}
	}
	if idPrefix(target) {
		var match *state.Box
		for _, b := range list.Boxes {
			if strings.HasPrefix(b.ID, target) {
				if match != nil {
					return nil, &hintError{
						fmt.Errorf("box id prefix %q matches more than one box", target),
						[]string{"use a longer prefix, or list boxes with 'pluto box ls'"},
					}
				}
				match = b
			}
		}
		if match != nil {
			return match, nil
		}
		return nil, &hintError{fmt.Errorf("no box with id prefix %q", target), []string{"list boxes with 'pluto box ls'"}}
	}
	return nil, &hintError{fmt.Errorf("no box for workspace %s", target), []string{"create it with 'pluto box create --workspace <path>'"}}
}

// boxWorkspace is a box's workspace path, falling back to the earlier
// worktree-based record's path until #150 removes it.
func boxWorkspace(b *state.Box) string {
	if b.WorkspacePath != "" {
		return b.WorkspacePath
	}
	return b.Worktree
}

// boxWakeLine renders a wake phase for `pluto box status`, naming the failing
// step (the declared command) and its exit code when it failed (#146).
func boxWakeLine(w state.PhaseStatus) string {
	detail := w.Command
	if w.ExitCode != 0 {
		if detail != "" {
			detail += " "
		}
		detail += fmt.Sprintf("exit %d", w.ExitCode)
	}
	if w.Error != "" {
		if detail != "" {
			detail += ": "
		}
		detail += w.Error
	}
	if detail == "" {
		return string(w.State)
	}
	return fmt.Sprintf("%s (%s)", w.State, detail)
}
