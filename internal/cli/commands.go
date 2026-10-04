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

func runUp(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	worktree := fs.String("worktree", "", "worktree path (default: current directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := *worktree
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "pluto: %v\n", err)
			return 1
		}
	}
	root, branch, err := gitInfo(dir)
	if err != nil {
		fmt.Fprintf(stderr, "pluto: %v\n", err)
		return 1
	}
	box, created, err := client.New(socket).CreateBox(api.CreateBoxRequest{
		Worktree: root,
		Project:  filepath.Base(root),
		Branch:   branch,
	})
	if err != nil {
		return fail(stderr, err)
	}
	if created {
		fmt.Fprintf(stdout, "created box %s for %s/%s\n", shortID(box.ID), box.Project, box.Branch)
	} else {
		fmt.Fprintf(stdout, "box %s already exists for %s/%s\n", shortID(box.ID), box.Project, box.Branch)
	}
	running, err := client.New(socket).UpBox(box.ID)
	if err != nil {
		return fail(stderr, err)
	}
	if running.Image != "" {
		fmt.Fprintf(stdout, "box %s running (image %s)\n", shortID(running.ID), shortImage(running.Image))
	} else {
		fmt.Fprintf(stdout, "box %s running\n", shortID(running.ID))
	}
	return 0
}

func runPause(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pause", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto pause <box-id|worktree>")
		return 2
	}
	box, err := resolveBox(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	paused, err := client.New(socket).PauseBox(box.ID)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "box %s paused\n", shortID(paused.ID))
	return 0
}

func runImage(args []string, socket string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pluto image <import <artifact-dir>|ls>")
		return 2
	}
	c := client.New(socket)
	switch args[0] {
	case "import":
		fs := flag.NewFlagSet("image import", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: pluto image import <artifact-dir>")
			return 2
		}
		dir, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "pluto: %v\n", err)
			return 1
		}
		version, err := c.ImportImage(dir)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "imported image %s\n", version)
		return 0
	case "ls":
		images, err := c.ListImages()
		if err != nil {
			return fail(stderr, err)
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tBUILT\tKERNEL\tROOTFS")
		for _, img := range images {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", img.Version, img.BuiltAt, shortImage(img.KernelSHA256), shortImage(img.RootfsSHA256))
		}
		w.Flush()
		return 0
	default:
		fmt.Fprintf(stderr, "unknown image subcommand %q\n", args[0])
		return 2
	}
}

func runLs(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	list, err := client.New(socket).ListBoxes()
	if err != nil {
		return fail(stderr, err)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPROJECT/BRANCH\tSTATE\tCREATED")
	for _, b := range list.Boxes {
		fmt.Fprintf(w, "%s\t%s/%s\t%s\t%s\n", shortID(b.ID), b.Project, b.Branch, b.State, b.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	w.Flush()
	for _, e := range list.Errors {
		fmt.Fprintf(stderr, "warning: unreadable box record %s: %s\n", e.Path, e.Err)
	}
	return 0
}

func runStatus(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto status <box-id|worktree>")
		return 2
	}
	box, err := resolveBox(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "id:       %s\n", box.ID)
	fmt.Fprintf(stdout, "project:  %s\n", box.Project)
	fmt.Fprintf(stdout, "branch:   %s\n", box.Branch)
	fmt.Fprintf(stdout, "worktree: %s\n", box.Worktree)
	fmt.Fprintf(stdout, "state:    %s\n", box.State)
	if box.Image != "" {
		fmt.Fprintf(stdout, "image:    %s\n", box.Image)
	}
	fmt.Fprintf(stdout, "created:  %s\n", box.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(stdout, "updated:  %s\n", box.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(stdout, "attach:   pluto attach %s\n", shortID(box.ID))
	return 0
}

func runDestroy(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(splitFlags(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto destroy <box-id|worktree> [--yes]")
		return 2
	}
	c := client.New(socket)
	target := fs.Arg(0)

	// A box ID can be destroyed even when its record is unreadable, so that
	// corrupt records have a repair path.
	var id, label string
	if state.ValidID(target) {
		id = target
		box, err := c.Box(target)
		switch {
		case err == nil:
			label = fmt.Sprintf("%s/%s, worktree %s", box.Project, box.Branch, box.Worktree)
		case errors.Is(err, client.ErrNotFound):
			return fail(stderr, err)
		default:
			label = "unreadable record"
		}
	} else {
		box, err := resolveBox(c, target)
		if err != nil {
			return fail(stderr, err)
		}
		id = box.ID
		label = fmt.Sprintf("%s/%s, worktree %s", box.Project, box.Branch, box.Worktree)
	}

	if !*yes {
		if !isTerminal(os.Stdin) {
			fmt.Fprintln(stderr, "pluto: refusing to destroy without confirmation; pass --yes")
			return 1
		}
		fmt.Fprintf(stdout, "destroy box %s (%s)? [y/N] ", shortID(id), label)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stderr, "aborted")
			return 1
		}
	}
	if err := c.DestroyBox(id); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "destroyed box %s (%s)\n", shortID(id), label)
	return 0
}
