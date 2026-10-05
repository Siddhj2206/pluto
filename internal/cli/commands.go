package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

func runUp(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "up", stdout) {
		return 0
	}
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
			return fail(stderr, err)
		}
	}
	box, created, err := createWorktreeBox(client.New(socket), dir)
	if err != nil {
		return fail(stderr, err, "run 'pluto up --worktree <path>' with the path to a git worktree")
	}
	if created {
		fmt.Fprintf(stdout, "created box %s for %s/%s\n", short(box.ID), box.Project, box.Branch)
	} else {
		fmt.Fprintf(stdout, "box %s already exists for %s/%s\n", short(box.ID), box.Project, box.Branch)
	}
	running, err := client.New(socket).UpBox(box.ID)
	if err != nil {
		return fail(stderr, err, contractRunHint(err, "pluto up")...)
	}
	if running.Image != "" {
		fmt.Fprintf(stdout, "box %s running (image %s)\n", short(running.ID), short(running.Image))
	} else {
		fmt.Fprintf(stdout, "box %s running\n", short(running.ID))
	}
	if summary := phaseSummary(running.Phases); summary != "" {
		fmt.Fprintf(stdout, "phases: %s\n", summary)
	}
	return 0
}

// runRun runs a declared job or an ad-hoc command in a box, ensuring it is
// up first. No arguments list the worktree's declared jobs; a lone argument
// without '--' is a job name, never a target. The exit code is the command's;
// a second run on the same box is refused while one is active. Interrupting
// the client detaches it — the job keeps running in the box.
func runRun(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "run", stdout) {
		return 0
	}
	positional, command, hasDash := splitRunArgs(args)
	if hasDash {
		if len(command) == 0 || len(positional) > 1 {
			runUsage(stderr)
			return 2
		}
		return runAdHoc(positional, command, socket, stdout, stderr)
	}
	switch len(positional) {
	case 0:
		return runListJobs(stdout, stderr)
	case 1, 2:
		return runNamedJob(positional, socket, stdout, stderr)
	default:
		runUsage(stderr)
		return 2
	}
}

// splitRunArgs separates the positionals before '--' from the ad-hoc command
// after it.
func splitRunArgs(args []string) (positional, command []string, hasDash bool) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
}

// runListJobs lists the current worktree's declared jobs, like `mise run`.
// It reads the contract directly: no box, daemon, or wake is involved. The
// contract lives at the worktree root — the same place run and up resolve
// it — so a subdirectory lists its worktree's jobs; a plain directory without
// git still lists its own contract.
func runListJobs(stdout, stderr io.Writer) int {
	dir, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	if root, _, err := gitInfo(dir); err == nil {
		dir = root
	}
	ct, err := contract.Load(dir)
	if err != nil {
		return fail(stderr, err, contractRunHint(err, "pluto run")...)
	}
	names := ct.JobNames()
	if len(names) == 0 {
		fmt.Fprintf(stdout, "no jobs declared in %s\n", filepath.Join(dir, contract.FileName))
		return 0
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	for _, name := range names {
		if desc := ct.Jobs[name].Description; desc != "" {
			fmt.Fprintf(w, "%s\t%s\n", name, desc)
		} else {
			fmt.Fprintln(w, name)
		}
	}
	w.Flush()
	return 0
}

// runNamedJob runs a declared job on a box. The job name resolves daemon-side
// against the worktree's current .pluto.toml at run time.
func runNamedJob(positional []string, socket string, stdout, stderr io.Writer) int {
	target, name := "", positional[0]
	if len(positional) == 2 {
		target, name = positional[0], positional[1]
	}
	explicitTarget := target != ""
	if target == "" {
		dir, err := os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
		target = dir
	}
	c := client.New(socket)
	box, err := ensureBox(c, target)
	if err != nil {
		return fail(stderr, err)
	}
	job, err := c.RunJob(box.ID, api.RunRequest{Job: name}, stdout)
	if err != nil {
		// A lone argument spelled like a path was probably the old
		// target-only form; show the ad-hoc spelling with it.
		if !explicitTarget && looksLikePath(name) {
			target, explicitTarget = name, true
		}
		return failNamedRun(stderr, err, target, explicitTarget)
	}
	return finishRun(job, box.ID, stderr)
}

// looksLikePath reports whether a lone run argument is spelled like a target
// rather than a job name: a path or a box id, never a job name.
func looksLikePath(arg string) bool {
	return strings.ContainsRune(arg, '/') || arg == "." || arg == ".." || idPrefix(arg)
}

// runAdHoc runs a one-off command with today's semantics: argv is exec'd
// directly, under the contract's top-level env.
func runAdHoc(positional, command []string, socket string, stdout, stderr io.Writer) int {
	target := ""
	if len(positional) == 1 {
		target = positional[0]
	}
	if target == "" {
		dir, err := os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
		target = dir
	}
	c := client.New(socket)
	box, err := ensureBox(c, target)
	if err != nil {
		return fail(stderr, err)
	}
	job, err := c.RunJob(box.ID, api.RunRequest{Argv: command}, stdout)
	if err != nil {
		return fail(stderr, err, contractRunHint(err, "pluto run")...)
	}
	return finishRun(job, box.ID, stderr)
}

// finishRun maps a recorded job outcome to the process exit code: the work's
// code wins, so scripts and pipelines see what the command saw. A failure
// without an exit code still names where to read the output.
func finishRun(job *state.Job, boxID string, stderr io.Writer) int {
	if job.State == state.JobDone {
		return 0
	}
	if job.ExitCode != 0 {
		return job.ExitCode
	}
	fmt.Fprintf(stderr, "pluto: job %s failed: %s\n", short(job.ID), job.Error)
	fmt.Fprintf(stderr, "next: read the output with 'pluto logs %s --job %s'\n", short(boxID), short(job.ID))
	return 1
}

// failNamedRun prints a failed job-name resolution. An unknown job gets the
// two ways forward: see the declared jobs, or run a one-off command.
func failNamedRun(stderr io.Writer, err error, target string, explicitTarget bool) int {
	var httpErr *client.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadRequest {
		return fail(stderr, err, contractRunHint(err, "pluto run")...)
	}
	fmt.Fprintf(stderr, "pluto: %v\n", err)
	fmt.Fprintln(stderr, "next: list declared jobs with 'pluto run'")
	if explicitTarget {
		fmt.Fprintf(stderr, "next: run a one-off command with 'pluto run %s -- <command>'\n", target)
	} else {
		fmt.Fprintln(stderr, "next: run a one-off command with 'pluto run -- <command>'")
	}
	return 1
}

func runUsage(stderr io.Writer) {
	fmt.Fprintln(stderr, "usage: pluto run [box-id|worktree] [job]")
	fmt.Fprintln(stderr, "       pluto run [box-id|worktree] -- <command> [args...]")
}

// ensureBox resolves a box id or worktree target, creating the box for a
// worktree the way `up` does.
func ensureBox(c *client.Client, target string) (*state.Box, error) {
	if state.ValidID(target) {
		return resolveBox(c, target)
	}
	dir, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	box, _, err := createWorktreeBox(c, dir)
	if err != nil {
		return nil, &hintError{err, []string{"run the command from inside a git worktree"}}
	}
	return box, nil
}

// createWorktreeBox registers (or returns) the box for a worktree directory,
// resolving its git root and branch. created reports a fresh box.
func createWorktreeBox(c *client.Client, dir string) (*state.Box, bool, error) {
	root, branch, err := gitInfo(dir)
	if err != nil {
		return nil, false, err
	}
	return c.CreateBox(api.CreateBoxRequest{
		Worktree: root,
		Project:  filepath.Base(root),
		Branch:   branch,
	})
}

func runPause(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "pause", stdout) {
		return 0
	}
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
	fmt.Fprintf(stdout, "box %s paused\n", short(paused.ID))
	return 0
}

func runImage(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelpAtStart(args, "image", stdout) {
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pluto image <import <artifact-dir>|ls>")
		return 2
	}
	c := client.New(socket)
	switch args[0] {
	case "import":
		if maybeHelp(args[1:], "image import", stdout) {
			return 0
		}
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
			return fail(stderr, err)
		}
		version, err := c.ImportImage(dir)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "imported image %s\n", version)
		return 0
	case "ls":
		if maybeHelp(args[1:], "image ls", stdout) {
			return 0
		}
		images, err := c.ListImages()
		if err != nil {
			return fail(stderr, err)
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tBUILT\tKERNEL\tROOTFS")
		for _, img := range images {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", img.Version, img.BuiltAt, short(img.KernelSHA256), short(img.RootfsSHA256))
		}
		w.Flush()
		return 0
	default:
		return unknownSubcommand(stderr, "image", args[0], []string{"import", "ls"})
	}
}

func runLs(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "ls", stdout) {
		return 0
	}
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
	fmt.Fprintln(w, "ID\tPROJECT/BRANCH\tSTATE\tPROVISION\tCREATED")
	for _, b := range list.Boxes {
		fmt.Fprintf(w, "%s\t%s/%s\t%s\t%s\t%s\n", short(b.ID), b.Project, b.Branch, b.State, provisionCell(b.Phases), b.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	w.Flush()
	for _, e := range list.Errors {
		fmt.Fprintf(stderr, "warning: unreadable box record %s: %s\n", e.Path, e.Err)
	}
	return 0
}

func runStatus(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "status", stdout) {
		return 0
	}
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
	// Re-fetch by id so the agent's live phases are included.
	box, err = client.New(socket).Box(box.ID)
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
	if box.Phases != nil {
		fmt.Fprintf(stdout, "synced:   %v\n", box.Phases.Synced)
		fmt.Fprintf(stdout, "provision: %s\n", phaseLine(box.Phases.Provision))
		fmt.Fprintf(stdout, "wake:     %s\n", phaseLine(box.Phases.Wake))
		for _, svc := range box.Phases.Services {
			port := ""
			if svc.Port > 0 {
				port = fmt.Sprintf(" (port %d)", svc.Port)
			}
			desc := ""
			if svc.Description != "" {
				desc = " - " + svc.Description
			}
			fmt.Fprintf(stdout, "service:  %s %s%s%s\n", svc.Name, svc.State, port, desc)
		}
	}
	// The daemon compares the worktree contract with the applied hash; a
	// matching contract prints nothing.
	if box.ContractStale {
		fmt.Fprintln(stdout, "contract: changed since this box applied it")
	}
	if latest := box.LatestJob(); latest != nil {
		fmt.Fprintf(stdout, "job:      %s\n", jobLine(latest))
	}
	if box.State == state.StateRunning {
		fmt.Fprintf(stdout, "auto-pause: %s\n", autoPauseLine(box))
	}
	fmt.Fprintf(stdout, "created:  %s\n", box.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(stdout, "updated:  %s\n", box.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(stdout, "attach:   pluto attach %s\n", short(box.ID))
	return 0
}

func runJobs(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "jobs", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto jobs <box-id|worktree>")
		return 2
	}
	box, err := resolveBox(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tEXIT\tDURATION\tSTARTED\tCOMMAND")
	for i := range box.Jobs {
		job := &box.Jobs[i]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			short(job.ID), job.State, jobExit(job), jobDuration(job),
			job.StartedAt.Local().Format("2006-01-02 15:04:05"), job.Command)
	}
	w.Flush()
	return 0
}

// jobExit renders a job's exit column: the code once it has one.
func jobExit(job *state.Job) string {
	if job.State == state.JobRunning {
		return "-"
	}
	return strconv.Itoa(job.ExitCode)
}

// jobDuration renders a job's duration column; empty until it finishes.
func jobDuration(job *state.Job) string {
	if job.DurationMS <= 0 {
		return "-"
	}
	return (time.Duration(job.DurationMS) * time.Millisecond).Round(time.Millisecond).String()
}

func runDestroy(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "destroy", stdout) {
		return 0
	}
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
			return fail(stderr, errors.New("refusing to destroy without confirmation"),
				fmt.Sprintf("confirm with 'pluto destroy %s --yes'", short(id)))
		}
		fmt.Fprintf(stdout, "destroy box %s (%s)? [y/N] ", short(id), label)
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
	fmt.Fprintf(stdout, "destroyed box %s (%s)\n", short(id), label)
	return 0
}

func runLogs(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "logs", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	phase := fs.String("phase", "", "provision or wake")
	service := fs.String("service", "", "show one service's journal")
	job := fs.String("job", "", "show a job's recorded output (id or 'last')")
	lines := fs.Int("lines", 100, "lines to show")
	if err := fs.Parse(splitFlags(args, "--phase", "--service", "--job", "--lines")); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto logs <box-id|worktree> [--phase provision|wake] [--service NAME] [--job ID|last] [--lines N]")
		return 2
	}
	if *phase != "" && *phase != "provision" && *phase != "wake" {
		return usageError(stderr, fmt.Sprintf("unknown phase %q (want provision or wake)", *phase),
			"usage: pluto logs <box-id|worktree> [--phase provision|wake] [--service NAME] [--job ID|last] [--lines N]")
	}
	box, err := resolveBox(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	c := client.New(socket)
	switch {
	case *service != "":
		log, err := c.Logs(box.ID, "", *service, *lines)
		if err != nil {
			return fail(stderr, err)
		}
		printLog(stdout, "service "+*service, log)
	case *phase != "":
		log, err := c.Logs(box.ID, *phase, "", *lines)
		if err != nil {
			return fail(stderr, err)
		}
		printLog(stdout, *phase, log)
	case *job != "":
		id, err := resolveJobID(box, *job)
		if err != nil {
			return fail(stderr, err, fmt.Sprintf("list the box's jobs with 'pluto jobs %s'", short(box.ID)))
		}
		log, err := c.JobLog(box.ID, id, *lines)
		if err != nil {
			return fail(stderr, err)
		}
		printLog(stdout, "job "+short(id), log)
	default:
		for _, name := range []string{"provision", "wake"} {
			log, err := c.Logs(box.ID, name, "", *lines)
			if err != nil {
				if box.State == state.StateRunning {
					return fail(stderr, err)
				}
				// Phase logs live in the guest; a paused box cannot serve
				// them, but its recorded job log is on the host.
				fmt.Fprintf(stderr, "warning: skipping %s log: %v\n", name, err)
				continue
			}
			printLog(stdout, name, log)
		}
		if latest := box.LatestJob(); latest != nil {
			log, err := c.JobLog(box.ID, latest.ID, *lines)
			if err != nil {
				return fail(stderr, err)
			}
			printLog(stdout, "job "+short(latest.ID), log)
		}
	}
	return 0
}

// resolveJobID turns a --job argument into a retained job's id: "last", a
// full id, or an unambiguous prefix.
func resolveJobID(box *state.Box, arg string) (string, error) {
	job, err := box.ResolveJob(arg)
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

func printLog(w io.Writer, title, log string) {
	fmt.Fprintf(w, "== %s ==\n", title)
	if strings.TrimSpace(log) == "" {
		fmt.Fprintln(w, "(no output)")
		return
	}
	fmt.Fprint(w, log)
	if !strings.HasSuffix(log, "\n") {
		fmt.Fprintln(w)
	}
}

// phaseSummary renders a box's contract state compactly; empty when there is
// nothing to say.
func phaseSummary(phases *state.Phases) string {
	if phases == nil {
		return ""
	}
	var parts []string
	if phases.Provision.State != "" {
		parts = append(parts, "provision "+string(phases.Provision.State))
	}
	if phases.Wake.State != "" {
		parts = append(parts, "wake "+string(phases.Wake.State))
	}
	if len(phases.Services) > 0 {
		active := 0
		for _, svc := range phases.Services {
			if svc.State == "active" {
				active++
			}
		}
		parts = append(parts, fmt.Sprintf("%d/%d services", active, len(phases.Services)))
	}
	return strings.Join(parts, ", ")
}

// phaseLine renders one phase for `pluto status`.
func phaseLine(phase state.PhaseStatus) string {
	if phase.State == "" {
		return "-"
	}
	line := string(phase.State)
	switch {
	case phase.Error != "":
		line += " (" + phase.Error + ")"
	case phase.ExitCode != 0:
		line += fmt.Sprintf(" (exit %d)", phase.ExitCode)
	}
	if phase.StartedAt != nil && phase.FinishedAt != nil {
		line += " in " + phase.FinishedAt.Sub(*phase.StartedAt).Round(time.Millisecond).String()
	}
	return line
}

// jobLine renders the box's latest job for `pluto status`.
func jobLine(job *state.Job) string {
	line := string(job.State)
	switch {
	case job.State == state.JobRunning:
		line += " (started " + job.StartedAt.Local().Format("15:04:05") + ")"
	case job.Error != "":
		line += " (" + job.Error + ")"
	case job.ExitCode != 0:
		line += fmt.Sprintf(" (exit %d)", job.ExitCode)
	}
	if job.DurationMS > 0 {
		line += " in " + (time.Duration(job.DurationMS) * time.Millisecond).Round(time.Millisecond).String()
	}
	return line + ": " + job.Command
}

// autoPauseLine explains what a running box's auto-pause is waiting on: an
// attached client, a running job, the remaining idle time, or a missing live
// view from the box.
func autoPauseLine(box *state.Box) string {
	switch box.AutoPauseSetting {
	case "", "unknown":
		// The daemon has not evaluated this box (or just lost its live view),
		// so it is not pausing it.
		return "blocked (no live view)"
	}
	window := contract.ResolveAutoPause(box.AutoPauseSetting)
	if window == 0 {
		return "off"
	}
	if box.Phases == nil || box.Phases.Clients == nil {
		return "blocked (client state unknown)"
	}
	if *box.Phases.Clients > 0 {
		return "blocked (client attached)"
	}
	if box.JobRunning() {
		return "blocked (job running)"
	}
	if box.IdleSince == nil {
		return fmt.Sprintf("idle 0s of %s (pauses in %s)", window, window)
	}
	idle := time.Since(*box.IdleSince).Round(time.Second)
	if idle < 0 {
		idle = 0
	}
	remaining := (window - idle).Round(time.Second)
	if remaining <= 0 {
		return fmt.Sprintf("due now (idle %s, window %s)", idle, window)
	}
	return fmt.Sprintf("idle %s of %s (pauses in %s)", idle, window, remaining)
}

// provisionCell is the `pluto ls` column for the provision phase.
func provisionCell(phases *state.Phases) string {
	if phases == nil || phases.Provision.State == "" {
		return "-"
	}
	return string(phases.Provision.State)
}
