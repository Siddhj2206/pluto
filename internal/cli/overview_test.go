package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// overviewCLIFixture seeds one daemon with the aggregate state the overview
// and watch screens render: two boxes, a failed schedule, a blocked queue
// item, a rejected webhook, and a short event history with one normal line and
// three distinct failure lines.
func overviewCLIFixture(t *testing.T) (socket string, st *state.Store) {
	t.Helper()
	socket, st, srv := startDaemonFull(t, fakeRunner{})
	runningRepo := gitRepo(t)
	writeJobContract(t, runningRepo)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", runningRepo); code != 0 {
		t.Fatalf("up running box exit = %d, stderr: %s", code, errOut)
	}
	pausedRepo := gitRepo(t)
	writeJobContract(t, pausedRepo)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", pausedRepo); code != 0 {
		t.Fatalf("up paused box exit = %d, stderr: %s", code, errOut)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "pause", pausedRepo); code != 0 {
		t.Fatalf("pause exit = %d, stderr: %s", code, errOut)
	}

	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 2 {
		t.Fatalf("boxes = %v, %v", boxes, err)
	}
	var running, paused *state.Box
	for _, box := range boxes {
		if box.State == state.StateRunning {
			running = box
		} else {
			paused = box
		}
	}
	if running == nil || paused == nil {
		t.Fatalf("boxes = %+v, want one running and one paused", boxes)
	}

	now := time.Date(2026, 10, 22, 14, 31, 2, 0, time.UTC)
	srv.StartedAt = now.Add(-3*time.Hour - 12*time.Minute)
	srv.Now = func() time.Time { return now }
	srv.MaxRunningBoxes = 4

	if _, err := st.SetSchedules(running.ID, []state.Schedule{{
		Name: "nightly", Cron: "0 2 * * *", Job: "test",
	}}, now); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	if _, err := st.SetScheduleOutcome(running.ID, "nightly", state.ScheduleFailed, "run-nightly-1", "job-nightly-1", ""); err != nil {
		t.Fatalf("SetScheduleOutcome: %v", err)
	}
	if _, err := st.AdvanceSchedule(running.ID, "nightly", now.Add(-6*time.Minute)); err != nil {
		t.Fatalf("AdvanceSchedule: %v", err)
	}
	if err := st.SaveWebhookSource(state.WebhookSource{
		Source: "production", Kind: "github", BoxID: running.ID,
		SecretEnv: "PLUTO_GITHUB_WEBHOOK_SECRET", CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("SaveWebhookSource: %v", err)
	}
	if _, _, err := st.RecordWebhookOutcome("production", state.WebhookOutcomeRejected, "delivery-9", "issues", "invalid signature", now.Add(-time.Minute)); err != nil {
		t.Fatalf("RecordWebhookOutcome: %v", err)
	}
	blocked, err := st.Enqueue(state.QueueItem{
		ID: state.NewID(), Source: state.QueueEvent, BoxID: running.ID, Job: "test",
		State: state.QueuePending, EventSource: "github", EventID: "delivery-10",
	}, 100, now)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := st.UpdateQueueItem(blocked.ID, state.QueueBlocked, "", "contract revision is not approved", now); err != nil {
		t.Fatalf("UpdateQueueItem: %v", err)
	}
	rejected, err := st.Enqueue(state.QueueItem{
		ID: state.NewID(), Source: state.QueueEvent, BoxID: paused.ID, Job: "test",
		State: state.QueuePending, EventSource: "github", EventID: "delivery-11",
	}, 100, now)
	if err != nil {
		t.Fatalf("Enqueue rejected: %v", err)
	}
	if _, err := st.UpdateQueueItem(rejected.ID, state.QueueRejected, "", "queue is full", now); err != nil {
		t.Fatalf("UpdateQueueItem rejected: %v", err)
	}

	trace := "trc_0011223344556677"
	for _, e := range []events.Event{
		{Kind: events.KindJobDone, Summary: "job test done", TraceID: trace,
			Actor: events.Actor{Type: "cli", Client: "laptop"},
			Where: events.Where{BoxID: running.ID, Project: running.Project, Branch: running.Branch}},
		{Kind: events.KindJobFailed, Level: events.LevelError, Summary: "job test failed", Reason: "exit 3",
			TraceID: trace, Actor: events.Actor{Type: "cli", Client: "laptop"},
			Where: events.Where{BoxID: running.ID, Project: running.Project, Branch: running.Branch}},
		{Kind: events.KindWebhookRejected, Level: events.LevelError, Summary: "delivery rejected", Reason: "invalid signature",
			TraceID: "trc_ffeeddccbbaa9988", Actor: events.Actor{Type: "github", Client: "production"},
			Where: events.Where{EventSource: "production", DeliveryID: "delivery-9"}},
		{Kind: events.KindScheduleFailed, Level: events.LevelError, Summary: "schedule nightly failed", Reason: "exit 3",
			TraceID: trace, Actor: events.Actor{Type: "schedule"},
			Where: events.Where{BoxID: running.ID, Project: running.Project, Branch: running.Branch, Schedule: "nightly"}},
		{Kind: events.KindQueueBlocked, Level: events.LevelWarn, Summary: "queue item blocked", Reason: "contract revision is not approved",
			TraceID: "trc_aabbccddeeff0011", Actor: events.Actor{Type: "github", Client: "production"},
			Where: events.Where{BoxID: running.ID, Project: running.Project, Branch: running.Branch, QueueID: blocked.ID}},
	} {
		if _, err := srv.Events.Append(e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	return socket, st
}

func TestOverviewPrintsTheHostOnce(t *testing.T) {
	t.Setenv("PLUTO_WATCH_WIDTH", "120")
	socket, _ := overviewCLIFixture(t)
	code, out, errOut := runCLI(t, "--socket", socket, "overview")
	if code != 0 {
		t.Fatalf("overview exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{
		"PLUTO", "daemon test", "up 3h12m", "state ",
		"1/4 running", "0 pending", "1 blocked", "1 rejected", "load ",
		"BOXES", "SCHEDULES", "WEBHOOKS", "LIVE",
		"nightly", "0 2 * * *", "failed",
		"production", "/github/production", "invalid signature",
		"webhook.rejected", "job.failed", "schedule.failed", "queue.blocked",
		"trc_00", "laptop", "main",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("overview is missing %q:\n%s", want, out)
		}
	}
	// Failure lines are distinct in plain text: a leading '!' marker.
	for _, kind := range []string{"webhook.rejected", "job.failed", "schedule.failed", "queue.blocked"} {
		if !hasFlaggedLine(out, kind) {
			t.Fatalf("%s is not visibly distinct:\n%s", kind, out)
		}
	}
	// Non-TTY output never carries ANSI escapes.
	if strings.Contains(out, "\x1b") {
		t.Fatalf("overview emitted ANSI escapes:\n%q", out)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if len(line) > 120 {
			t.Fatalf("line is wider than the configured 120 columns (%d):\n%s", len(line), line)
		}
	}
}

func hasFlaggedLine(out, needle string) bool {
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if strings.Contains(line, needle) && (strings.HasPrefix(trimmed, "!") || strings.HasPrefix(trimmed, "~")) {
			return true
		}
	}
	return false
}

func TestOverviewJSONIsTheVersionedAggregate(t *testing.T) {
	socket, _ := overviewCLIFixture(t)
	code, out, errOut := runCLI(t, "--socket", socket, "overview", "--json")
	if code != 0 {
		t.Fatalf("overview --json exit = %d, stderr: %s", code, errOut)
	}
	var ov api.OverviewResponse
	if err := json.Unmarshal([]byte(out), &ov); err != nil {
		t.Fatalf("decode overview JSON: %v (%s)", err, out)
	}
	if ov.SchemaVersion != 1 || ov.Daemon.Version != "test" {
		t.Fatalf("overview JSON = %+v, want schema 1 and the daemon version", ov)
	}
	if ov.Boxes.Total != 2 || ov.Boxes.Running != 1 || ov.Boxes.Paused != 1 {
		t.Fatalf("boxes = %+v", ov.Boxes)
	}
	if len(ov.Schedules) != 1 || ov.Schedules[0].LastOutcome != state.ScheduleFailed {
		t.Fatalf("schedules = %+v", ov.Schedules)
	}
	if len(ov.Webhooks) != 1 || ov.Webhooks[0].Rejected != 1 {
		t.Fatalf("webhooks = %+v", ov.Webhooks)
	}
	if len(ov.RecentEvents) < 5 {
		t.Fatalf("recent events = %d, want at least the 5 seeded", len(ov.RecentEvents))
	}
}

func TestHealthPrintsDaemonFacts(t *testing.T) {
	socket, st := overviewCLIFixture(t)
	code, out, errOut := runCLI(t, "--socket", socket, "health")
	if code != 0 {
		t.Fatalf("health exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{
		"status:", "ok",
		"version:", "test",
		"state version:", "1",
		"uptime:", "3h12m",
		"socket:", socket,
		"state dir:", st.Root(),
		"boxes:", "2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("health is missing %q:\n%s", want, out)
		}
	}
}

func TestWatchOnceRendersOneFrameWithoutEscapes(t *testing.T) {
	t.Setenv("PLUTO_WATCH_WIDTH", "120")
	socket, _ := overviewCLIFixture(t)
	code, out, errOut := runCLI(t, "--socket", socket, "watch", "--once")
	if code != 0 {
		t.Fatalf("watch --once exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "LIVE") || !strings.Contains(out, "webhook.rejected") {
		t.Fatalf("watch frame is incomplete:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("non-TTY watch emitted ANSI escapes:\n%q", out)
	}
	// One frame, not a repeated render.
	if strings.Count(out, "PLUTO") != 1 {
		t.Fatalf("watch --once rendered more than one frame:\n%s", out)
	}
}

func TestWatchRejectsAnInvalidInterval(t *testing.T) {
	socket, _ := overviewCLIFixture(t)
	code, _, errOut := runCLI(t, "--socket", socket, "watch", "--once", "--interval", "0s")
	if code != 2 {
		t.Fatalf("watch --interval 0s exit = %d, want 2 (stderr %s)", code, errOut)
	}
	if !strings.Contains(errOut, "--interval") {
		t.Fatalf("stderr = %q, want an --interval usage hint", errOut)
	}
}

func TestOverviewAndHealthFailLegiblyWithoutADaemon(t *testing.T) {
	socket := t.TempDir() + "/missing.sock"
	for _, cmd := range [][]string{{"overview"}, {"watch", "--once"}, {"health"}} {
		code, _, errOut := runCLI(t, append([]string{"--socket", socket}, cmd...)...)
		if code == 0 {
			t.Fatalf("%v succeeded without a daemon", cmd)
		}
		if !strings.Contains(errOut, "daemon") {
			t.Fatalf("%v stderr = %q, want a daemon hint", cmd, errOut)
		}
	}
}
