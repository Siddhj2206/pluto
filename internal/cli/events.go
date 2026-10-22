package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

const eventsUsage = "usage: pluto events [--box ID] [--kind PREFIX] [--trace ID] [--since WHEN] [--limit N] [--follow] [--json]"

// runEvents reads the host's durable activity ledger. Human output is
// chronological; the machine list keeps the daemon's newest-first order.
func runEvents(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "events", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	boxArg := fs.String("box", "", "only events for this box (id, prefix, or worktree)")
	kind := fs.String("kind", "", "only events whose kind starts with this prefix")
	trace := fs.String("trace", "", "only events carrying this trace id")
	since := fs.String("since", "", "events after a sequence, within a duration (1h), or after an RFC3339 time")
	limit := fs.Int("limit", 100, "maximum events to read before following")
	follow := fs.Bool("follow", false, "keep streaming new events")
	jsonOutput := fs.Bool("json", false, "print versioned JSON, or one object per line with --follow")
	if code := cliutil.ParseCommand(fs, args, stderr, eventsUsage); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "events takes no positional arguments", eventsUsage)
	}
	if *limit < 1 {
		return usageError(stderr, "--limit must be positive", eventsUsage)
	}
	if _, err := events.ParseSince(*since, time.Now()); err != nil {
		return usageError(stderr, err.Error(), eventsUsage)
	}

	c := client.New(socket)
	boxID := *boxArg
	if boxID != "" && !state.ValidID(boxID) {
		box, err := resolveBox(c, boxID)
		if err != nil {
			return fail(stderr, err, "pass an exact box id with '--box'")
		}
		boxID = box.ID
	}
	q := client.EventQuery{Since: *since, Limit: *limit, BoxID: boxID, Kind: *kind, TraceID: *trace}

	list, err := c.Events(q)
	if err != nil {
		return fail(stderr, err)
	}
	if !*follow {
		if *jsonOutput {
			return writeTaskJSON(stdout, api.EventListResponse{SchemaVersion: events.Schema, Events: list})
		}
		if len(list) == 0 {
			fmt.Fprintln(stdout, "no events")
			return 0
		}
		writeEventTable(stdout, reverseEvents(list))
		return 0
	}

	cursor := ""
	if len(list) > 0 {
		if *jsonOutput {
			for i := len(list) - 1; i >= 0; i-- {
				if err := json.NewEncoder(stdout).Encode(list[i]); err != nil {
					return fail(stderr, err)
				}
			}
		} else {
			writeEventTable(stdout, reverseEvents(list))
		}
		cursor = strconv.FormatUint(list[0].Seq, 10)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = c.StreamEvents(ctx, client.EventQuery{Since: cursor, BoxID: boxID, Kind: *kind, TraceID: *trace}, func(e events.Event) error {
		if *jsonOutput {
			return json.NewEncoder(stdout).Encode(e)
		}
		writeEventLine(stdout, e)
		return nil
	})
	if err != nil && ctx.Err() == nil {
		return fail(stderr, err)
	}
	return 0
}

func reverseEvents(list []events.Event) []events.Event {
	out := make([]events.Event, len(list))
	for i, e := range list {
		out[len(list)-1-i] = e
	}
	return out
}

// writeEventTable renders history oldest first so a trace reads top to bottom.
func writeEventTable(w io.Writer, list []events.Event) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tLEVEL\tKIND\tSUMMARY\tTRACE\tWHERE")
	for _, e := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.At.Local().Format("15:04:05"), eventLevel(e), e.Kind, eventSummary(e), eventTrace(e.TraceID), eventWhere(e.Where))
	}
	tw.Flush()
}

// writeEventLine renders one live event without buffering the stream.
func writeEventLine(w io.Writer, e events.Event) {
	fmt.Fprintf(w, "%s  %-5s  %-24s  %s  %s  %s\n",
		e.At.Local().Format("15:04:05"), eventLevel(e), e.Kind, eventSummary(e), eventTrace(e.TraceID), eventWhere(e.Where))
}

func eventLevel(e events.Event) string {
	if e.Level == "" {
		return events.LevelInfo
	}
	return e.Level
}

// eventSummary adds the recorded duration to the summary so a finish line
// answers "how long" without another column.
func eventSummary(e events.Event) string {
	if e.DurationMS <= 0 {
		return e.Summary
	}
	d := time.Duration(e.DurationMS) * time.Millisecond
	return fmt.Sprintf("%s in %s", e.Summary, d.Round(100*time.Millisecond))
}

// eventTrace shortens a trace id for the table; the full id stays in --json
// and in the --trace filter.
func eventTrace(id string) string {
	if id == "" {
		return "-"
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// eventWhere renders the flat location compactly: project/branch when known,
// otherwise the box, otherwise the host.
func eventWhere(w events.Where) string {
	switch {
	case w.Project != "" && w.Branch != "":
		return w.Project + "/" + w.Branch
	case w.Project != "":
		return w.Project
	case w.BoxID != "":
		return "box " + state.ShortID(w.BoxID)
	case w.Host != "":
		return w.Host
	}
	return "-"
}
