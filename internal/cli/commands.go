package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
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
	box, created, err := createWorktreeBox(client.New(socket), dir)
	if err != nil {
		return fail(stderr, err)
	}
	if created {
		fmt.Fprintf(stdout, "created box %s for %s/%s\n", short(box.ID), box.Project, box.Branch)
	} else {
		fmt.Fprintf(stdout, "box %s already exists for %s/%s\n", short(box.ID), box.Project, box.Branch)
	}
	running, err := client.New(socket).UpBox(box.ID)
	if err != nil {
		return fail(stderr, err)
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

// runRun runs a bounded command in a box, ensuring it is up first. The exit
// code is the command's; a second run on the same box is refused while one
// is active. Interrupting the client detaches it — the job keeps running in
// the box and its outcome is recorded.
func runRun(args []string, socket string, stdout, stderr io.Writer) int {
	target := ""
	var command []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			command = args[i+1:]
			break
		}
		if target != "" {
			fmt.Fprintln(stderr, "usage: pluto run [box-id|worktree] -- <command> [args...]")
			return 2
		}
		target = args[i]
	}
	if len(command) == 0 {
		fmt.Fprintln(stderr, "usage: pluto run [box-id|worktree] -- <command> [args...]")
		return 2
	}
	if target == "" {
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "pluto: %v\n", err)
			return 1
		}
		target = dir
	}
	c := client.New(socket)
	box, err := ensureBox(c, target)
	if err != nil {
		return fail(stderr, err)
	}
	job, err := c.RunJob(box.ID, command, stdout)
	if err != nil {
		return fail(stderr, err)
	}
	if job.State == state.JobDone {
		return 0
	}
	if job.ExitCode != 0 {
		return job.ExitCode
	}
	fmt.Fprintf(stderr, "pluto: job %s failed: %s\n", short(job.ID), job.Error)
	return 1
}

// ensureBox resolves a box id or worktree target, creating the box for a
// worktree the way `up` does.
func ensureBox(c *client.Client, target string) (*state.Box, error) {
	if state.ValidID(target) {
		return c.Box(target)
	}
	dir, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	box, _, err := createWorktreeBox(c, dir)
	return box, err
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
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", img.Version, img.BuiltAt, short(img.KernelSHA256), short(img.RootfsSHA256))
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
			fmt.Fprintf(stdout, "service:  %s %s%s\n", svc.Name, svc.State, port)
		}
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
		fmt.Fprintf(stderr, "pluto: unknown phase %q (want provision or wake)\n", *phase)
		return 2
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
			return fail(stderr, err)
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
				fmt.Fprintf(stderr, "pluto: skipping %s log: %v\n", name, err)
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
