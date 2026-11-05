package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Siddhj2206/pluto/internal/host"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
	"github.com/Siddhj2206/pluto/internal/version"
)

// noInput is set by --no-input: never prompt, and fail where a prompt would be needed.
var noInput bool

// command is one node of the command tree. Daily verbs sit at the top; the
// things you manage are a noun with verbs under it (ADR 0009).
type command struct {
	name    string
	aliases []string
	args    string // Shown after the name in help, such as "<task> [--detach]".
	summary string
	run     func(args []string) error
	subs    []*command
	hidden  bool
}

func (c *command) find(name string) *command {
	for _, s := range c.subs {
		if s.name == name || slices.Contains(s.aliases, name) {
			return s
		}
	}
	return nil
}

func commands() *command {
	return &command{name: "pluto", summary: "Pluto runs CI and tasks on machines you own.", subs: []*command{
		{name: "run", args: "<task> [--detach]", summary: "Run a task from .pluto, using your working tree", run: runCmd},
		{name: "exec", args: "[--detach] <command...>", summary: "Run a one-off command in this repo's box", run: execCmd},
		{name: "history", args: "[--since 24h] [--limit n] [--json]", summary: "List runs, newest first", run: historyCmd},
		{name: "logs", args: "<run> [--follow]", summary: "Show a run's log", run: logsCmd},
		{name: "cancel", args: "<run>", summary: "Stop a task's run", run: cancelCmd},
		{name: "enter", args: "[<run>]", summary: "Open a shell in a run's box, or this repo's box", run: enterCmd},
		{name: "task", summary: "Tasks defined in this repo's .pluto", subs: []*command{
			{name: "list", aliases: []string{"ls"}, args: "[--json]", summary: "List tasks, with schedules and last results", run: taskListCmd},
		}},
		{name: "schedule", summary: "Run this repo's tasks on a schedule, from a committed branch", subs: []*command{
			{name: "list", aliases: []string{"ls"}, args: "[--json]", summary: "List scheduled tasks on this host", run: scheduleListCmd},
			{name: "enable", args: "[--branch <name>]", summary: "Follow a branch, by default the repo's default branch", run: scheduleEnableCmd},
			{name: "disable", summary: "Stop running this repo's schedules", run: scheduleDisableCmd},
			{name: "run", args: "<task> [--detach]", summary: "Run a task now from the committed branch, as its schedule would", run: scheduleRunCmd},
		}},
		{name: "box", summary: "This repo's durable box", subs: []*command{
			{name: "list", aliases: []string{"ls"}, args: "[--json]", summary: "List boxes, with their size", run: boxListCmd},
			{name: "reset", args: "[--yes]", summary: "Delete this repo's box, including /workspace and home", run: boxResetCmd},
			{name: "grant", args: "--privileged", summary: "Let this repo's box run privileged", run: boxGrantCmd(true)},
			{name: "revoke", args: "--privileged", summary: "Take that back", run: boxGrantCmd(false)},
		}},
		{name: "secret", summary: "This repo's secrets on this host", subs: []*command{
			{name: "set", args: "<NAME>", summary: "Set a secret from a hidden prompt or a pipe", run: secretSetCmd},
			{name: "list", aliases: []string{"ls"}, args: "[--json]", summary: "List secret names", run: secretListCmd},
			{name: "delete", aliases: []string{"rm"}, args: "<NAME>", summary: "Delete a secret", run: secretDeleteCmd},
		}},
		{name: "ci", summary: "Run a git host's CI jobs here", subs: []*command{
			{name: "connect", args: "github", summary: "Connect this repo", run: ciConnectCmd},
			{name: "list", aliases: []string{"ls"}, args: "[--json]", summary: "List connected repos and stuck jobs", run: ciListCmd},
		}},
		{name: "host", summary: "The background service that does the work", subs: []*command{
			{name: "start", summary: "Install and start the service, or restart it", run: noArgs(hostStart)},
			{name: "stop", summary: "Stop the service. Boxes keep running", run: noArgs(func() error { return runSystemctl("disable", "--now", "pluto-host.service") })},
			{name: "status", summary: "Show whether the service is running", run: noArgs(hostStatus)},
			{name: "run", summary: "Run the service in the foreground", run: noArgs(hostRun), hidden: true},
		}},
		{name: "doctor", summary: "Check this machine and print what to do next", run: noArgs(doctor)},
		{name: "version", summary: "Show the version", run: noArgs(func() error { fmt.Println("pluto", version.Version); return nil })},
	}}
}

// renamedCommands point people at a command's new name.
var renamedCommands = map[string]string{"runs": "history", "ps": "history"}

func noArgs(f func() error) func([]string) error {
	return func(args []string) error {
		if len(args) > 0 {
			return usageError("unexpected argument " + args[0])
		}
		return f()
	}
}

// Main runs the command line and returns an exit code.
func Main(args []string) int {
	if err := dispatch(args); err != nil {
		var ue *userError
		if errors.As(err, &ue) {
			fmt.Fprintf(os.Stderr, "error: %s\n", ue.msg)
			if ue.next != "" {
				fmt.Fprintf(os.Stderr, "  Next: %s\n", ue.next)
			}
			return ue.code
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitError
	}
	return exitOK
}

func usageError(msg string) error { return &userError{msg: msg, next: "pluto help", code: exitUsage} }

// globalFlags takes --no-input from anywhere before a "--".
func globalFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i, a := range args {
		// After "--", or once exec reaches its command, the words aren't Pluto's.
		if a == "--" || len(out) > 0 && out[0] == "exec" && len(out) > 1 && !strings.HasPrefix(out[len(out)-1], "-") {
			return append(out, args[i:]...)
		}
		if a == "--no-input" {
			noInput = true
			continue
		}
		out = append(out, a)
	}
	return out
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func dispatch(args []string) error {
	args = globalFlags(args)
	root := commands()
	if len(args) == 0 {
		printHelp(root, nil)
		return nil
	}
	if args[0] == "help" {
		cmd, path := resolve(root, args[1:])
		printHelp(cmd, path)
		return nil
	}
	if args[0] == "--version" {
		fmt.Println("pluto", version.Version)
		return nil
	}
	cmd, path := resolve(root, args)
	rest := args[len(path):]
	helpArgs := rest
	if cmd.name == "exec" && len(rest) > 0 {
		helpArgs = rest[:1] // Anything after the command's first word is the command's own.
	}
	if wantsHelp(helpArgs) || cmd.run == nil && len(rest) == 0 {
		printHelp(cmd, path)
		return nil
	}
	if cmd.run == nil {
		if cmd == root {
			if renamed, ok := renamedCommands[rest[0]]; ok {
				return &userError{msg: "pluto " + rest[0] + " is now pluto " + renamed + ".", next: "pluto " + renamed, code: exitUsage}
			}
			return usageError("unknown command " + rest[0])
		}
		return usageError(fmt.Sprintf("unknown command %q for pluto %s", rest[0], strings.Join(path, " ")))
	}
	return cmd.run(rest)
}

// resolve walks the tree as far as the arguments name commands.
func resolve(root *command, args []string) (*command, []string) {
	cmd := root
	var path []string
	for _, a := range args {
		next := cmd.find(a)
		if next == nil {
			break
		}
		cmd = next
		path = append(path, a)
	}
	return cmd, path
}

func printHelp(cmd *command, path []string) {
	name := strings.Join(append([]string{"pluto"}, path...), " ")
	if cmd.run != nil {
		fmt.Printf("Usage: %s %s\n\n%s.\n", name, cmd.args, cmd.summary)
		return
	}
	fmt.Println(strings.TrimSuffix(cmd.summary, ".") + ".")
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	for _, s := range cmd.subs {
		if s.hidden {
			continue
		}
		label := strings.TrimSpace(name + " " + s.name + " " + s.args)
		if s.run == nil {
			label = name + " " + s.name + " <command>"
		}
		fmt.Fprintf(tw, "  %s\t%s\n", label, s.summary)
	}
	tw.Flush()
	if cmd.name == "pluto" {
		fmt.Println("\nEvery list takes --json. --yes skips a confirmation, and --no-input fails instead of prompting.")
		fmt.Println("Run pluto help <command> for more.")
	}
}

// parse reads flags anywhere among the arguments, as people expect from
// `pluto run build --detach`. Everything after "--" is left alone.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var tail []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, tail = args[:i], args[i+1:]
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, &userError{msg: err.Error(), next: "pluto help", code: exitUsage}
		}
		args = fs.Args()
		if len(args) == 0 {
			return append(positional, tail...), nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func newFlags(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }

func printJSON(key string, v any) error {
	return writeJSON(map[string]any{"schema": 1, key: v})
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func exactly(n int, args []string, usage string) error {
	if len(args) != n {
		return usageError("usage: pluto " + usage)
	}
	return nil
}

func runCmd(args []string) error {
	fs := newFlags("run")
	detach := fs.Bool("detach", false, "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(1, args, "run <task> [--detach]"); err != nil {
		return err
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	cfg, err := task.LoadDir(repo.root)
	if err != nil {
		return &userError{msg: err.Error(), code: exitError}
	}
	t, ok := cfg.Tasks[args[0]]
	if !ok {
		names := taskNames(cfg)
		msg := "no task named " + args[0] + " in " + task.Dir
		if len(names) > 0 {
			msg += ". Tasks: " + strings.Join(names, ", ")
		}
		next := "pluto task list"
		if strings.Contains(args[0], " ") {
			next = "pluto exec " + args[0]
		}
		return &userError{msg: msg + ".", next: next, code: exitUsage}
	}
	return submitTask(repo, cfg, t, *detach)
}

func taskNames(cfg *task.Config) []string {
	names := make([]string, 0, len(cfg.Tasks))
	for name := range cfg.Tasks {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func execCmd(args []string) error {
	fs := newFlags("exec")
	detach := fs.Bool("detach", false, "")
	// Flags only before the command, so `pluto exec ls -la` passes -la to ls.
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return &userError{msg: err.Error(), next: "pluto help exec", code: exitUsage}
	}
	args = fs.Args()
	if len(args) == 0 {
		return usageError("usage: pluto exec [--detach] <command...>")
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	cfg, err := task.LoadDir(repo.root)
	if err != nil {
		return &userError{msg: err.Error(), code: exitError}
	}
	t := task.Task{Name: "exec", Run: []string{commandLine(args)}, Timeout: task.DefaultTimeout}
	return submitTask(repo, cfg, t, *detach)
}

func submitTask(repo repoInfo, cfg *task.Config, t task.Task, detach bool) error {
	c := newClient()
	if err := c.require("tasks"); err != nil {
		return err
	}
	upload, err := c.upload(repo.root)
	if err != nil {
		return err
	}
	var run store.Run
	req := host.RunTaskRequest{Repo: repo.key, Upload: upload, Plan: host.Plan{Task: t, Image: cfg.Image, Requires: cfg.Requires}}
	if err := c.json(http.MethodPost, "/v1/tasks/run", req, &run); err != nil {
		return err
	}
	return watch(c, run, detach)
}

// watch follows a new run to its end, or just names it with --detach.
func watch(c *client, run store.Run, detach bool) error {
	if detach {
		fmt.Println(run.ID)
		fmt.Fprintf(os.Stderr, "Queued %s. Follow it with: pluto logs %s --follow\n", run.ID, run.ID)
		return nil
	}
	fmt.Fprintf(os.Stderr, "Run %s. Ctrl-C stops watching, and the run keeps going. To stop it: pluto cancel %s\n", run.ID, run.ID)
	state, err := followRun(c, run.ID)
	if err != nil {
		return err
	}
	var final store.Run
	if err := c.json(http.MethodGet, "/v1/runs/"+url.PathEscape(run.ID), nil, &final); err != nil {
		return err
	}
	from := ""
	if final.SHA != "" {
		from = fmt.Sprintf(", from %s at %s", final.Ref, shortSHA(final.SHA))
	}
	if state == store.StatePassed {
		fmt.Fprintf(os.Stderr, "Run %s passed%s.\n", run.ID, from)
		return nil
	}
	msg := fmt.Sprintf("run %s %s%s", run.ID, state, from)
	if n := note(final); n != "" {
		msg += ": " + n
	}
	next := ""
	if !final.StartedAt.IsZero() && state != store.StateCancelled {
		next = "pluto enter " + run.ID // A run that never started has nothing to look at.
	}
	return &userError{msg: msg, next: next, code: exitError}
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func historyCmd(args []string) error {
	fs := newFlags("history")
	asJSON := fs.Bool("json", false, "")
	limit := fs.Int("limit", 30, "")
	since := fs.String("since", "", "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(0, args, "history [--since 24h] [--limit n] [--json]"); err != nil {
		return err
	}
	c := newClient()
	if err := c.require("runs"); err != nil {
		return err
	}
	runs, err := fetchRuns(c, *limit, *since)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON("runs", runs)
	}
	if len(runs) == 0 {
		fmt.Fprintln(os.Stderr, "No runs. A run appears when a job with the pluto label is queued, or when a task starts.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tREPO\tJOB\tREF\tSTATE\tSTARTED\tTOOK\tNOTE")
	for _, r := range runs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Repo, dash(r.Job), dash(shortRef(r.Ref)), r.State, ago(r.CreatedAt), took(r), note(r))
	}
	return tw.Flush()
}

func fetchRuns(c *client, limit int, since string) ([]store.Run, error) {
	path := fmt.Sprintf("/v1/runs?limit=%d", limit)
	if since != "" {
		path += "&since=" + url.QueryEscape(since)
	}
	var runs []store.Run
	return runs, c.json(http.MethodGet, path, nil, &runs)
}

func logsCmd(args []string) error {
	fs := newFlags("logs")
	follow := fs.Bool("follow", false, "")
	fs.BoolVar(follow, "f", false, "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(1, args, "logs <run> [--follow]"); err != nil {
		return err
	}
	id := args[0]
	c := newClient()
	if err := c.require("logs"); err != nil {
		return err
	}
	if *follow {
		if err := c.require("tasks"); err != nil {
			return err
		}
		state, err := followRun(c, id)
		if err != nil {
			return err
		}
		if state != store.StatePassed {
			return &userError{msg: "run " + id + " " + state + ".", code: exitError}
		}
		return nil
	}
	data, err := c.raw(http.MethodGet, "/v1/runs/"+url.PathEscape(id)+"/logs", nil)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func cancelCmd(args []string) error {
	if err := exactly(1, args, "cancel <run>"); err != nil {
		return err
	}
	c := newClient()
	if err := c.require("tasks"); err != nil {
		return err
	}
	if _, err := c.raw(http.MethodPost, "/v1/runs/"+url.PathEscape(args[0])+"/cancel", nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Cancelling %s.\n", args[0])
	return nil
}

func enterCmd(args []string) error {
	if len(args) > 1 {
		return usageError("usage: pluto enter [<run>]")
	}
	c := newClient()
	var resp host.EnterResponse
	if len(args) == 1 {
		if err := c.require("enter"); err != nil {
			return err
		}
		if err := c.json(http.MethodPost, "/v1/runs/"+url.PathEscape(args[0])+"/enter", nil, &resp); err != nil {
			return err
		}
	} else {
		repo, err := currentRepo()
		if err != nil {
			return err
		}
		if err := c.require("box.enter"); err != nil {
			return err
		}
		if err := c.json(http.MethodPost, "/v1/boxes/enter", host.RepoRequest{Repo: repo.key}, &resp); err != nil {
			return err
		}
	}
	if resp.Warning != "" {
		fmt.Fprintln(os.Stderr, resp.Warning)
	}
	fmt.Fprintln(os.Stderr, "Entering the box. Exit with Ctrl-D.")
	cmd := exec.Command(resp.Argv[0], resp.Argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return enterResult(cmd.Run())
}

// taskRow is one task as task list shows it.
type taskRow struct {
	Name      string `json:"name"`
	Steps     int    `json:"steps"`
	Schedule  string `json:"schedule,omitempty"`
	Timeout   string `json:"timeout"`
	LastRun   string `json:"last_run,omitempty"`
	LastState string `json:"last_state,omitempty"`
	LastAt    string `json:"last_at,omitempty"`
}

func taskListCmd(args []string) error {
	fs := newFlags("task list")
	asJSON := fs.Bool("json", false, "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(0, args, "task list [--json]"); err != nil {
		return err
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	cfg, err := task.LoadDir(repo.root)
	if err != nil {
		return &userError{msg: err.Error(), code: exitError}
	}
	// Last results are a bonus: the list still works with the host stopped.
	runs, _ := fetchRuns(newClient(), 500, "")
	rows := make([]taskRow, 0, len(cfg.Tasks))
	for _, name := range taskNames(cfg) {
		t := cfg.Tasks[name]
		row := taskRow{Name: name, Steps: len(t.Run), Schedule: t.Schedule, Timeout: t.Timeout.String()}
		for _, r := range runs {
			if r.Kind == store.KindTask && r.Repo == repo.key && r.Job == name && r.State != store.StateSkipped {
				row.LastRun, row.LastState, row.LastAt = r.ID, r.State, r.CreatedAt.Format(time.RFC3339)
				break
			}
		}
		rows = append(rows, row)
	}
	if *asJSON {
		return printJSON("tasks", rows)
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "No tasks in %s. See https://github.com/Siddhj2206/pluto/blob/rewrite/docs/tasks.md\n", task.Dir)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tSTEPS\tSCHEDULE\tTIMEOUT\tLAST")
	for _, r := range rows {
		last := "-"
		if r.LastRun != "" {
			last = r.LastRun + " " + r.LastState
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", r.Name, r.Steps, dash(r.Schedule), r.Timeout, last)
	}
	return tw.Flush()
}

func scheduleListCmd(args []string) error {
	fs := newFlags("schedule list")
	asJSON := fs.Bool("json", false, "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(0, args, "schedule list [--json]"); err != nil {
		return err
	}
	c := newClient()
	if err := c.require("schedules"); err != nil {
		return err
	}
	var list host.ScheduleList
	if err := c.json(http.MethodGet, "/v1/schedules", nil, &list); err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(map[string]any{"schema": 1, "schedules": list.Schedules, "sources": list.Sources})
	}
	return printSchedules(list)
}

func scheduleEnableCmd(args []string) error {
	fs := newFlags("schedule enable")
	branch := fs.String("branch", "", "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(0, args, "schedule enable [--branch <name>]"); err != nil {
		return err
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	if repo.url == "" {
		return &userError{msg: "this repo has no remote named origin, and schedules read from one.", code: exitError}
	}
	if *branch == "" {
		if *branch, err = defaultBranch(repo.root); err != nil {
			return err
		}
	}
	c := newClient()
	if err := c.require("schedules"); err != nil {
		return err
	}
	c.http.Timeout = 0 // A first fetch can take a while on a home connection.
	var list host.ScheduleList
	req := host.ScheduleEnableRequest{Repo: repo.key, URL: repo.url, Branch: *branch}
	if err := c.json(http.MethodPost, "/v1/schedules", req, &list); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Schedules for %s now follow the committed %s branch, in this machine's local time.\n", repo.key, *branch)
	return printSchedules(list)
}

func scheduleDisableCmd(args []string) error {
	if err := exactly(0, args, "schedule disable"); err != nil {
		return err
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	c := newClient()
	if err := c.require("schedules"); err != nil {
		return err
	}
	if _, err := c.raw(http.MethodDelete, "/v1/schedules?repo="+url.QueryEscape(repo.key), nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Schedules for %s are off. Its box and runs stay.\n", repo.key)
	return nil
}

func scheduleRunCmd(args []string) error {
	fs := newFlags("schedule run")
	detach := fs.Bool("detach", false, "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(1, args, "schedule run <task> [--detach]"); err != nil {
		return err
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	c := newClient()
	if err := c.require("schedule.run"); err != nil {
		return err
	}
	var run store.Run
	if err := c.json(http.MethodPost, "/v1/schedules/run", host.ScheduleRunRequest{Repo: repo.key, Task: args[0]}, &run); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Running %s from the committed %s branch at %s. Uncommitted changes aren't included.\n", args[0], run.Ref, shortSHA(run.SHA))
	return watch(c, run, *detach)
}

func boxListCmd(args []string) error {
	fs := newFlags("box list")
	asJSON := fs.Bool("json", false, "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(0, args, "box list [--json]"); err != nil {
		return err
	}
	c := newClient()
	if err := c.require("boxes"); err != nil {
		return err
	}
	c.http.Timeout = 0 // Measuring a big build cache can take a while.
	var boxes []host.BoxInfo
	if err := c.json(http.MethodGet, "/v1/boxes", nil, &boxes); err != nil {
		return err
	}
	if *asJSON {
		return printJSON("boxes", boxes)
	}
	if len(boxes) == 0 {
		fmt.Fprintln(os.Stderr, "No boxes yet. A repo gets one the first time it runs a task.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tSTATE\tSIZE\tPRIVILEGED\tIMAGE")
	for _, b := range boxes {
		state := "stopped"
		if b.Running {
			state = "running"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\n", b.Repo, state, humanBytes(b.Bytes), b.Privileged, b.Image)
	}
	return tw.Flush()
}

func boxResetCmd(args []string) error {
	fs := newFlags("box reset")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(0, args, "box reset [--yes]"); err != nil {
		return err
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	if !*yes && !confirm(fmt.Sprintf("Delete %s's box, including its /workspace and home directory?", repo.key)) {
		return &userError{msg: "nothing was deleted.", next: "pluto box reset --yes", code: exitError}
	}
	c := newClient()
	if err := c.require("boxes"); err != nil {
		return err
	}
	if _, err := c.raw(http.MethodPost, "/v1/boxes/reset", host.RepoRequest{Repo: repo.key}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Reset %s's box. Its next run starts clean, with a fresh copy of the image.\n", repo.key)
	return nil
}

func boxGrantCmd(grant bool) func([]string) error {
	verb := "revoke"
	if grant {
		verb = "grant"
	}
	return func(args []string) error {
		fs := newFlags("box " + verb)
		privileged := fs.Bool(task.Privileged, false, "")
		args, err := parse(fs, args)
		if err != nil {
			return err
		}
		if len(args) != 0 || !*privileged {
			return usageError("usage: pluto box " + verb + " --privileged")
		}
		repo, err := currentRepo()
		if err != nil {
			return err
		}
		c := newClient()
		if err := c.require("boxes"); err != nil {
			return err
		}
		if !grant {
			q := url.Values{"repo": {repo.key}, "name": {task.Privileged}}
			if _, err := c.raw(http.MethodDelete, "/v1/grants?"+q.Encode(), nil); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s can no longer use privileged boxes.\n", repo.key)
			return nil
		}
		if _, err := c.raw(http.MethodPost, "/v1/grants", host.RepoRequest{Repo: repo.key, Name: task.Privileged}); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s can now use privileged boxes. Code in this repo can act with your user's rights on this machine.\n", repo.key)
		return nil
	}
}

func secretSetCmd(args []string) error {
	if err := exactly(1, args, "secret set <NAME>"); err != nil {
		return err
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	c := newClient()
	if err := c.require("secrets"); err != nil {
		return err
	}
	value, err := readSecret(args[0])
	if err != nil {
		return err
	}
	if _, err := c.raw(http.MethodPost, "/v1/secrets", host.RepoRequest{Repo: repo.key, Name: args[0], Value: value}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Set %s for %s on this host. Tasks get it by listing it under secrets:.\n", args[0], repo.key)
	return nil
}

func secretListCmd(args []string) error {
	fs := newFlags("secret list")
	asJSON := fs.Bool("json", false, "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(0, args, "secret list [--json]"); err != nil {
		return err
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	c := newClient()
	if err := c.require("secrets"); err != nil {
		return err
	}
	var names []string
	if err := c.json(http.MethodGet, "/v1/secrets?repo="+url.QueryEscape(repo.key), nil, &names); err != nil {
		return err
	}
	if *asJSON {
		return printJSON("secrets", names)
	}
	if len(names) == 0 {
		fmt.Fprintf(os.Stderr, "%s has no secrets.\n", repo.key)
		return nil
	}
	fmt.Println(strings.Join(names, "\n"))
	return nil
}

func secretDeleteCmd(args []string) error {
	if err := exactly(1, args, "secret delete <NAME>"); err != nil {
		return err
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	c := newClient()
	if err := c.require("secrets"); err != nil {
		return err
	}
	q := url.Values{"repo": {repo.key}, "name": {args[0]}}
	if _, err := c.raw(http.MethodDelete, "/v1/secrets?"+q.Encode(), nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Deleted %s.\n", args[0])
	return nil
}

func ciConnectCmd(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "github":
		return ciConnect()
	case len(args) == 1:
		return &userError{msg: "Pluto only connects to GitHub so far, not " + args[0] + ".", code: exitUsage}
	}
	return usageError("usage: pluto ci connect github")
}

func ciListCmd(args []string) error {
	fs := newFlags("ci list")
	asJSON := fs.Bool("json", false, "")
	args, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(0, args, "ci list [--json]"); err != nil {
		return err
	}
	return ciList(*asJSON)
}

func printSchedules(list host.ScheduleList) error {
	for _, s := range list.Sources {
		if s.Problem != "" {
			fmt.Fprintf(os.Stderr, "%s (%s): using the last good config. Problem: %s\n", s.Repo, s.Branch, s.Problem)
		}
	}
	if len(list.Schedules) == 0 {
		fmt.Fprintln(os.Stderr, "No scheduled tasks. Add a schedule: to a task in .pluto, commit it, and it appears within 15 minutes.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tTASK\tSCHEDULE\tNEXT\tLAST\tNOTE")
	for _, s := range list.Schedules {
		last := "-"
		if s.LastRun != "" {
			last = s.LastRun + " " + s.LastState
		}
		next := "-"
		if !s.Next.IsZero() {
			next = s.Next.Format("Jan 2 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Repo, s.Task, s.Expr, next, last, s.LastNote)
	}
	return tw.Flush()
}
