package cli

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/state"
)

// This file is the schedule surface: list a host's schedules with next due
// and last outcome, show one schedule's occurrence history, and materialize
// one explicit occurrence for a demo without waiting for a cron window.

const scheduleUsage = "usage: pluto schedule ls|show|run"
const scheduleLsUsage = "usage: pluto schedule ls [box-id|worktree] [--json]"
const scheduleShowUsage = "usage: pluto schedule show <box-id|worktree> <name> [--json]"
const scheduleRunUsage = "usage: pluto schedule run <box-id|worktree> <name> [--json]"

func runScheduleCommand(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelpAtStart(args, "schedule", commandHelp, stdout) {
		return 0
	}
	if len(args) == 0 {
		return usageError(stderr, "schedule needs a subcommand", scheduleUsage)
	}
	switch args[0] {
	case "ls":
		return scheduleList(args[1:], socket, stdout, stderr)
	case "show":
		return scheduleShow(args[1:], socket, stdout, stderr)
	case "run":
		return scheduleRun(args[1:], socket, stdout, stderr)
	default:
		return unknownSubcommand(stderr, "schedule", args[0], []string{"ls", "show", "run"})
	}
}

func scheduleList(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "schedule ls", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("schedule ls", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := cliutil.ParseCommand(fs, splitFlags(args), stderr, scheduleLsUsage); code != 0 {
		return code
	}
	if fs.NArg() > 1 {
		return usageError(stderr, "schedule ls takes at most one box", scheduleLsUsage)
	}
	c := client.New(socket)
	boxID := ""
	if fs.NArg() == 1 {
		box, err := resolveBox(c, fs.Arg(0))
		if err != nil {
			return fail(stderr, err, "list boxes with 'pluto ls'")
		}
		boxID = box.ID
	}
	schedules, err := c.Schedules(boxID)
	if err != nil {
		return fail(stderr, err)
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, api.ScheduleListResponse{SchemaVersion: 1, Schedules: schedules})
	}
	if len(schedules) == 0 {
		fmt.Fprintln(stdout, "no schedules")
		fmt.Fprintln(stdout, "next: declare one in .pluto.toml with [[schedule]] and apply it with 'pluto up'")
		return 0
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "BOX\tNAME\tCRON\tNEXT\tLAST FIRED\tOUTCOME\tRUN/JOB")
	for _, sched := range schedules {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			short(sched.BoxID), sched.Name, sched.Cron,
			scheduleDueCell(sched.NextDueAt), scheduleFiredCell(sched),
			scheduleOutcomeCell(sched.LastOutcome), scheduleRunJobCell(sched))
	}
	if err := w.Flush(); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "next: run one now with 'pluto schedule run %s %s'\n", short(schedules[0].BoxID), schedules[0].Name)
	return 0
}

func scheduleShow(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "schedule show", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("schedule show", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := cliutil.ParseCommand(fs, splitFlags(args), stderr, scheduleShowUsage); code != 0 {
		return code
	}
	if fs.NArg() != 2 {
		return usageError(stderr, "schedule show requires a box and a schedule name", scheduleShowUsage)
	}
	c := client.New(socket)
	box, err := resolveBox(c, fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list boxes with 'pluto ls'")
	}
	name := fs.Arg(1)
	schedules, err := c.Schedules(box.ID)
	if err != nil {
		return fail(stderr, err)
	}
	info, ok := findScheduleInfo(schedules, name)
	if !ok {
		return fail(stderr, fmt.Errorf("box %s has no schedule %q", short(box.ID), name),
			fmt.Sprintf("list its schedules with 'pluto schedule ls %s'", short(box.ID)))
	}
	tasks, err := c.Tasks()
	if err != nil {
		return fail(stderr, err)
	}
	occurrences := scheduleOccurrences(tasks, box.ID, name)
	if *jsonOutput {
		return writeTaskJSON(stdout, api.ScheduleShowResponse{SchemaVersion: 1, Schedule: info, Occurrences: occurrences})
	}
	fmt.Fprintf(stdout, "box:      %s (%s/%s)\n", short(box.ID), box.Project, box.Branch)
	fmt.Fprintf(stdout, "name:     %s\n", info.Name)
	fmt.Fprintf(stdout, "cron:     %s\n", info.Cron)
	if info.Job != "" {
		fmt.Fprintf(stdout, "job:      %s\n", info.Job)
	} else {
		fmt.Fprintln(stdout, "job:      none (warm-up)")
	}
	fmt.Fprintf(stdout, "armed:    %s\n", info.ArmedAt.Local().Format("2006-01-02 15:04"))
	fmt.Fprintf(stdout, "next:     %s\n", scheduleDueCell(info.NextDueAt))
	fmt.Fprintf(stdout, "last:     %s\n", scheduleLastLine(info))
	if len(occurrences) == 0 {
		fmt.Fprintln(stdout, "occurrences: none yet")
		fmt.Fprintf(stdout, "next: run one now with 'pluto schedule run %s %s'\n", short(box.ID), name)
		return 0
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "OCCURRENCE\tSOURCE\tRUN\tJOB\tSTATE\tREASON")
	for _, occ := range occurrences {
		jobID := occ.Job
		if jobID == "" && occ.JobID != "" {
			jobID = short(occ.JobID)
		}
		if jobID == "" {
			jobID = "-"
		}
		reason := oneLine(occ.Reason)
		if reason == "" {
			reason = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", occ.Occurrence, occ.Source, short(occ.RunID), jobID, occ.State, reason)
	}
	if err := w.Flush(); err != nil {
		return fail(stderr, err)
	}
	if last := occurrences[0]; last.RunID != "" {
		fmt.Fprintf(stdout, "next: read its output with 'pluto run logs %s'\n", short(last.RunID))
	}
	return 0
}

func scheduleRun(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "schedule run", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("schedule run", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON")
	if code := cliutil.ParseCommand(fs, splitFlags(args), stderr, scheduleRunUsage); code != 0 {
		return code
	}
	if fs.NArg() != 2 {
		return usageError(stderr, "schedule run requires a box and a schedule name", scheduleRunUsage)
	}
	c := workClient(socket)
	box, err := resolveBox(c, fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "list boxes with 'pluto ls'")
	}
	key, err := idempotencyKey()
	if err != nil {
		return fail(stderr, err)
	}
	run, err := c.RunSchedule(box.ID, fs.Arg(1), api.ScheduleRunRequest{IdempotencyKey: key, TraceID: c.TraceID})
	if err != nil {
		return fail(stderr, err, fmt.Sprintf("list its schedules with 'pluto schedule ls %s'", short(box.ID)))
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, run)
	}
	fmt.Fprintf(stdout, "occurrence %s queued for schedule %s on box %s\n", run.Occurrence, run.Schedule, short(run.BoxID))
	fmt.Fprintf(stdout, "run %s %s\n", short(run.Run.ID), run.Run.State)
	fmt.Fprintf(stdout, "next: follow it with 'pluto events --trace %s' or 'pluto task show %s'\n", run.TraceID, short(run.Task.ID))
	return 0
}

// findScheduleInfo picks one schedule out of a listing.
func findScheduleInfo(list []api.ScheduleInfo, name string) (api.ScheduleInfo, bool) {
	for _, sched := range list {
		if sched.Name == name {
			return sched, true
		}
	}
	return api.ScheduleInfo{}, false
}

// scheduleOccurrences derives a schedule's history from durable tasks: the
// scheduler and an explicit run both materialize occurrences as tasks whose
// source is "schedule" and whose ref is the schedule name. Newest first.
func scheduleOccurrences(tasks []state.Task, boxID, name string) []api.ScheduleOccurrence {
	out := make([]api.ScheduleOccurrence, 0)
	for _, task := range tasks {
		if task.Source != "schedule" || task.BoxID != boxID || task.Ref != name {
			continue
		}
		for i := range task.Runs {
			run := &task.Runs[i]
			occ := api.ScheduleOccurrence{
				Source:     run.QueueSource,
				TaskID:     task.ID,
				RunID:      run.ID,
				JobID:      run.JobID,
				Job:        run.Job,
				State:      run.State,
				Reason:     run.Reason,
				QueuedAt:   run.CreatedAt,
				StartedAt:  run.StartedAt,
				FinishedAt: run.FinishedAt,
			}
			if occ.Source == "" {
				occ.Source = state.QueueScheduled
			}
			if run.Event != nil && run.Event.ObjectID != "" {
				occ.Occurrence = run.Event.ObjectID
			} else {
				occ.Occurrence = run.CreatedAt.UTC().Format(time.RFC3339)
			}
			out = append(out, occ)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].QueuedAt.After(out[j].QueuedAt) })
	return out
}

// scheduleDueCell renders a next-due time with a relative hint. A past time
// is an occurrence waiting for the next scheduler tick, not an error.
func scheduleDueCell(due *time.Time) string {
	if due == nil {
		return "-"
	}
	at := due.Local()
	delta := time.Until(*due)
	if delta < 0 {
		return at.Format("2006-01-02 15:04") + " (overdue)"
	}
	return at.Format("2006-01-02 15:04") + " (in " + delta.Round(time.Minute).String() + ")"
}

// scheduleFiredCell shows the last cron slot materialized, or "manual" when
// only an explicit occurrence has completed.
func scheduleFiredCell(sched api.ScheduleInfo) string {
	if sched.LastFired != nil {
		return sched.LastFired.Local().Format("2006-01-02 15:04")
	}
	if sched.LastRunID != "" {
		return "manual"
	}
	return "-"
}

func scheduleOutcomeCell(outcome string) string {
	if outcome == "" {
		return "-"
	}
	return outcome
}

// scheduleRunJobCell shows the last occurrence's run and job ids.
func scheduleRunJobCell(sched api.ScheduleInfo) string {
	switch {
	case sched.LastRunID != "" && sched.LastJobID != "":
		return short(sched.LastRunID) + "/" + short(sched.LastJobID)
	case sched.LastRunID != "":
		return short(sched.LastRunID)
	case sched.LastJobID != "":
		return short(sched.LastJobID)
	default:
		return "-"
	}
}

// scheduleLastLine is the one-line summary `schedule show` prints.
func scheduleLastLine(sched api.ScheduleInfo) string {
	if sched.LastOutcome == "" {
		return "no occurrence has completed yet"
	}
	line := sched.LastOutcome
	if sched.LastJobID != "" {
		line += " (job " + short(sched.LastJobID) + ")"
	} else if sched.LastRunID != "" {
		line += " (run " + short(sched.LastRunID) + ")"
	}
	return line
}

// statusScheduleLine is the compact schedule summary `status` adds for each
// stored schedule.
func statusScheduleLine(sched state.Schedule) string {
	line := sched.Name + " " + sched.Cron
	if sched.NextDueAt != nil {
		line += " next " + scheduleDueCell(sched.NextDueAt)
	}
	if sched.LastOutcome != "" {
		line += " last " + sched.LastOutcome
	}
	if sched.LastJobID != "" {
		line += " (job " + short(sched.LastJobID) + ")"
	}
	return line
}
