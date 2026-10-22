package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

const timelineUsage = "usage: pluto timeline <box|task|run|trace-id> [--json]"

// timelineLimit bounds one chain read. A trace is normally a handful of
// events; a busy box can have more history than a terminal can show.
const timelineLimit = 1000

// timelineTarget is the resolved identity of a timeline read: exactly one of
// box, task/run, or trace, plus the label the caller used.
type timelineTarget struct {
	Target  string
	BoxID   string
	TaskID  string
	RunID   string
	TraceID string
}

// runTimeline reads one causal chain from the daemon's activity ledger and
// prints it in sequence order. It never creates work and adds no store.
func runTimeline(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "timeline", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print versioned JSON in sequence order")
	if code := cliutil.ParseCommand(fs, splitFlags(args), stderr, timelineUsage); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(stderr, "timeline takes one box, task, run, or trace id", timelineUsage)
	}
	target, err := resolveTimelineTarget(client.New(socket), fs.Arg(0))
	if err != nil {
		return fail(stderr, err, "pass a box id or worktree, a task or run id, or a trace id (trc_...)")
	}

	c := client.New(socket)
	list, err := readTimeline(c, target)
	if err != nil {
		return fail(stderr, err)
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, struct {
			SchemaVersion int            `json:"schema_version"`
			Target        string         `json:"target"`
			TraceID       string         `json:"trace_id,omitempty"`
			BoxID         string         `json:"box_id,omitempty"`
			TaskID        string         `json:"task_id,omitempty"`
			RunID         string         `json:"run_id,omitempty"`
			Events        []events.Event `json:"events"`
		}{1, target.Target, target.TraceID, target.BoxID, target.TaskID, target.RunID, list})
	}
	if len(list) == 0 {
		fmt.Fprintf(stdout, "no events for %s\n", target.Target)
		fmt.Fprintln(stdout, "next: run 'pluto events --since 1h' to see what the daemon did record")
		return 0
	}
	if target.TraceID != "" {
		fmt.Fprintf(stdout, "trace: %s\n", target.TraceID)
	}
	writeTimelineTable(stdout, list)
	return 0
}

// resolveTimelineTarget accepts a trace id, a box (id, prefix, or worktree),
// a task id, or a run id. Boxes win over tasks because a worktree path and a
// box id are the common manual case; ids are distinct UUIDs, so a task id
// never resolves as a box.
func resolveTimelineTarget(c *client.Client, raw string) (timelineTarget, error) {
	target := timelineTarget{Target: raw}
	if strings.HasPrefix(raw, "trc_") {
		target.TraceID = raw
		return target, nil
	}
	if box, err := resolveBox(c, raw); err == nil {
		target.BoxID = box.ID
		return target, nil
	}
	tasks, err := c.Tasks()
	if err != nil {
		return timelineTarget{}, err
	}
	var matches []timelineTarget
	for _, task := range tasks {
		if task.ID == raw || strings.HasPrefix(task.ID, raw) {
			matches = append(matches, timelineTarget{Target: raw, BoxID: task.BoxID, TaskID: task.ID, TraceID: task.TraceID})
		}
		for _, run := range task.Runs {
			if run.ID == raw || strings.HasPrefix(run.ID, raw) {
				traceID := run.TraceID
				if traceID == "" {
					traceID = task.TraceID
				}
				matches = append(matches, timelineTarget{Target: raw, BoxID: task.BoxID, TaskID: task.ID, RunID: run.ID, TraceID: traceID})
			}
		}
	}
	switch len(matches) {
	case 0:
		return timelineTarget{}, fmt.Errorf("%q matches no box, task, run, or trace", raw)
	case 1:
		return matches[0], nil
	default:
		return timelineTarget{}, fmt.Errorf("%q matches more than one task or run; pass the full id", raw)
	}
}

// readTimeline reads the chain in sequence order (oldest first). A task or run
// with no trace id falls back to its box and filters on the task identity, so
// history recorded before traces were added stays readable.
func readTimeline(c *client.Client, target timelineTarget) ([]events.Event, error) {
	q := client.EventQuery{Limit: timelineLimit}
	switch {
	case target.TraceID != "":
		q.TraceID = target.TraceID
	case target.BoxID != "":
		q.BoxID = target.BoxID
	default:
		return nil, nil
	}
	list, err := c.Events(q)
	if err != nil {
		return nil, err
	}
	// Newest first from the daemon; a chain reads oldest first.
	out := make([]events.Event, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		e := list[i]
		if target.TraceID == "" && target.TaskID != "" {
			if target.RunID != "" && e.Where.RunID != target.RunID {
				continue
			}
			if target.RunID == "" && e.Where.TaskID != target.TaskID {
				continue
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// writeTimelineTable renders one chain. The reason is folded into the summary
// so a wait, a failure, or a deduplication answers "why" on the same line.
func writeTimelineTable(w io.Writer, list []events.Event) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tLEVEL\tKIND\tSUMMARY\tWHERE")
	for _, e := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			e.At.Local().Format("15:04:05"), eventLevel(e), e.Kind, timelineSummary(e), eventWhere(e.Where))
	}
	tw.Flush()
}

func timelineSummary(e events.Event) string {
	summary := eventSummary(e)
	if e.Reason != "" {
		summary += " (" + e.Reason + ")"
	}
	return summary
}

// latestBoxEvent returns the newest ledger event for a box, or nil when the
// ledger is unavailable. Status treats it as additive evidence: a daemon
// without events still answers every existing column.
func latestBoxEvent(c *client.Client, boxID string) *events.Event {
	list, err := c.Events(client.EventQuery{BoxID: boxID, Limit: 1})
	if err != nil || len(list) == 0 {
		return nil
	}
	return &list[0]
}

// writeStatusEvent prints the latest decision and how to replay its trace. It
// is read-only: the caller has already fetched the box, and this never writes
// lifecycle state.
func writeStatusEvent(w io.Writer, e *events.Event, box *state.Box) {
	if e == nil {
		return
	}
	fmt.Fprintf(w, "last:     %s %s\n", e.Kind, timelineSummary(*e))
	trace := e.TraceID
	if trace == "" {
		trace = "-"
	}
	command := "pluto timeline " + state.ShortID(box.ID)
	if e.TraceID != "" {
		command = "pluto timeline " + e.TraceID
	}
	fmt.Fprintf(w, "trace:    %s  %s\n", trace, command)
}
