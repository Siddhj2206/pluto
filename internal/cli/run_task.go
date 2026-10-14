package cli

import (
	"encoding/json"
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

const runUsageLine = "usage: pluto run <task>"

// runTask runs a task declared in pluto.yml in its workspace's durable box.
func runTask(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "run", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	if code := parseCommand(fs, args, stderr, runUsageLine); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, runUsageLine)
		return 2
	}
	dir, project, ok, err := yamlContractDir()
	if !ok {
		return fail(stderr, errors.New("no pluto.yml in this workspace"), "create one with 'pluto init'")
	}
	if err != nil {
		return fail(stderr, err, contractRunHint(err, "pluto run")...)
	}
	return runDeclaredTask(dir, project, fs.Arg(0), socket, stdout, stderr)
}

// runLs dispatches `pluto ls runs`, the recorded-run listing.
func runLs(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelpAtStart(args, "ls runs", stdout) {
		return 0
	}
	if len(args) == 0 || args[0] != "runs" {
		return usageError(stderr, "ls lists recorded runs", "usage: pluto ls runs [--json]")
	}
	return runLsRuns(args[1:], socket, stdout, stderr)
}

// logsUsage is the usage printed for a logs parse, arity, or selector error.
const logsUsage = "usage: pluto logs <run-id> [--follow] [--lines N]"

// runLogs shows a recorded run's retained step output, or follows a live run.
func runLogs(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "logs", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	lines := fs.Int("lines", 0, "lines to show (0 shows all retained output)")
	follow := fs.Bool("follow", false, "follow a live run until it finishes")
	fs.BoolVar(follow, "f", false, "follow a live run until it finishes")
	if code := parseCommand(fs, splitFlags(args, "--lines"), stderr, logsUsage); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, logsUsage)
		return 2
	}
	c := client.New(socket)
	run, err := c.Run(fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list runs with 'pluto ls runs'")
	}
	return printRunLogs(c, run, *lines, *follow, stdout, stderr)
}

// printLog renders one titled log block.
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

// writeTaskJSON emits one versioned JSON document without HTML escaping.
func writeTaskJSON(w io.Writer, value any) int {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return 1
	}
	return 0
}

// yamlContractDir returns the current worktree's root and its loaded pluto.yml
// when one exists. ok is false when there is no pluto.yml, so the legacy TOML
// path keeps its meaning until #150/#151 retire it.
func yamlContractDir() (dir string, project *contract.Project, ok bool, err error) {
	cwd, e := os.Getwd()
	if e != nil {
		return "", nil, false, nil
	}
	dir = cwd
	if root, _, e := gitInfo(cwd); e == nil {
		dir = root
	}
	if _, e := os.Stat(filepath.Join(dir, contract.YAMLFileName)); e != nil {
		return dir, nil, false, nil
	}
	project, err = contract.LoadProject(dir)
	return dir, project, true, err
}

// currentWorktreeDeclaresTask reports whether the current worktree's pluto.yml
// declares a task with this exact name.
func currentWorktreeDeclaresTask(name string) bool {
	_, project, ok, err := yamlContractDir()
	if !ok || err != nil {
		return false
	}
	_, declared := project.Tasks[name]
	return declared
}

// runDeclaredTask runs a task declared in pluto.yml in the workspace's durable
// box and records it. It is the `pluto run <task>` shape; the legacy job and
// ad-hoc spellings keep their meaning when no pluto.yml is present.
func runDeclaredTask(dir string, project *contract.Project, name, socket string, stdout, stderr io.Writer) int {
	if _, err := project.TaskExec(name); err != nil {
		return fail(stderr, err, "list the workspace's tasks in pluto.yml")
	}
	run, err := client.New(socket).RunTask(api.RunTaskRequest{Workspace: dir, Task: name}, stdout)
	if err != nil {
		return fail(stderr, err, "list the workspace's tasks in pluto.yml")
	}
	return finishDeclaredRun(run, stderr)
}

// runLsRuns lists the host's recorded runs, newest first. With --json it emits
// the versioned run schema for scripts.
func runLsRuns(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "ls runs", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("ls runs", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, args, stderr, "usage: pluto ls runs [--json]"); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "ls runs takes no arguments", "usage: pluto ls runs [--json]")
	}
	list, err := client.New(socket).ListRuns()
	if err != nil {
		return fail(stderr, err)
	}
	if *jsonOutput {
		if list.Runs == nil {
			list.Runs = []*state.Run{}
		}
		return writeTaskJSON(stdout, struct {
			SchemaVersion int          `json:"schema_version"`
			Runs          []*state.Run `json:"runs"`
		}{state.RunSchema, list.Runs})
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTASK\tSTATE\tEXIT\tDURATION\tSTARTED")
	for _, run := range list.Runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			short(run.ID), run.Task, run.State, runExit(run), runDuration(run),
			run.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	if err := w.Flush(); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// runExit renders a run's exit column: the code once the run has one.
func runExit(run *state.Run) string {
	if !run.State.Terminal() {
		return "-"
	}
	return strconv.Itoa(run.ExitCode)
}

// runDuration renders a run's duration column; empty until it finishes.
func runDuration(run *state.Run) string {
	if run.DurationMS <= 0 {
		return "-"
	}
	return (time.Duration(run.DurationMS) * time.Millisecond).Round(time.Millisecond).String()
}

// runShowRun shows one recorded run: its task, box, workspace, trigger,
// state, timing, exit code, and steps. With --json it emits the versioned run
// schema for scripts.
func runShowRun(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "show", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, splitFlags(args), stderr, "usage: pluto show <run-id> [--json]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto show <run-id> [--json]")
		return 2
	}
	run, err := client.New(socket).Run(fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list runs with 'pluto ls runs'")
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int        `json:"schema_version"`
			Run           *state.Run `json:"run"`
		}{state.RunSchema, run})
	}
	printRun(stdout, run)
	return 0
}

// printRun renders a run and its steps.
func printRun(stdout io.Writer, run *state.Run) {
	fmt.Fprintf(stdout, "id:        %s\n", run.ID)
	fmt.Fprintf(stdout, "task:      %s\n", run.Task)
	fmt.Fprintf(stdout, "box:       %s\n", run.BoxID)
	if run.WorkspacePath != "" {
		fmt.Fprintf(stdout, "workspace: %s\n", run.WorkspacePath)
	}
	fmt.Fprintf(stdout, "trigger:   %s\n", run.Trigger)
	fmt.Fprintf(stdout, "state:     %s\n", run.State)
	if run.State.Terminal() {
		fmt.Fprintf(stdout, "exit:      %d\n", run.ExitCode)
	}
	if run.Error != "" {
		fmt.Fprintf(stdout, "error:     %s\n", run.Error)
	}
	fmt.Fprintf(stdout, "created:   %s\n", run.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	if run.FinishedAt != nil {
		fmt.Fprintf(stdout, "finished:  %s\n", run.FinishedAt.Local().Format("2006-01-02 15:04:05"))
	}
	if run.DurationMS > 0 {
		fmt.Fprintf(stdout, "duration:  %s\n", (time.Duration(run.DurationMS) * time.Millisecond).Round(time.Millisecond))
	}
	for _, step := range run.Steps {
		fmt.Fprintf(stdout, "step %s: %s", step.Name, step.State)
		if step.State.Terminal() {
			fmt.Fprintf(stdout, " exit %d", step.ExitCode)
		}
		if step.StartedAt != nil && step.FinishedAt != nil {
			fmt.Fprintf(stdout, " in %s", step.FinishedAt.Sub(*step.StartedAt).Round(time.Millisecond))
		}
		fmt.Fprintln(stdout)
		if step.Command != "" {
			fmt.Fprintf(stdout, "  command: %s\n", step.Command)
		}
		if step.Log != "" {
			fmt.Fprintf(stdout, "  log:     %s\n", step.Log)
		}
		if step.Error != "" {
			fmt.Fprintf(stdout, "  error:   %s\n", step.Error)
		}
	}
}

// finishDeclaredRun maps a recorded run to the process exit code: the work's
// code wins, so scripts and pipelines see what the command saw.
func finishDeclaredRun(run *state.Run, stderr io.Writer) int {
	if run.State == state.RunDone {
		return 0
	}
	if run.Error != "" {
		fmt.Fprintf(stderr, "pluto: task %s failed: %s\n", run.Task, run.Error)
	}
	if run.ExitCode != 0 {
		return run.ExitCode
	}
	fmt.Fprintf(stderr, "next: show the run with 'pluto show %s'\n", short(run.ID))
	return 1
}

// runLogFollowInterval is how often a followed run's retained log is re-read.
const runLogFollowInterval = 100 * time.Millisecond

// printRunLogs renders a run's retained step output, or follows a live run and
// prints output as it grows until the run finishes.
func printRunLogs(c *client.Client, run *state.Run, lines int, follow bool, stdout, stderr io.Writer) int {
	if follow {
		return followRunLogs(c, run, stdout, stderr)
	}
	resp, err := c.RunLogs(run.ID)
	if err != nil {
		return fail(stderr, err, "list runs with 'pluto ls runs'")
	}
	if len(resp.Steps) == 0 {
		fmt.Fprintf(stdout, "run %s has no steps\n", short(run.ID))
		return 0
	}
	for _, step := range resp.Steps {
		printLog(stdout, "step "+step.Name, tailLines(step.Log, lines))
	}
	return 0
}

// followRunLogs polls a live run's record and retained logs, printing output as
// it appears, until the run reaches a terminal state.
func followRunLogs(c *client.Client, run *state.Run, stdout, stderr io.Writer) int {
	last := make(map[int]int)
	seen := make(map[int]bool)
	wrote := false
	for {
		current, err := c.Run(run.ID)
		if err != nil {
			return fail(stderr, err, "list runs with 'pluto ls runs'")
		}
		resp, err := c.RunLogs(run.ID)
		if err != nil {
			return fail(stderr, err)
		}
		for i, step := range resp.Steps {
			if len(step.Log) <= last[i] {
				continue
			}
			if !seen[i] {
				fmt.Fprintf(stdout, "== step %s ==\n", step.Name)
				seen[i] = true
			}
			fmt.Fprint(stdout, step.Log[last[i]:])
			last[i] = len(step.Log)
			wrote = true
		}
		if current.State.Terminal() {
			if wrote {
				fmt.Fprintln(stdout)
			}
			fmt.Fprintf(stdout, "run %s %s\n", short(current.ID), current.State)
			return 0
		}
		time.Sleep(runLogFollowInterval)
	}
}

// tailLines keeps the last n lines of s; n <= 0 keeps everything retained.
func tailLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "")
}
