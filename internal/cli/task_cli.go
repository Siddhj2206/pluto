package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/state"
)

const taskUsage = "usage: pluto task create|ls|show|follow|logs|changes|retry"
const taskCreateUsage = "usage: pluto task create --job NAME --prompt TEXT [--isolate] [box-id|worktree] [--json]"
const taskFollowUsage = "usage: pluto task follow <task-id> --prompt TEXT [--job NAME] [--json]"
const taskChangesUsage = "usage: pluto task changes <task-id> [--json]"
const taskRetryUsage = "usage: pluto task retry <task-id> [run-id] [--json]"
const runManagerUsage = "usage: pluto run ls|show|logs"

func runTaskCommand(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelpAtStart(args, "task", stdout) {
		return 0
	}
	if len(args) == 0 {
		return usageError(stderr, "task needs a subcommand", taskUsage)
	}
	switch args[0] {
	case "create":
		return taskCreate(args[1:], socket, stdout, stderr)
	case "ls":
		return taskList(args[1:], socket, stdout, stderr)
	case "show":
		return taskShow(args[1:], socket, stdout, stderr)
	case "follow":
		return taskFollow(args[1:], socket, stdout, stderr)
	case "logs":
		return taskLogs(args[1:], socket, stdout, stderr)
	case "changes":
		return taskChanges(args[1:], socket, stdout, stderr)
	case "retry":
		return taskRetry(args[1:], socket, stdout, stderr)
	default:
		return unknownSubcommand(stderr, "task", args[0], []string{"create", "ls", "show", "follow", "logs", "changes", "retry"})
	}
}

func taskCreate(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "task create", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("task create", flag.ContinueOnError)
	job := fs.String("job", "", "declared job to run")
	prompt := fs.String("prompt", "", "requested work")
	isolate := fs.Bool("isolate", false, "create a separate worktree and box")
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, splitFlags(args, "--job", "--prompt"), stderr, taskCreateUsage); code != 0 {
		return code
	}
	if *job == "" || strings.TrimSpace(*prompt) == "" || fs.NArg() > 1 {
		return usageError(stderr, "task create requires --job, --prompt, and at most one target", taskCreateUsage)
	}
	target := ""
	if fs.NArg() == 1 {
		target = fs.Arg(0)
	} else {
		var err error
		target, err = os.Getwd()
		if err != nil {
			return fail(stderr, err)
		}
	}
	box, err := ensureBox(client.New(socket), target)
	if err != nil {
		return fail(stderr, err, "create or select a box with 'pluto up'")
	}
	key, err := idempotencyKey()
	if err != nil {
		return fail(stderr, err)
	}
	task, err := client.New(socket).CreateTask(api.TaskRequest{
		BoxID: box.ID, Job: *job, Prompt: *prompt, Isolate: *isolate, IdempotencyKey: key,
	})
	if err != nil {
		return fail(stderr, err, "check the job with 'pluto job ls' and the box with 'pluto status'")
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int        `json:"schema_version"`
			Task          state.Task `json:"task"`
		}{1, *task})
	}
	first := latestRun(task)
	fmt.Fprintf(stdout, "task %s accepted (%s)\n", short(task.ID), task.State)
	if first != nil {
		fmt.Fprintf(stdout, "run %s %s\n", short(first.ID), first.State)
	}
	fmt.Fprintf(stdout, "next: inspect it with 'pluto task show %s'\n", short(task.ID))
	return 0
}

func taskList(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "task ls", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("task ls", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, args, stderr, "usage: pluto task ls [--json]"); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "task ls takes no arguments", "usage: pluto task ls [--json]")
	}
	tasks, err := client.New(socket).Tasks()
	if err != nil {
		return fail(stderr, err)
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int          `json:"schema_version"`
			Tasks         []state.Task `json:"tasks"`
		}{1, tasks})
	}
	if len(tasks) == 0 {
		fmt.Fprintln(stdout, "no tasks")
		fmt.Fprintln(stdout, "next: start one with 'pluto task create --job NAME --prompt TEXT'")
		return 0
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TASK\tSTATE\tPROJECT/REF\tRUNS\tUPDATED")
	for _, task := range tasks {
		projectRef := task.Project
		if task.Ref != "" {
			projectRef += "/" + task.Ref
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", short(task.ID), task.State, projectRef, len(task.Runs), task.UpdatedAt.Local().Format("2006-01-02 15:04"))
	}
	if err := w.Flush(); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "next: inspect a task with 'pluto task show %s'\n", short(tasks[0].ID))
	return 0
}

func taskShow(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "task show", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("task show", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, splitFlags(args), stderr, "usage: pluto task show <task-id> [--json]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "task show requires one task id", "usage: pluto task show <task-id> [--json]")
	}
	task, err := resolveTask(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list tasks with 'pluto task ls'")
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int        `json:"schema_version"`
			Task          state.Task `json:"task"`
		}{1, *task})
	}
	printTask(stdout, task)
	fmt.Fprintln(stdout, taskNextStep(task))
	return 0
}

func taskFollow(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "task follow", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("task follow", flag.ContinueOnError)
	prompt := fs.String("prompt", "", "follow-up requested work")
	jobName := fs.String("job", "", "declared job (defaults to the latest run's job)")
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, splitFlags(args, "--prompt", "--job"), stderr, taskFollowUsage); code != 0 {
		return code
	}
	if fs.NArg() != 1 || strings.TrimSpace(*prompt) == "" {
		return usageError(stderr, "task follow requires a task id and --prompt", taskFollowUsage)
	}
	c := client.New(socket)
	task, err := resolveTask(c, fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list tasks with 'pluto task ls'")
	}
	job := *jobName
	if job == "" {
		if latest := latestRun(task); latest != nil {
			job = latest.Job
		}
	}
	if job == "" {
		return fail(stderr, errors.New("task has no prior run to choose a job from"), "provide a job with '--job NAME'")
	}
	key, err := idempotencyKey()
	if err != nil {
		return fail(stderr, err)
	}
	run, err := c.CreateTaskRun(task.ID, api.TaskRunRequest{Job: job, Prompt: *prompt, IdempotencyKey: key})
	if err != nil {
		return fail(stderr, err, fmt.Sprintf("inspect the task with 'pluto task show %s'", short(task.ID)))
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int           `json:"schema_version"`
			TaskID        string        `json:"task_id"`
			Run           state.TaskRun `json:"run"`
		}{1, task.ID, *run})
	}
	fmt.Fprintf(stdout, "run %s accepted (%s) for task %s\n", short(run.ID), run.State, short(task.ID))
	fmt.Fprintf(stdout, "next: inspect it with 'pluto task show %s'\n", short(task.ID))
	return 0
}

// taskRetry recovers work by queueing a new run under the existing task with
// the retried run's job and prompt. It defaults to the latest run.
func taskRetry(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "task retry", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("task retry", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, splitFlags(args), stderr, taskRetryUsage); code != 0 {
		return code
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return usageError(stderr, "task retry requires a task id and optional run id", taskRetryUsage)
	}
	c := client.New(socket)
	task, err := resolveTask(c, fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list tasks with 'pluto task ls'")
	}
	run := latestRun(task)
	if fs.NArg() == 2 {
		run = findRun(task, fs.Arg(1))
		if run == nil {
			return fail(stderr, fmt.Errorf("task %s has no run matching %q", short(task.ID), fs.Arg(1)), fmt.Sprintf("list runs with 'pluto task show %s'", short(task.ID)))
		}
	}
	if run == nil {
		return fail(stderr, errors.New("task has no runs to retry"), fmt.Sprintf("add work with 'pluto task follow %s --prompt TEXT --job NAME'", short(task.ID)))
	}
	key, err := idempotencyKey()
	if err != nil {
		return fail(stderr, err)
	}
	retried, err := c.CreateTaskRun(task.ID, api.TaskRunRequest{RetryRunID: run.ID, IdempotencyKey: key})
	if err != nil {
		return fail(stderr, err, fmt.Sprintf("inspect the task with 'pluto task show %s'", short(task.ID)))
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int           `json:"schema_version"`
			TaskID        string        `json:"task_id"`
			RetriedRunID  string        `json:"retried_run_id"`
			Run           state.TaskRun `json:"run"`
		}{1, task.ID, run.ID, *retried})
	}
	fmt.Fprintf(stdout, "run %s accepted (%s) retrying run %s for task %s\n", short(retried.ID), retried.State, short(run.ID), short(task.ID))
	fmt.Fprintf(stdout, "next: inspect it with 'pluto task show %s'\n", short(task.ID))
	return 0
}

func runTaskRunCommand(args []string, socket string, stdout, stderr io.Writer) int {
	if len(args) == 0 || maybeHelpAtStart(args[1:], "run "+args[0], stdout) {
		if len(args) == 0 {
			return usageError(stderr, "run manager needs a subcommand", runManagerUsage)
		}
		return 0
	}
	switch args[0] {
	case "ls":
		return runList(args[1:], socket, stdout, stderr)
	case "show":
		return runShow(args[1:], socket, stdout, stderr)
	case "logs":
		return runLogsTask(args[1:], socket, stdout, stderr)
	default:
		return unknownSubcommand(stderr, "run", args[0], []string{"ls", "show", "logs"})
	}
}

func runList(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run ls", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, args, stderr, "usage: pluto run ls [--json]"); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "run ls takes no arguments", "usage: pluto run ls [--json]")
	}
	tasks, err := client.New(socket).Tasks()
	if err != nil {
		return fail(stderr, err)
	}
	runs := make([]runWithTask, 0)
	for _, task := range tasks {
		for _, run := range task.Runs {
			runs = append(runs, runWithTask{TaskID: task.ID, Project: task.Project, Run: run})
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Run.CreatedAt.After(runs[j].Run.CreatedAt) })
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int           `json:"schema_version"`
			Runs          []runWithTask `json:"runs"`
		}{1, runs})
	}
	if len(runs) == 0 {
		fmt.Fprintln(stdout, "no runs")
		fmt.Fprintln(stdout, "next: start work with 'pluto task create --job NAME --prompt TEXT'")
		return 0
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RUN\tTASK\tSTATE\tJOB\tPROMPT")
	for _, item := range runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", short(item.Run.ID), short(item.TaskID), item.Run.State, item.Run.Job, oneLine(item.Run.Prompt))
	}
	if err := w.Flush(); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "next: inspect a run with 'pluto run show %s'\n", short(runs[0].Run.ID))
	return 0
}

type runWithTask struct {
	TaskID  string        `json:"task_id"`
	Project string        `json:"project,omitempty"`
	Run     state.TaskRun `json:"run"`
}

func runShow(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run show", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, splitFlags(args), stderr, "usage: pluto run show <run-id> [--json]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "run show requires one run id", "usage: pluto run show <run-id> [--json]")
	}
	task, run, err := resolveRun(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list runs with 'pluto run ls'")
	}
	item := runWithTask{TaskID: task.ID, Project: task.Project, Run: *run}
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int         `json:"schema_version"`
			Run           runWithTask `json:"run"`
		}{1, item})
	}
	fmt.Fprintf(stdout, "run: %s\ntask: %s\nstate: %s\njob: %s\nprompt: %s\n", short(run.ID), short(task.ID), run.State, run.Job, run.Prompt)
	if task.Source != "" {
		fmt.Fprintf(stdout, "source: %s\n", task.Source)
	}
	if task.Project != "" {
		fmt.Fprintf(stdout, "project: %s\n", task.Project)
	}
	if task.Ref != "" {
		fmt.Fprintf(stdout, "ref: %s\n", task.Ref)
	}
	if run.Event != nil {
		fmt.Fprintf(stdout, "event: %s\n", runEventSummary(run.Event))
		if run.Event.URL != "" {
			fmt.Fprintf(stdout, "url: %s\n", run.Event.URL)
		}
	}
	if run.Reason != "" {
		fmt.Fprintf(stdout, "reason: %s\n", run.Reason)
	}
	fmt.Fprintf(stdout, "next: %s\n", runNextStepWithTask(task, run))
	return 0
}

func taskLogs(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "task logs", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("task logs", flag.ContinueOnError)
	lines := fs.Int("lines", 100, "maximum log lines")
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, splitFlags(args, "--lines"), stderr, "usage: pluto task logs <task-id> [run-id] [--lines N] [--json]"); code != 0 {
		return code
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return usageError(stderr, "task logs requires a task id and optional run id", "usage: pluto task logs <task-id> [run-id] [--lines N] [--json]")
	}
	task, err := resolveTask(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list tasks with 'pluto task ls'")
	}
	run := latestRun(task)
	if fs.NArg() == 2 {
		run = findRun(task, fs.Arg(1))
		if run == nil {
			return fail(stderr, fmt.Errorf("task %s has no run matching %q", short(task.ID), fs.Arg(1)), fmt.Sprintf("list runs with 'pluto task show %s'", short(task.ID)))
		}
	}
	return printTaskRunLog(task, run, *lines, *jsonOutput, socket, stdout, stderr)
}

func runLogsTask(args []string, socket string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run logs", flag.ContinueOnError)
	lines := fs.Int("lines", 100, "maximum log lines")
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, splitFlags(args, "--lines"), stderr, "usage: pluto run logs <run-id> [--lines N] [--json]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "run logs requires one run id", "usage: pluto run logs <run-id> [--lines N] [--json]")
	}
	task, run, err := resolveRun(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list runs with 'pluto run ls'")
	}
	return printTaskRunLog(task, run, *lines, *jsonOutput, socket, stdout, stderr)
}

func taskChanges(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "task changes", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("task changes", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := parseCommand(fs, splitFlags(args), stderr, taskChangesUsage); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "task changes requires one task id", taskChangesUsage)
	}
	task, err := resolveTask(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list tasks with 'pluto task ls'")
	}
	changes, err := client.New(socket).TaskChanges(task.ID)
	if err != nil {
		return fail(stderr, err, fmt.Sprintf("inspect the task with 'pluto task show %s'", short(task.ID)))
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int                     `json:"schema_version"`
			Changes       api.TaskChangesResponse `json:"changes"`
		}{1, *changes})
	}
	printTaskChanges(stdout, task, changes)
	return 0
}

// printTaskChanges reports the box's working-tree state with its attribution,
// so a shared box's changes are never presented as one task's work.
func printTaskChanges(w io.Writer, task *state.Task, changes *api.TaskChangesResponse) {
	fmt.Fprintf(w, "task: %s (%s)\n", short(task.ID), task.State)
	if changes.BoxID != "" {
		fmt.Fprintf(w, "box: %s (%s) %s\n", short(changes.BoxID), changes.BoxState, changes.Branch)
	}
	if changes.AttributionNote != "" {
		fmt.Fprintf(w, "changes: %s\n", changes.AttributionNote)
	}
	if changes.UnavailableReason != "" {
		fmt.Fprintf(w, "unavailable: %s\n", changes.UnavailableReason)
	}
	if len(changes.ChangedFiles) == 0 {
		fmt.Fprintln(w, "files: none")
	} else {
		fmt.Fprintln(w, "files:")
		for _, file := range changes.ChangedFiles {
			fmt.Fprintf(w, "  %s\n", file)
		}
	}
	if changes.Diff != "" {
		fmt.Fprintln(w, "diff:")
		fmt.Fprint(w, changes.Diff)
		if !strings.HasSuffix(changes.Diff, "\n") {
			fmt.Fprintln(w)
		}
	}
	if changes.DiffTruncated {
		fmt.Fprintln(w, "diff: truncated")
	}
	fmt.Fprintln(w, taskChangesNextStep(task, changes))
}

func taskChangesNextStep(task *state.Task, changes *api.TaskChangesResponse) string {
	if changes.Attribution == api.TaskChangesSharedBox {
		return "next: these changes are shared box state; start isolated work with 'pluto task create --isolate' for a task-specific diff"
	}
	return fmt.Sprintf("next: inspect the task with 'pluto task show %s'", short(task.ID))
}

func printTaskRunLog(task *state.Task, run *state.TaskRun, lines int, jsonOutput bool, socket string, stdout, stderr io.Writer) int {
	if run == nil {
		return fail(stderr, errors.New("task has no runs yet"), fmt.Sprintf("inspect the task with 'pluto task show %s'", short(task.ID)))
	}
	if run.JobID == "" {
		return fail(stderr, fmt.Errorf("run %s has no recorded job output yet (%s)", short(run.ID), run.State), fmt.Sprintf("check its state with 'pluto run show %s'", short(run.ID)))
	}
	log, err := client.New(socket).JobLog(task.BoxID, run.JobID, lines)
	if err != nil {
		return fail(stderr, err)
	}
	if jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int    `json:"schema_version"`
			TaskID        string `json:"task_id"`
			RunID         string `json:"run_id"`
			Log           string `json:"log"`
		}{1, task.ID, run.ID, log})
	}
	printLog(stdout, "run "+short(run.ID), log)
	return 0
}

func runJobCommand(args []string, stdout, stderr io.Writer) int {
	if maybeHelpAtStart(args, "job", stdout) {
		return 0
	}
	if len(args) == 2 && args[0] == "ls" && (args[1] == "-h" || args[1] == "--help") {
		if text, ok := commandHelp("job ls"); ok {
			fmt.Fprint(stdout, text)
		}
		return 0
	}
	if len(args) != 1 || args[0] != "ls" {
		return unknownSubcommand(stderr, "job", firstArg(args), []string{"ls"})
	}
	if maybeHelp(nil, "job ls", stdout) {
		return 0
	}
	return runListJobs(stdout, stderr)
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func resolveTask(c *client.Client, prefix string) (*state.Task, error) {
	tasks, err := c.Tasks()
	if err != nil {
		return nil, err
	}
	var match *state.Task
	for i := range tasks {
		if tasks[i].ID == prefix || (idPrefix(prefix) && strings.HasPrefix(tasks[i].ID, prefix)) {
			if match != nil {
				return nil, &hintError{fmt.Errorf("task id prefix %q matches more than one task", prefix), []string{"use a longer prefix, or list tasks with 'pluto task ls'"}}
			}
			match = &tasks[i]
		}
	}
	if match == nil {
		return nil, fmt.Errorf("no task with id %q", prefix)
	}
	return c.Task(match.ID)
}

func resolveRun(c *client.Client, prefix string) (*state.Task, *state.TaskRun, error) {
	tasks, err := c.Tasks()
	if err != nil {
		return nil, nil, err
	}
	var taskMatch *state.Task
	var runMatch *state.TaskRun
	for i := range tasks {
		for j := range tasks[i].Runs {
			run := &tasks[i].Runs[j]
			if run.ID == prefix || (idPrefix(prefix) && strings.HasPrefix(run.ID, prefix)) {
				if runMatch != nil {
					return nil, nil, &hintError{fmt.Errorf("run id prefix %q matches more than one run", prefix), []string{"use a longer prefix, or list runs with 'pluto run ls'"}}
				}
				taskMatch, runMatch = &tasks[i], run
			}
		}
	}
	if runMatch == nil {
		return nil, nil, fmt.Errorf("no run with id %q", prefix)
	}
	return taskMatch, runMatch, nil
}

func findRun(task *state.Task, prefix string) *state.TaskRun {
	var match *state.TaskRun
	for i := range task.Runs {
		run := &task.Runs[i]
		if run.ID == prefix || (idPrefix(prefix) && strings.HasPrefix(run.ID, prefix)) {
			if match != nil {
				return nil
			}
			match = run
		}
	}
	return match
}

func latestRun(task *state.Task) *state.TaskRun {
	if len(task.Runs) == 0 {
		return nil
	}
	return &task.Runs[len(task.Runs)-1]
}

func printTask(w io.Writer, task *state.Task) {
	fmt.Fprintf(w, "task: %s\nstate: %s\nsource: %s\nproject: %s\nref: %s\nbox: %s\ncreated: %s\nruns: %d\n", short(task.ID), task.State, task.Source, task.Project, task.Ref, short(task.BoxID), task.CreatedAt.Local().Format("2006-01-02 15:04:05"), len(task.Runs))
	if len(task.Runs) > 0 {
		tab := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tab, "RUN\tSTATE\tJOB\tPROMPT\tREASON")
		for _, run := range task.Runs {
			fmt.Fprintf(tab, "%s\t%s\t%s\t%s\t%s\n", short(run.ID), run.State, run.Job, oneLine(run.Prompt), oneLine(run.Reason))
		}
		_ = tab.Flush()
	}
	if task.BoxID != "" {
		fmt.Fprintf(w, "changes: inspect the box's working tree with 'pluto task changes %s'\n", short(task.ID))
	}
}

func taskNextStep(task *state.Task) string {
	if run := latestRun(task); run != nil {
		return runNextStepWithTask(task, run)
	}
	return fmt.Sprintf("next: add work with 'pluto task follow %s --prompt TEXT --job NAME'", short(task.ID))
}

// runNextStepWithTask names the next useful action, including recovery by
// retry for a run that failed, blocked, or was rejected.
func runNextStepWithTask(task *state.Task, run *state.TaskRun) string {
	switch run.State {
	case state.TaskRunFailed, state.TaskRunBlocked, state.TaskRunRejected:
		return fmt.Sprintf("inspect the output with 'pluto run logs %s', then retry with 'pluto task retry %s'", short(run.ID), short(task.ID))
	default:
		return runNextStep(run)
	}
}

// runEventSummary renders a trigger's stable provider metadata in one line.
func runEventSummary(event *state.EventContext) string {
	summary := event.Kind
	if event.Action != "" {
		summary += " " + event.Action
	}
	if event.Repo != "" {
		summary += " " + event.Repo
	}
	return summary
}

func runNextStep(run *state.TaskRun) string {
	switch run.State {
	case state.TaskRunQueued, state.TaskRunRunning:
		return fmt.Sprintf("check progress with 'pluto run show %s'", short(run.ID))
	case state.TaskRunCompleted:
		return fmt.Sprintf("read the output with 'pluto run logs %s'", short(run.ID))
	case state.TaskRunFailed, state.TaskRunBlocked, state.TaskRunRejected:
		return fmt.Sprintf("inspect the output with 'pluto run logs %s'", short(run.ID))
	default:
		return fmt.Sprintf("inspect the task with 'pluto task show %s'", short(run.ID))
	}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func writeTaskJSON(w io.Writer, value any) int {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return 1
	}
	return 0
}

func idempotencyKey() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("create request id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}
