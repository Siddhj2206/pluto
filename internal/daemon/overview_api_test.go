package daemon_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// overviewFixture is one daemon with a running box, a paused box, a schedule,
// a blocked and a rejected queue item, a running job, a persisted webhook
// source with one rejection, and a short event history.
type overviewFixture struct {
	socket string
	st     *state.Store
	now    time.Time
	box    boxJSON
	paused boxJSON
}

func newOverviewFixture(t *testing.T) overviewFixture {
	t.Helper()
	socket, st, srv := startServer(t, fakeRunner{})
	c := client(socket)

	worktree := writeContract(t, t.TempDir(), "[jobs.work]\ncommand = 'true'\n")
	box := createBoxAt(t, c, worktree)
	if resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/up", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("up status = %d, body %s", resp.StatusCode, data)
	}
	pausedWorktree := writeContract(t, t.TempDir(), "[jobs.work]\ncommand = 'true'\n")
	paused := createBoxAt(t, c, pausedWorktree)
	if resp, data := do(t, c, "POST", "/v1/boxes/"+paused.ID+"/up", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("paused box up status = %d, body %s", resp.StatusCode, data)
	}
	if resp, data := do(t, c, "POST", "/v1/boxes/"+paused.ID+"/pause", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d, body %s", resp.StatusCode, data)
	}

	now := time.Date(2026, 10, 22, 14, 31, 2, 0, time.UTC)
	srv.StartedAt = now.Add(-3*time.Hour - 12*time.Minute)
	srv.Now = func() time.Time { return now }

	armTwoSchedules(t, st, box.ID, now.Add(-24*time.Hour))
	if _, err := st.SetScheduleOutcome(box.ID, "nightly", state.ScheduleFailed, "run-failed", "job-failed", ""); err != nil {
		t.Fatalf("SetScheduleOutcome: %v", err)
	}

	if err := st.SaveWebhookSource(state.WebhookSource{
		Source:    "production",
		Kind:      "github",
		BoxID:     box.ID,
		SecretEnv: "PLUTO_GITHUB_WEBHOOK_SECRET",
		CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("SaveWebhookSource: %v", err)
	}
	if _, _, err := st.RecordWebhookOutcome("production", state.WebhookOutcomeRejected, "delivery-1", "issues", "invalid signature", now.Add(-time.Minute)); err != nil {
		t.Fatalf("RecordWebhookOutcome: %v", err)
	}

	blocked, err := st.Enqueue(state.QueueItem{
		ID: state.NewID(), Source: state.QueueEvent, BoxID: box.ID, Job: "work",
		State: state.QueuePending, EventSource: "github", EventID: "delivery-2",
	}, 100, now)
	if err != nil {
		t.Fatalf("Enqueue blocked: %v", err)
	}
	if _, err := st.UpdateQueueItem(blocked.ID, state.QueueBlocked, "", "contract revision is not approved", now); err != nil {
		t.Fatalf("UpdateQueueItem blocked: %v", err)
	}
	rejected, err := st.Enqueue(state.QueueItem{
		ID: state.NewID(), Source: state.QueueEvent, BoxID: paused.ID, Job: "work",
		State: state.QueuePending, EventSource: "github", EventID: "delivery-3",
	}, 100, now)
	if err != nil {
		t.Fatalf("Enqueue rejected: %v", err)
	}
	if _, err := st.UpdateQueueItem(rejected.ID, state.QueueRejected, "", "queue is full", now); err != nil {
		t.Fatalf("UpdateQueueItem rejected: %v", err)
	}
	if _, err := st.Enqueue(state.QueueItem{
		ID: state.NewID(), Source: state.QueueScheduled, BoxID: paused.ID, Job: "work",
		State: state.QueuePending, ScheduleName: "nightly",
	}, 100, now); err != nil {
		t.Fatalf("Enqueue pending: %v", err)
	}

	// A running job and a declared session usage on the running box.
	job := state.StartJobCommand(state.NewID(), "make test")
	job.TraceID = "trc_0011223344556677"
	job.JobOrigin = "schedule"
	job.ScheduleName = "nightly"
	if _, err := st.BeginJob(box.ID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	if _, err := st.SetResources(box.ID, &state.Resources{CPUs: 2, MemoryMiB: 4096, DiskMiB: 20480}); err != nil {
		t.Fatalf("SetResources: %v", err)
	}
	if _, err := st.SetPhases(box.ID, state.Phases{
		Synced:       true,
		SessionUsage: &state.SessionUsage{CPUUsec: 12_300_000, IOBytes: 1_200_000_000},
	}); err != nil {
		t.Fatalf("SetPhases: %v", err)
	}

	// History: a normal flow event and the three distinct failure kinds.
	for _, e := range []events.Event{
		{Kind: events.KindJobDone, Summary: "job test done", TraceID: "trc_0011223344556677",
			Actor: events.Actor{Type: "cli", Client: "laptop"}, Where: events.Where{BoxID: box.ID}},
		{Kind: events.KindJobFailed, Level: events.LevelError, Summary: "job test failed", Reason: "exit 3",
			TraceID: "trc_0011223344556677", Actor: events.Actor{Type: "cli", Client: "laptop"}, Where: events.Where{BoxID: box.ID}},
		{Kind: events.KindWebhookRejected, Level: events.LevelError, Summary: "delivery rejected", Reason: "invalid signature",
			TraceID: "trc_ffeeddccbbaa9988", Actor: events.Actor{Type: "github", Client: "production", ID: "delivery-1"},
			Where: events.Where{EventSource: "production", DeliveryID: "delivery-1"}},
		{Kind: events.KindScheduleFailed, Level: events.LevelError, Summary: "schedule nightly failed", Reason: "exit 3",
			TraceID: "trc_0011223344556677", Actor: events.Actor{Type: "schedule"}, Where: events.Where{BoxID: box.ID, Schedule: "nightly"}},
		{Kind: events.KindQueueBlocked, Level: events.LevelWarn, Summary: "queue item blocked", Reason: "contract revision is not approved",
			TraceID: "trc_aabbccddeeff0011", Actor: events.Actor{Type: "github", Client: "production"},
			Where: events.Where{BoxID: box.ID, QueueID: blocked.ID}},
	} {
		if _, err := srv.Events.Append(e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}

	return overviewFixture{socket: socket, st: st, now: now, box: box, paused: paused}
}

func TestOverviewAggregatesHostState(t *testing.T) {
	f := newOverviewFixture(t)
	resp, data := do(t, client(f.socket), "GET", "/v1/overview", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/overview status = %d, body %s", resp.StatusCode, data)
	}
	var ov api.OverviewResponse
	if err := json.Unmarshal(data, &ov); err != nil {
		t.Fatalf("decode overview: %v (%s)", err, data)
	}
	if ov.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", ov.SchemaVersion)
	}
	if ov.Daemon.Version != "test" || ov.Daemon.StateVersion != state.StateVersion {
		t.Fatalf("daemon = %+v, want version test and the state version", ov.Daemon)
	}
	if ov.Daemon.StateDir != f.st.Root() {
		t.Fatalf("state_dir = %q, want %q", ov.Daemon.StateDir, f.st.Root())
	}
	if ov.Daemon.Socket != f.socket {
		t.Fatalf("socket = %q, want %q", ov.Daemon.Socket, f.socket)
	}
	if ov.Daemon.UptimeMS != int64((3*time.Hour+12*time.Minute)/time.Millisecond) {
		t.Fatalf("uptime_ms = %d, want 3h12m", ov.Daemon.UptimeMS)
	}
	if ov.Boxes.Total != 2 || ov.Boxes.Running != 1 || ov.Boxes.Paused != 1 || ov.Boxes.MaxRunning != 4 {
		t.Fatalf("boxes = %+v, want 2 total, 1 running, 1 paused, max 4", ov.Boxes)
	}
	if ov.Queue.Pending != 1 || ov.Queue.Blocked != 1 || ov.Queue.Rejected != 1 {
		t.Fatalf("queue = %+v, want one pending, one blocked, one rejected", ov.Queue)
	}
	if len(ov.Queue.Problems) != 2 {
		t.Fatalf("queue problems = %+v, want the blocked and rejected items", ov.Queue.Problems)
	}
	if len(ov.ActiveJobs) != 1 || ov.ActiveJobs[0].JobID == "" || ov.ActiveJobs[0].Command != "make test" {
		t.Fatalf("active jobs = %+v, want the running make test job", ov.ActiveJobs)
	}
	if ov.ActiveJobs[0].TraceID != "trc_0011223344556677" || ov.ActiveJobs[0].Origin != "schedule" {
		t.Fatalf("active job = %+v, want the trace and origin", ov.ActiveJobs[0])
	}
	if len(ov.Schedules) != 2 {
		t.Fatalf("schedules = %+v, want both schedules", ov.Schedules)
	}
	var nightly *api.ScheduleInfo
	for i := range ov.Schedules {
		if ov.Schedules[i].Name == "nightly" {
			nightly = &ov.Schedules[i]
		}
	}
	if nightly == nil || nightly.NextDueAt == nil || nightly.LastOutcome != state.ScheduleFailed {
		t.Fatalf("nightly = %+v, want a next due and the failed outcome", nightly)
	}
	if len(ov.Webhooks) != 1 {
		t.Fatalf("webhooks = %+v, want the persisted source", ov.Webhooks)
	}
	hook := ov.Webhooks[0]
	if hook.Source != "production" || hook.Endpoint != "/github/production" || hook.Rejected != 1 ||
		hook.LastRejectionReason != "invalid signature" || hook.SecretPresent {
		t.Fatalf("webhook = %+v, want a redacted rejected source", hook)
	}
	if len(ov.RecentEvents) < 5 {
		t.Fatalf("recent events = %d, want at least the 5 appended", len(ov.RecentEvents))
	}
	// Newest first, matching GET /v1/events.
	if ov.RecentEvents[0].Kind != events.KindQueueBlocked {
		t.Fatalf("recent events[0] = %+v, want newest first", ov.RecentEvents[0])
	}
	seen := make(map[string]bool, len(ov.RecentEvents))
	for _, e := range ov.RecentEvents {
		seen[e.Kind] = true
	}
	for _, kind := range []string{
		events.KindJobDone, events.KindJobFailed, events.KindWebhookRejected,
		events.KindScheduleFailed, events.KindQueueBlocked,
	} {
		if !seen[kind] {
			t.Fatalf("recent events are missing %s", kind)
		}
	}

	// A box row carries its declared size, and usage only while running.
	var runningRow, pausedRow *api.BoxSummary
	for i := range ov.BoxSummaries {
		switch ov.BoxSummaries[i].ID {
		case f.box.ID:
			runningRow = &ov.BoxSummaries[i]
		case f.paused.ID:
			pausedRow = &ov.BoxSummaries[i]
		}
	}
	if runningRow == nil || pausedRow == nil {
		t.Fatalf("box summaries = %+v, want both boxes", ov.BoxSummaries)
	}
	if runningRow.Resources == nil || runningRow.Resources.CPUs != 2 || runningRow.Resources.MemoryMiB != 4096 {
		t.Fatalf("running resources = %+v, want the declared size", runningRow.Resources)
	}
	if runningRow.Usage == nil || runningRow.Usage.CPUUsec != 12_300_000 || runningRow.Usage.IOBytes != 1_200_000_000 {
		t.Fatalf("running usage = %+v, want the reported session work", runningRow.Usage)
	}
	if pausedRow.Usage != nil {
		t.Fatalf("paused usage = %+v, want unknown (nil)", pausedRow.Usage)
	}
	if pausedRow.LastJob != nil {
		t.Fatalf("paused last job = %+v, want none", pausedRow.LastJob)
	}
}

func TestOverviewHonorsEventLimit(t *testing.T) {
	f := newOverviewFixture(t)
	resp, data := do(t, client(f.socket), "GET", "/v1/overview?events=2", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, data)
	}
	var ov api.OverviewResponse
	if err := json.Unmarshal(data, &ov); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(ov.RecentEvents) != 2 {
		t.Fatalf("recent events = %d, want 2", len(ov.RecentEvents))
	}
	resp, data = do(t, client(f.socket), "GET", "/v1/overview?events=0", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("events=0 status = %d, want 400 (%s)", resp.StatusCode, data)
	}
}

func TestHealthReportsUptimeSocketAndStateDir(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	now := time.Date(2026, 10, 22, 14, 31, 2, 0, time.UTC)
	srv.StartedAt = now.Add(-90 * time.Minute)
	srv.Now = func() time.Time { return now }

	resp, data := do(t, client(socket), "GET", "/v1/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, data)
	}
	var h api.Health
	if err := json.Unmarshal(data, &h); err != nil {
		t.Fatalf("decode health: %v (%s)", err, data)
	}
	if h.Status != "ok" || h.Version != "test" || h.StateVersion != state.StateVersion {
		t.Fatalf("health = %+v, want the daemon identity", h)
	}
	if h.StateDir != st.Root() || h.Socket != socket {
		t.Fatalf("health paths = %q, %q, want %q, %q", h.StateDir, h.Socket, st.Root(), socket)
	}
	if h.UptimeMS != int64(90*time.Minute/time.Millisecond) {
		t.Fatalf("uptime_ms = %d, want 90m", h.UptimeMS)
	}
	if h.Host == "" {
		t.Fatalf("health host is empty, want the daemon host")
	}
	if h.Boxes != 0 {
		t.Fatalf("boxes = %d, want 0", h.Boxes)
	}
}

func TestOverviewSortsBoxesAndTruncatesNothingInJSON(t *testing.T) {
	socket, st, _ := startServer(t, fakeRunner{})
	c := client(socket)
	first := writeContract(t, t.TempDir(), "")
	b1 := createBoxAt(t, c, first)
	second := writeContract(t, t.TempDir(), "")
	b2 := createBoxAt(t, c, second)
	// The order is stable (project, branch, id) regardless of creation order.
	if b1.ID == "" || b2.ID == "" || st == nil {
		t.Fatal("fixture is incomplete")
	}
	resp, data := do(t, c, "GET", "/v1/overview", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, data)
	}
	var ov api.OverviewResponse
	if err := json.Unmarshal(data, &ov); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(ov.BoxSummaries) != 2 {
		t.Fatalf("box summaries = %+v, want both", ov.BoxSummaries)
	}
	for i := 1; i < len(ov.BoxSummaries); i++ {
		prev, cur := ov.BoxSummaries[i-1], ov.BoxSummaries[i]
		key := func(b api.BoxSummary) string { return b.Project + "\x00" + b.Branch + "\x00" + b.ID }
		if key(cur) < key(prev) {
			t.Fatalf("box summaries are not sorted: %q then %q", key(prev), key(cur))
		}
	}
	if ov.Daemon.StateDir != st.Root() {
		t.Fatalf("state_dir = %q, want %q", ov.Daemon.StateDir, st.Root())
	}
	if ov.Daemon.Host == "" {
		t.Fatal("daemon host is empty")
	}
	if ov.Boxes.MaxRunning != 4 {
		t.Fatalf("max running = %d, want 4", ov.Boxes.MaxRunning)
	}
	if filepath.Base(ov.BoxSummaries[0].Worktree) == "" {
		t.Fatal("box summary lost its worktree")
	}
}
