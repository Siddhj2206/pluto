package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/cliutil"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// This file is the host's live operations view: `pluto overview` prints the
// aggregate state once, and `pluto watch` refreshes the same frame in place.
// Both read one daemon aggregate (GET /v1/overview); neither adds a store, a
// listener, or a browser.

const (
	overviewUsage = "usage: pluto overview [--json]"
	watchUsage    = "usage: pluto watch [--interval 2s] [--once]"

	// defaultViewWidth keeps the frame readable in a typical terminal
	// screenshot. PLUTO_WATCH_WIDTH overrides it; the view never wraps.
	defaultViewWidth = 120
	minViewWidth     = 100
	maxViewWidth     = 240
	// overviewEventTail is the default number of events in the live pane.
	overviewEventTail = 20
	// watchClear is the cursor-home plus clear-screen sequence. It is written
	// only when stdout is a terminal.
	watchClear = "\x1b[H\x1b[2J"
)

// viewOptions control one frame: the clock used for relative times, the fixed
// column width, and whether ANSI color is safe on this terminal.
type viewOptions struct {
	Now   time.Time
	Width int
	Color bool
	// Note is a one-line warning shown under the header (for example, a
	// daemon that went away between refreshes).
	Note string
}

// runOverview prints the host's aggregate state once.
func runOverview(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "overview", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("overview", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print the versioned aggregate as JSON")
	if code := cliutil.ParseCommand(fs, args, stderr, overviewUsage); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "overview takes no arguments", overviewUsage)
	}
	ov, err := client.New(socket).Overview(overviewEventTail)
	if err != nil {
		return fail(stderr, err)
	}
	if *jsonOutput {
		return writeTaskJSON(stdout, ov)
	}
	writeOverview(stdout, ov, viewOptions{
		Now:   frameNow(ov),
		Width: viewWidth(),
		Color: colorEnabled(stdout),
	})
	return 0
}

// runWatch refreshes the overview in place until interrupted. It is
// deliberately non-interactive: no keys, no alternate screen, no dashboard.
func runWatch(args []string, socket string, stdout, stderr io.Writer) int {
	if cliutil.MaybeHelp(args, "watch", commandHelp, stdout) {
		return 0
	}
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	interval := fs.Duration("interval", 2*time.Second, "how often to refresh the view")
	once := fs.Bool("once", false, "print one frame and exit (for screenshots and scripts)")
	if code := cliutil.ParseCommand(fs, args, stderr, watchUsage); code != 0 {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "watch takes no arguments", watchUsage)
	}
	if *interval <= 0 {
		return usageError(stderr, "--interval must be a positive duration", watchUsage)
	}

	c := client.New(socket)
	ov, err := c.Overview(overviewEventTail)
	if err != nil {
		return fail(stderr, err)
	}
	tty := isTerminal(stdout)
	color := tty && colorEnabled(stdout)
	width := viewWidth()
	render := func(ov *api.OverviewResponse, note string) {
		if tty {
			fmt.Fprint(stdout, watchClear)
		}
		writeOverview(stdout, ov, viewOptions{Now: frameNow(ov), Width: width, Color: color, Note: note})
	}
	render(ov, "")
	if *once {
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
		next, err := c.Overview(overviewEventTail)
		if err != nil {
			// A daemon restart must not end the view; show the last frame and
			// say why it is stale until the socket answers again.
			render(ov, "daemon unreachable: "+err.Error())
			continue
		}
		ov = next
		render(ov, "")
	}
}

// frameNow is the clock the frame's relative times use. The daemon's clock is
// authoritative: a client over SSH may be minutes off.
func frameNow(ov *api.OverviewResponse) time.Time {
	if ov != nil && !ov.Daemon.Now.IsZero() {
		return ov.Daemon.Now
	}
	return time.Now()
}

// viewWidth resolves the fixed frame width. A too-narrow frame would truncate
// every column, so the override is clamped.
func viewWidth() int {
	width := defaultViewWidth
	if raw := os.Getenv("PLUTO_WATCH_WIDTH"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			width = n
		}
	}
	if width < minViewWidth {
		return minViewWidth
	}
	if width > maxViewWidth {
		return maxViewWidth
	}
	return width
}

// isTerminal reports whether w is a character device. A piped or captured
// writer is never treated as a terminal, so tests and files get plain text.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// colorEnabled honours NO_COLOR (presence disables color) and a dumb TERM.
func colorEnabled(w io.Writer) bool {
	if !isTerminal(w) {
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return os.Getenv("TERM") != "dumb"
}

// palette colors only the marker and kind, never the whole line. When color is
// disabled every method returns its input unchanged, so the plain-text frame
// is the same bytes a screenshot tool sees.
type palette struct{ enabled bool }

func (p palette) error(s string) string { return p.paint("31", s) }
func (p palette) warn(s string) string  { return p.paint("33", s) }

func (p palette) paint(code, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// writeOverview renders one complete frame. Every line is built from fixed
// column widths and truncated to the frame width, so a long branch name or
// reason cannot wrap the screen.
func writeOverview(w io.Writer, ov *api.OverviewResponse, opts viewOptions) {
	if opts.Width <= 0 {
		opts.Width = defaultViewWidth
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	paint := palette{enabled: opts.Color}

	writeHeader(w, ov, opts)
	if opts.Note != "" {
		fmt.Fprintf(w, "warning  %s\n", truncate(opts.Note, opts.Width-9))
	}

	fmt.Fprintln(w)
	writeBoxSection(w, ov, opts)
	if len(ov.Queue.Problems) > 0 {
		fmt.Fprintln(w)
		writeQueueSection(w, ov, opts)
	}
	if len(ov.ActiveJobs) > 0 {
		fmt.Fprintln(w)
		writeJobSection(w, ov, opts)
	}
	fmt.Fprintln(w)
	writeScheduleSection(w, ov, opts)
	fmt.Fprintln(w)
	writeWebhookSection(w, ov, opts)
	fmt.Fprintln(w)
	writeLiveSection(w, ov, opts, paint)
}

// writeHeader is the top lines: identity, capacity, and load. Each line is
// truncated to the frame width so a long hostname or state path cannot wrap.
func writeHeader(w io.Writer, ov *api.OverviewResponse, opts viewOptions) {
	host := ov.Daemon.Host
	if host == "" {
		host = "-"
	}
	load := fmt.Sprintf("load %.2f %.2f %.2f", ov.Host.Load1, ov.Host.Load5, ov.Host.Load15)
	mem := "memory unknown"
	if ov.Host.MemoryTotalMiB > 0 {
		mem = fmt.Sprintf("memory %.1f/%.1f GiB",
			float64(ov.Host.MemoryTotalMiB-ov.Host.MemoryAvailableMiB)/1024,
			float64(ov.Host.MemoryTotalMiB)/1024)
	}
	for _, line := range []string{
		fmt.Sprintf("PLUTO  %s  daemon %s  up %s", host, ov.Daemon.Version, uptimeLabel(ov.Daemon.UptimeMS)),
		fmt.Sprintf("state  %s", compactHome(ov.Daemon.StateDir)),
		fmt.Sprintf("boxes  %d/%d running  %d paused  %d failed",
			ov.Boxes.Running, ov.Boxes.MaxRunning, ov.Boxes.Paused, ov.Boxes.Failed),
		fmt.Sprintf("queue  %d pending  %d running  %d blocked  %d rejected   jobs %d active",
			ov.Queue.Pending, ov.Queue.Running, ov.Queue.Blocked, ov.Queue.Rejected, len(ov.ActiveJobs)),
		fmt.Sprintf("host   %s  %d cpus  %s", load, ov.Host.CPUs, mem),
	} {
		fmt.Fprintln(w, truncate(line, opts.Width))
	}
}

// writeBoxSection is one stable row per box: identity, state, phase, declared
// size, observed use (or unknown), and the latest job.
func writeBoxSection(w io.Writer, ov *api.OverviewResponse, opts viewOptions) {
	fmt.Fprintln(w, "BOXES")
	fmt.Fprintf(w, "%-8s  %-22s  %-8s  %-14s  %-14s  %-16s  %s\n",
		"ID", "PROJECT/BRANCH", "STATE", "PHASE", "RESOURCES", "SESSION USE", "LAST JOB")
	if len(ov.BoxSummaries) == 0 {
		fmt.Fprintln(w, "no boxes")
		return
	}
	for _, box := range ov.BoxSummaries {
		fmt.Fprintln(w, truncate(fmt.Sprintf("%-8s  %-22s  %-8s  %-14s  %-14s  %-16s  %s",
			state.ShortID(box.ID),
			truncate(box.Project+"/"+box.Branch, 22),
			box.State,
			truncate(orDash(box.Phase), 14),
			truncate(resourceCell(box.Resources), 14),
			truncate(usageCell(box), 16),
			truncate(lastJobCell(box.LastJob, opts.Now), opts.Width-94)), opts.Width))
	}
}

// writeQueueSection shows blocked and rejected items with their reason.
func writeQueueSection(w io.Writer, ov *api.OverviewResponse, opts viewOptions) {
	fmt.Fprintln(w, "QUEUE")
	for _, item := range ov.Queue.Problems {
		fmt.Fprintf(w, "%s %-9s  %-8s  %-22s  %-10s  %s\n",
			"!", item.State, state.ShortID(item.ID),
			truncate(boxWhere(ov, item.BoxID), 22),
			truncate(orDash(item.Job), 10),
			truncate(orDash(item.Reason), opts.Width-59))
	}
}

// writeJobSection lists in-flight jobs, so a long run is visible rather than
// inferred from the header count.
func writeJobSection(w io.Writer, ov *api.OverviewResponse, opts viewOptions) {
	fmt.Fprintln(w, "JOBS")
	fmt.Fprintf(w, "%-8s  %-8s  %-10s  %-8s  %-8s  %s\n", "BOX", "JOB", "ORIGIN", "TRACE", "STARTED", "COMMAND")
	for _, job := range ov.ActiveJobs {
		fmt.Fprintf(w, "%-8s  %-8s  %-10s  %-8s  %-8s  %s\n",
			state.ShortID(job.BoxID), state.ShortID(job.JobID), orDash(job.Origin),
			shortTrace(job.TraceID), job.StartedAt.Local().Format("15:04:05"),
			truncate(job.Command, opts.Width-52))
	}
}

// writeScheduleSection is every box schedule with its next due and last
// outcome. A failed, blocked, rejected, or skipped outcome carries a leading
// '!' so it is distinct without color.
func writeScheduleSection(w io.Writer, ov *api.OverviewResponse, opts viewOptions) {
	fmt.Fprintln(w, "SCHEDULES")
	fmt.Fprintf(w, "%-8s  %-12s  %-12s  %-26s  %s\n", "BOX", "NAME", "CRON", "NEXT", "LAST")
	if len(ov.Schedules) == 0 {
		fmt.Fprintln(w, "no schedules")
		return
	}
	for _, sched := range ov.Schedules {
		fmt.Fprintf(w, "%-8s  %-12s  %-12s  %-26s  %s\n",
			state.ShortID(sched.BoxID), truncate(sched.Name, 12), truncate(sched.Cron, 12),
			truncate(scheduleNextCell(sched.NextDueAt, opts.Now), 26),
			truncate(scheduleLastCell(sched, opts.Now), opts.Width-66))
	}
}

// writeWebhookSection is the redacted source list. A rejection is visible as
// a count, a '!' marker, and the last reason.
func writeWebhookSection(w io.Writer, ov *api.OverviewResponse, opts viewOptions) {
	fmt.Fprintln(w, "WEBHOOKS")
	fmt.Fprintf(w, "%-12s  %-7s  %-24s  %-22s  %-8s  %-8s  %s\n",
		"SOURCE", "KIND", "ENDPOINT", "LAST DELIVERY", "ACCEPTED", "REJECTED", "LAST REJECTION")
	if len(ov.Webhooks) == 0 {
		fmt.Fprintln(w, "no webhook sources")
		return
	}
	for _, hook := range ov.Webhooks {
		marker := ""
		if hook.Rejected > 0 {
			marker = "! "
		}
		fmt.Fprintf(w, "%-12s  %-7s  %-24s  %-22s  %-8d  %-8s  %s\n",
			truncate(hook.Source, 12), truncate(hook.Kind, 7), truncate(hook.Endpoint, 24),
			truncate(webhookDeliveryCell(hook, opts.Now), 22),
			hook.Accepted, marker+strconv.Itoa(hook.Rejected),
			truncate(orDash(hook.LastRejectionReason), opts.Width-93))
	}
}

// writeLiveSection is the chronological event tail. Each line answers what,
// where, when, and why: time, source, kind, actor, box/project/branch, trace,
// and the recorded reason (or the summary when there is no reason).
func writeLiveSection(w io.Writer, ov *api.OverviewResponse, opts viewOptions, paint palette) {
	fmt.Fprintln(w, "LIVE")
	format := "%-1s %-8s  %-8s  %-20s  %-14s  %-18s  %-8s  %s\n"
	fmt.Fprintf(w, format, "", "TIME", "SOURCE", "KIND", "ACTOR", "WHERE", "TRACE", "REASON")
	if len(ov.RecentEvents) == 0 {
		fmt.Fprintln(w, "no events yet")
		return
	}
	// RecentEvents is newest first; the tail reads oldest to newest.
	for i := len(ov.RecentEvents) - 1; i >= 0; i-- {
		e := ov.RecentEvents[i]
		flag := " "
		switch e.Level {
		case events.LevelError:
			flag = "!"
		case events.LevelWarn:
			flag = "~"
		}
		line := fmt.Sprintf(format,
			flag,
			e.At.Local().Format("15:04:05"),
			truncate(eventSource(e.Kind), 8),
			truncate(e.Kind, 20),
			truncate(eventActor(e.Actor), 14),
			truncate(eventWhereShort(e.Where), 18),
			shortTrace(e.TraceID),
			truncate(eventWhy(e), opts.Width-90))
		// Color the whole row after padding so ANSI bytes never shift a
		// column. With color disabled this is exactly the plain line.
		switch e.Level {
		case events.LevelError:
			line = paint.error(line)
		case events.LevelWarn:
			line = paint.warn(line)
		}
		fmt.Fprintln(w, line)
	}
}

// eventSource is the kind's group: daemon, box, queue, job, schedule, ...
func eventSource(kind string) string {
	if i := strings.IndexByte(kind, '.'); i > 0 {
		return kind[:i]
	}
	return kind
}

// eventActor names who caused the event without hiding the class.
func eventActor(a events.Actor) string {
	switch {
	case a.Type != "" && a.Client != "":
		return a.Type + ":" + a.Client
	case a.Client != "":
		return a.Client
	case a.Type != "" && a.ID != "":
		return a.Type + ":" + state.ShortID(a.ID)
	case a.Type != "":
		return a.Type
	}
	return "-"
}

// eventWhereShort renders the flat location compactly: project/branch when
// known, else the box, else the event source or host.
func eventWhereShort(w events.Where) string {
	switch {
	case w.Project != "" && w.Branch != "":
		return w.Project + "/" + w.Branch
	case w.Project != "":
		return w.Project
	case w.BoxID != "":
		return "box " + state.ShortID(w.BoxID)
	case w.Schedule != "":
		return "schedule " + w.Schedule
	case w.EventSource != "":
		return w.EventSource
	case w.Host != "":
		return w.Host
	}
	return "-"
}

// eventWhy is the "why" on one line: the recorded reason, or the summary when
// the event is normal flow and has no reason.
func eventWhy(e events.Event) string {
	if e.Reason != "" {
		return e.Reason
	}
	return orDash(e.Summary)
}

// resourceCell is a box's declared size. Zero values mean the record predates
// resources, so the cell says unknown rather than inventing defaults.
func resourceCell(res *state.Resources) string {
	if res == nil || (res.CPUs == 0 && res.MemoryMiB == 0 && res.DiskMiB == 0) {
		return "unknown"
	}
	cpus := "-"
	if res.CPUs > 0 {
		cpus = strconv.Itoa(res.CPUs) + "cpu"
	}
	return fmt.Sprintf("%s/%s/%s", cpus, mibLabel(res.MemoryMiB), mibLabel(res.DiskMiB))
}

// usageCell reports observed session work for a running box and "unknown" for
// anything else. It never guesses from a stale counter.
func usageCell(box api.BoxSummary) string {
	if box.State != string(state.StateRunning) {
		return "unknown (" + box.State + ")"
	}
	if box.Usage == nil {
		return "unknown"
	}
	return fmt.Sprintf("cpu %s io %s",
		secondsLabel(box.Usage.CPUUsec), bytesLabel(box.Usage.IOBytes))
}

// lastJobCell is "test done 7.1s" or "-".
func lastJobCell(job *api.JobSummary, now time.Time) string {
	if job == nil {
		return "-"
	}
	cell := job.Command + " " + job.State
	if job.State == string(state.JobFailed) {
		cell = "! " + cell
	}
	if job.DurationMS > 0 {
		cell += " " + shortDuration(time.Duration(job.DurationMS)*time.Millisecond)
	}
	return cell
}

// scheduleNextCell is "10-23 02:00 in 11h28m" or an overdue slot.
func scheduleNextCell(due *time.Time, now time.Time) string {
	if due == nil {
		return "-"
	}
	at := due.Local()
	if !due.After(now) {
		return at.Format("01-02 15:04") + " overdue"
	}
	return at.Format("01-02 15:04") + " in " + shortDuration(due.Sub(now))
}

// scheduleLastCell is "done 6m ago (test)" or a marked failure.
func scheduleLastCell(sched api.ScheduleInfo, now time.Time) string {
	if sched.LastOutcome == "" {
		if sched.NextDueAt != nil {
			return "not run yet"
		}
		return "-"
	}
	cell := sched.LastOutcome
	switch sched.LastOutcome {
	case state.ScheduleFailed, state.ScheduleBlocked, state.ScheduleRejected, state.ScheduleSkipped:
		cell = "! " + cell
	}
	switch {
	case sched.LastFired != nil:
		cell += " " + shortDuration(now.Sub(*sched.LastFired)) + " ago"
	case sched.LastRunID != "":
		cell += " manual"
	}
	if sched.Job != "" {
		cell += " (" + sched.Job + ")"
	} else {
		cell += " (warm)"
	}
	return cell
}

// webhookDeliveryCell is "8s ago (push)" or "never".
func webhookDeliveryCell(hook api.WebhookSourceStatus, now time.Time) string {
	if hook.LastDeliveryAt == nil {
		if !hook.SecretPresent {
			return "never (no secret)"
		}
		return "never"
	}
	cell := shortDuration(now.Sub(*hook.LastDeliveryAt)) + " ago"
	if hook.LastEvent != "" {
		cell += " (" + hook.LastEvent + ")"
	}
	return cell
}

// boxWhere resolves a queue item's box to project/branch for the queue table.
func boxWhere(ov *api.OverviewResponse, boxID string) string {
	for _, box := range ov.BoxSummaries {
		if box.ID == boxID {
			return box.Project + "/" + box.Branch
		}
	}
	return "box " + state.ShortID(boxID)
}

// uptimeLabel renders a duration from milliseconds as "3h12m", "2d4h", "8m".
func uptimeLabel(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	return shortDuration(time.Duration(ms) * time.Millisecond)
}

// shortDuration is a compact, fixed-feeling duration for the frame: "8s",
// "12m", "3h12m", "2d4h". It never prints a trailing zero unit.
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		hours := int(d / time.Hour)
		minutes := int(d%time.Hour) / int(time.Minute)
		if minutes == 0 {
			return strconv.Itoa(hours) + "h"
		}
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		days := int(d / (24 * time.Hour))
		hours := int(d%(24*time.Hour)) / int(time.Hour)
		if hours == 0 {
			return strconv.Itoa(days) + "d"
		}
		return fmt.Sprintf("%dd%dh", days, hours)
	}
}

// mibLabel renders a declared size in MiB as GiB when it is a clean multiple.
func mibLabel(mib int) string {
	if mib <= 0 {
		return "-"
	}
	if mib%1024 == 0 {
		return strconv.Itoa(mib/1024) + "G"
	}
	return strconv.Itoa(mib) + "M"
}

// secondsLabel renders cumulative CPU microseconds as "12.3s".
func secondsLabel(usec int64) string {
	return fmt.Sprintf("%.1fs", float64(usec)/1e6)
}

// bytesLabel renders cumulative IO bytes in binary units.
func bytesLabel(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + "B"
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(bytes)/float64(div), "KMGTP"[exp])
}

// compactHome shortens the user's home directory to "~" for a screenshot.
func compactHome(path string) string {
	if path == "" {
		return "unknown"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// shortTrace is the 8-character trace label used in dense tables.
func shortTrace(trace string) string {
	if trace == "" {
		return "-"
	}
	if len(trace) > 8 {
		return trace[:8]
	}
	return trace
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncate cuts s to width runes, ending with "..." when it had to. It never
// returns a line longer than width, so a long branch or reason cannot wrap.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}
