package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/workspace"
)

const projectUsage = "usage: pluto project <add <url|path>|ls|show NAME|rm NAME>"
const worktreeUsage = "usage: pluto worktree <ls [project]|rm <key|path>>"

// runProject dispatches the host's project registry manager.
func runProject(args []string, socket string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, projectUsage)
		return 2
	}
	if cliutil.MaybeHelpAtStart(args, "project", commandHelp, stdout) {
		return 0
	}
	switch args[0] {
	case "add":
		return runProjectAdd(args[1:], stdout, stderr)
	case "ls":
		return runProjectLs(args[1:], socket, stdout, stderr)
	case "show":
		return runProjectShow(args[1:], socket, stdout, stderr)
	case "rm":
		return runProjectRm(args[1:], socket, stdout, stderr)
	default:
		return unknownSubcommand(stderr, "project", args[0], []string{"add", "ls", "show", "rm"})
	}
}

// runProjectAdd registers a project once, cloning it under the managed root.
func runProjectAdd(args []string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "project add", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("project add", flag.ContinueOnError)
	name := fs.String("name", "", "project name (default: derived from the source)")
	branch := fs.String("branch", "", "default branch (default: the clone's HEAD)")
	if code := cliutil.ParseCommand(fs, splitFlags(args, "--name", "--branch"), stderr, "usage: pluto project add <url|path> [--name NAME] [--branch BRANCH]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "project add takes one repository URL or path", "usage: pluto project add <url|path> [--name NAME] [--branch BRANCH]")
	}
	w := workspace.Default()
	p, created, err := w.EnsureProject(fs.Arg(0), *name, *branch)
	if err != nil {
		return fail(stderr, err, "check the repository URL or path and the host's Git credentials")
	}
	if created {
		fmt.Fprintf(stdout, "project %s added\n", p.Name)
	} else {
		fmt.Fprintf(stdout, "project %s already registered\n", p.Name)
	}
	fmt.Fprintf(stdout, "source:         %s\n", p.Source)
	fmt.Fprintf(stdout, "default branch: %s\n", p.DefaultBranch)
	fmt.Fprintf(stdout, "clone:          %s\n", p.Repo)
	return 0
}

// runProjectLs lists the registry with each project's branch-worktree and box
// counts. Boxes are a best-effort daemon read; project management itself works
// with the daemon stopped.
func runProjectLs(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "project ls", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("project ls", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := cliutil.ParseCommand(fs, args, stderr, "usage: pluto project ls [--json]"); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "project ls takes no arguments", "usage: pluto project ls [--json]")
	}
	w := workspace.Default()
	projects, err := w.Projects()
	if err != nil {
		return fail(stderr, err)
	}
	boxes, boxesOK := projectBoxes(socket)
	if *jsonOutput {
		items := make([]projectJSON, 0, len(projects))
		for i := range projects {
			items = append(items, buildProjectJSON(w, &projects[i], boxes, boxesOK))
		}
		out := struct {
			SchemaVersion int           `json:"schema_version"`
			Projects      []projectJSON `json:"projects"`
		}{SchemaVersion: 1, Projects: items}
		if err := json.NewEncoder(stdout).Encode(out); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tDEFAULT\tWORKTREES\tBOXES")
	for i := range projects {
		p := &projects[i]
		worktrees, _ := w.Worktrees(p.Name)
		linked := 0
		for _, wt := range worktrees {
			if !wt.Default {
				linked++
			}
		}
		boxCell := "-"
		if boxesOK {
			boxCell = fmt.Sprintf("%d", len(relatedBoxes(boxes, w, p)))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", p.Name, p.Source, p.DefaultBranch, linked, boxCell)
	}
	tw.Flush()
	return 0
}

// runProjectShow shows one project: source, clone, worktrees, and boxes.
func runProjectShow(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "project show", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("project show", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := cliutil.ParseCommand(fs, splitFlags(args), stderr, "usage: pluto project show NAME [--json]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "project show requires one project name", "usage: pluto project show NAME [--json]")
	}
	w := workspace.Default()
	p, err := w.Project(fs.Arg(0))
	if err != nil {
		if errors.Is(err, workspace.ErrProjectNotFound) {
			return fail(stderr, err, "list projects with 'pluto project ls'")
		}
		return fail(stderr, err)
	}
	worktrees, err := w.Worktrees(p.Name)
	if err != nil {
		return fail(stderr, err)
	}
	boxes, boxesOK := projectBoxes(socket)
	if *jsonOutput {
		out := struct {
			SchemaVersion int         `json:"schema_version"`
			Project       projectJSON `json:"project"`
		}{SchemaVersion: 1, Project: buildProjectJSON(w, p, boxes, boxesOK)}
		if err := json.NewEncoder(stdout).Encode(out); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	fmt.Fprintf(stdout, "name:           %s\n", p.Name)
	fmt.Fprintf(stdout, "source:         %s\n", p.Source)
	fmt.Fprintf(stdout, "default branch: %s\n", p.DefaultBranch)
	fmt.Fprintf(stdout, "clone:          %s\n", p.Repo)
	fmt.Fprintf(stdout, "created:        %s\n", p.CreatedAt.Local().Format("2006-01-02 15:04"))
	if p.LastFetch != nil {
		fmt.Fprintf(stdout, "last fetch:     %s\n", p.LastFetch.Local().Format("2006-01-02 15:04"))
	} else {
		fmt.Fprintln(stdout, "last fetch:     never")
	}
	fmt.Fprintln(stdout, "worktrees:")
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  KEY\tBRANCH\tPATH\tBOX")
	for _, wt := range worktrees {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", wt.Key, wt.Branch, wt.Path, worktreeBoxCell(boxForWorktree(boxes, wt.Path)))
	}
	tw.Flush()
	related := relatedBoxes(boxes, w, p)
	if !boxesOK {
		fmt.Fprintln(stdout, "boxes:          unknown (daemon unreachable)")
		return 0
	}
	if len(related) == 0 {
		fmt.Fprintln(stdout, "boxes:          none")
		return 0
	}
	fmt.Fprintln(stdout, "boxes:")
	bw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(bw, "  ID\tPROJECT/BRANCH\tSTATE\tWORKTREE")
	for _, b := range related {
		fmt.Fprintf(bw, "  %s\t%s/%s\t%s\t%s\n", short(b.ID), b.Project, b.Branch, b.State, b.Worktree)
	}
	bw.Flush()
	return 0
}

// runProjectRm removes a project's worktrees, clone, and registry entry. It
// refuses while boxes exist or a checkout is dirty unless --force.
func runProjectRm(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "project rm", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("project rm", flag.ContinueOnError)
	force := fs.Bool("force", false, "remove even with boxes or uncommitted changes")
	if code := cliutil.ParseCommand(fs, splitFlags(args), stderr, "usage: pluto project rm NAME [--force]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "project rm requires one project name", "usage: pluto project rm NAME [--force]")
	}
	w := workspace.Default()
	p, err := w.Project(fs.Arg(0))
	if err != nil {
		if errors.Is(err, workspace.ErrProjectNotFound) {
			return fail(stderr, err, "list projects with 'pluto project ls'")
		}
		return fail(stderr, err)
	}
	boxes, boxesOK := projectBoxes(socket)
	if !boxesOK && !*force {
		return fail(stderr, errors.New("cannot check boxes: the daemon is unreachable"), "start the daemon with 'pluto daemon', or pass --force")
	}
	if boxesOK {
		if related := relatedBoxes(boxes, w, p); len(related) > 0 && !*force {
			return fail(stderr, fmt.Errorf("project %q has %d box(es)", p.Name, len(related)),
				"destroy them with 'pluto destroy <id> --yes', or pass --force")
		}
	}
	if err := w.RemoveProject(p.Name, *force); err != nil {
		return fail(stderr, err, "commit or discard changes, or pass --force")
	}
	fmt.Fprintf(stdout, "project %s removed\n", p.Name)
	return 0
}

// projectJSON is the --json shape for one project.
type projectJSON struct {
	Name          string         `json:"name"`
	Source        string         `json:"source"`
	DefaultBranch string         `json:"default_branch"`
	Repo          string         `json:"repo"`
	CreatedAt     time.Time      `json:"created_at"`
	LastFetch     *time.Time     `json:"last_fetch,omitempty"`
	Worktrees     []worktreeJSON `json:"worktrees"`
	Boxes         []boxJSON      `json:"boxes"`
}

type worktreeJSON struct {
	Key     string   `json:"key"`
	Project string   `json:"project"`
	Branch  string   `json:"branch"`
	Path    string   `json:"path"`
	Default bool     `json:"default"`
	Box     *boxJSON `json:"box,omitempty"`
}

type boxJSON struct {
	ID       string `json:"id"`
	Project  string `json:"project"`
	Branch   string `json:"branch"`
	State    string `json:"state"`
	Worktree string `json:"worktree"`
}

func buildProjectJSON(w *workspace.Workspace, p *workspace.Project, boxes []*state.Box, boxesOK bool) projectJSON {
	out := projectJSON{
		Name: p.Name, Source: p.Source, DefaultBranch: p.DefaultBranch,
		Repo: p.Repo, CreatedAt: p.CreatedAt, LastFetch: p.LastFetch,
		Worktrees: []worktreeJSON{}, Boxes: []boxJSON{},
	}
	worktrees, _ := w.Worktrees(p.Name)
	for _, wt := range worktrees {
		item := worktreeJSON{Key: wt.Key, Project: wt.Project, Branch: wt.Branch, Path: wt.Path, Default: wt.Default}
		if b := boxForWorktree(boxes, wt.Path); b != nil {
			item.Box = boxToJSON(b)
		}
		out.Worktrees = append(out.Worktrees, item)
	}
	if boxesOK {
		for _, b := range relatedBoxes(boxes, w, p) {
			out.Boxes = append(out.Boxes, *boxToJSON(b))
		}
	}
	return out
}

func boxToJSON(b *state.Box) *boxJSON {
	return &boxJSON{ID: b.ID, Project: b.Project, Branch: b.Branch, State: string(b.State), Worktree: b.Worktree}
}

// projectBoxes reads the daemon's box list. The second result is false when
// the daemon is unreachable, which is not an error for read-only project
// commands.
func projectBoxes(socket string) ([]*state.Box, bool) {
	list, err := client.New(socket).ListBoxes()
	if err != nil {
		return nil, false
	}
	return list.Boxes, true
}

// relatedBoxes returns the boxes that belong to a project, by identity or by
// a checkout under its managed clone or flat worktrees.
func relatedBoxes(boxes []*state.Box, w *workspace.Workspace, p *workspace.Project) []*state.Box {
	repo := filepath.Clean(p.Repo)
	worktrees := filepath.Clean(w.WorktreesPath()) + string(filepath.Separator)
	var out []*state.Box
	for _, b := range boxes {
		path := filepath.Clean(b.Worktree)
		if b.Project == p.Name ||
			path == repo ||
			strings.HasPrefix(path, repo+string(filepath.Separator)) ||
			(strings.HasPrefix(path, worktrees) && strings.HasPrefix(filepath.Base(path), p.Name+"-")) {
			out = append(out, b)
		}
	}
	return out
}

func boxForWorktree(boxes []*state.Box, path string) *state.Box {
	clean := filepath.Clean(path)
	for _, b := range boxes {
		if filepath.Clean(b.Worktree) == clean {
			return b
		}
	}
	return nil
}

func worktreeBoxCell(b *state.Box) string {
	if b == nil {
		return "-"
	}
	return fmt.Sprintf("%s (%s)", short(b.ID), b.State)
}

// warnManagedTarget is the one-time nudge when a positional target is a host
// path rather than a managed project or key.
func warnManagedTarget(stderr io.Writer, path string) {
	fmt.Fprintf(stderr, "warning: %s is a host path; managed targets are preferred (see 'pluto project ls')\n", path)
}

// statDir reports whether path exists and is a directory.
func statDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
