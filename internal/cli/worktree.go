package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/workspace"
)

// runWorktree dispatches the managed-worktree manager.
func runWorktree(args []string, socket string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, worktreeUsage)
		return 2
	}
	if cliutil.MaybeHelpAtStart(args, "worktree", commandHelp, stdout) {
		return 0
	}
	switch args[0] {
	case "ls":
		return runWorktreeLs(args[1:], socket, stdout, stderr)
	case "rm":
		return runWorktreeRm(args[1:], socket, stdout, stderr)
	default:
		return unknownSubcommand(stderr, "worktree", args[0], []string{"ls", "rm"})
	}
}

// runWorktreeLs lists managed checkouts. The default branch is the managed
// clone; other branches have flat keys under the worktrees root.
func runWorktreeLs(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "worktree ls", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("worktree ls", flag.ContinueOnError)
	if code := cliutil.ParseCommand(fs, args, stderr, "usage: pluto worktree ls [project]"); code != 0 {
		return code
	}
	if fs.NArg() > 1 {
		return usageError(stderr, "worktree ls takes at most one project", "usage: pluto worktree ls [project]")
	}
	w := workspace.Default()
	worktrees, err := w.Worktrees(fs.Arg(0))
	if err != nil {
		if errors.Is(err, workspace.ErrProjectNotFound) {
			return fail(stderr, err, "list projects with 'pluto project ls'")
		}
		return fail(stderr, err)
	}
	boxes, _ := projectBoxes(socket)
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tPROJECT\tBRANCH\tPATH\tBOX")
	for _, wt := range worktrees {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", wt.Key, wt.Project, wt.Branch, wt.Path, worktreeBoxCell(boxForWorktree(boxes, wt.Path)))
	}
	tw.Flush()
	return 0
}

// runWorktreeRm removes one managed worktree. It refuses the default clone, a
// dirty checkout, or a checkout with a box unless --force.
func runWorktreeRm(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "worktree rm", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("worktree rm", flag.ContinueOnError)
	force := fs.Bool("force", false, "remove even with a box or uncommitted changes")
	if code := cliutil.ParseCommand(fs, splitFlags(args), stderr, "usage: pluto worktree rm <key|path> [--force]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "worktree rm requires one key or path", "usage: pluto worktree rm <key|path> [--force]")
	}
	w := workspace.Default()
	wt, err := w.FindWorktree(fs.Arg(0))
	if err != nil {
		if errors.Is(err, workspace.ErrWorktreeNotFound) {
			return fail(stderr, err, "list worktrees with 'pluto worktree ls'")
		}
		return fail(stderr, err)
	}
	if wt.Default {
		return fail(stderr, fmt.Errorf("worktree %s is the managed clone for the default branch", wt.Key), "remove the project with 'pluto project rm "+wt.Project+"'")
	}
	boxes, boxesOK := projectBoxes(socket)
	if !boxesOK && !*force {
		return fail(stderr, errors.New("cannot check boxes: the daemon is unreachable"), "start the daemon with 'pluto daemon', or pass --force")
	}
	if b := boxForWorktree(boxes, wt.Path); b != nil && !*force {
		return fail(stderr, fmt.Errorf("worktree %s has box %s", wt.Key, short(b.ID)),
			"destroy it with 'pluto destroy "+short(b.ID)+" --yes', or pass --force")
	}
	if _, err := w.RemoveWorktree(wt.Key, *force); err != nil {
		return fail(stderr, err, "commit or discard changes, or pass --force")
	}
	fmt.Fprintf(stdout, "worktree %s removed\n", wt.Key)
	return 0
}
