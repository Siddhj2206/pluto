package daemon_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// armTwoSchedules approves the box's contract and stores both a job schedule
// and a warm-up, so the listing can be read against one fixed clock.
func armTwoSchedules(t *testing.T, st *state.Store, boxID string, armed time.Time) {
	t.Helper()
	box, err := st.Box(boxID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		t.Fatalf("Load contract: %v", err)
	}
	if _, err := st.ApproveContract(box.Project, ct.Hash(), armed); err != nil {
		t.Fatalf("ApproveContract: %v", err)
	}
	if _, err := st.SetSchedules(boxID, []state.Schedule{
		{Name: "nightly", Cron: "0 2 * * *", Job: "work"},
		{Name: "warm", Cron: "0 3 * * *"},
	}, armed); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
}

func TestScheduleListReportsNextDueAndLastOutcome(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.work]\ncommand = 'true'\n")
	box := createBoxAt(t, c, worktree)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armTwoSchedules(t, st, box.ID, armed)
	if _, err := st.SetScheduleOutcome(box.ID, "nightly", state.ScheduleDone, "run1234", "job1234"); err != nil {
		t.Fatalf("SetScheduleOutcome: %v", err)
	}
	srv.Now = func() time.Time { return armed.Add(time.Hour) }

	resp, data := do(t, c, "GET", "/v1/schedules", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/schedules status = %d, body %s", resp.StatusCode, data)
	}
	var list api.ScheduleListResponse
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("decode schedules: %v (%s)", err, data)
	}
	if list.SchemaVersion != 1 || len(list.Schedules) != 2 {
		t.Fatalf("list = %+v, want schema 1 with both schedules", list)
	}
	nightly, warm := list.Schedules[0], list.Schedules[1]
	if nightly.Name != "nightly" || nightly.BoxID != box.ID || nightly.Project != filepath.Base(worktree) || nightly.Branch != "main" {
		t.Fatalf("nightly = %+v, want the box's identity", nightly)
	}
	if nightly.Cron != "0 2 * * *" || nightly.Job != "work" {
		t.Fatalf("nightly definition = %+v", nightly)
	}
	wantDue := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	if nightly.NextDueAt == nil || !nightly.NextDueAt.Equal(wantDue) {
		t.Fatalf("nightly next_due_at = %v, want %v", nightly.NextDueAt, wantDue)
	}
	if nightly.LastOutcome != state.ScheduleDone || nightly.LastRunID != "run1234" || nightly.LastJobID != "job1234" {
		t.Fatalf("nightly outcome = %+v, want the recorded run and job", nightly)
	}
	if warm.Job != "" || warm.NextDueAt == nil || !warm.NextDueAt.Equal(time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("warm = %+v, want a visible warm-up with its next due", warm)
	}

	// A box filter narrows the read for `pluto schedule ls <box>`.
	resp, data = do(t, c, "GET", "/v1/schedules?box="+box.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filtered schedules status = %d, body %s", resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, &list); err != nil || len(list.Schedules) != 2 {
		t.Fatalf("filtered list = %+v, err %v", list, err)
	}
	resp, data = do(t, c, "GET", "/v1/schedules?box="+state.NewID(), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown box status = %d, want 404 (body %s)", resp.StatusCode, data)
	}
}

func TestScheduleRunMaterializesOneExplicitOccurrence(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.work]\ncommand = 'true'\n")
	box := createBoxAt(t, c, worktree)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "nightly", Cron: "0 2 * * *", Job: "work"})
	srv.Now = func() time.Time { return armed.Add(30 * time.Second) }

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/schedules/nightly/run", api.ScheduleRunRequest{IdempotencyKey: "one"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, body %s", resp.StatusCode, data)
	}
	var first api.ScheduleRunResponse
	if err := json.Unmarshal(data, &first); err != nil {
		t.Fatalf("decode run: %v (%s)", err, data)
	}
	if first.Schedule != "nightly" || first.BoxID != box.ID || !strings.HasPrefix(first.Occurrence, "manual:") {
		t.Fatalf("run response = %+v, want one manual occurrence", first)
	}
	if first.Run.State != state.TaskRunQueued || first.Run.Job != "work" || first.TraceID == "" {
		t.Fatalf("run = %+v, want queued declared work with a trace", first.Run)
	}

	items, err := st.Queue()
	if err != nil || len(items) != 1 {
		t.Fatalf("queue = %+v, err %v, want one explicit occurrence", items, err)
	}
	if items[0].Source != state.QueueManual || items[0].ScheduleName != "nightly" || items[0].RunID != first.Run.ID {
		t.Fatalf("queue item = %+v, want a manual schedule occurrence", items[0])
	}
	// The cron clock is untouched: a manual occurrence is not a cron slot.
	got, err := st.Box(box.ID)
	if err != nil || got.Schedules[0].LastFired != nil {
		t.Fatalf("last_fired = %+v (err %v), want the cron clock unmoved", got.Schedules, err)
	}

	// The occurrence leaves a durable fired event carrying the schedule name
	// and its manual source, on the same trace as the run.
	fired := false
	for _, e := range listEvents(t, c, "?kind=schedule.&trace="+first.TraceID) {
		if e.Kind == events.KindScheduleFired && e.Where.Schedule == "nightly" && e.Attrs["source"] == "manual" && e.Reason != "" {
			fired = true
		}
	}
	if !fired {
		t.Fatalf("trace %s has no manual schedule.fired event", first.TraceID)
	}

	// The same idempotency key is the same occurrence, not a second run.
	resp, data = do(t, c, "POST", "/v1/boxes/"+box.ID+"/schedules/nightly/run", api.ScheduleRunRequest{IdempotencyKey: "one"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repeated run status = %d, want 200 (body %s)", resp.StatusCode, data)
	}
	var replay api.ScheduleRunResponse
	if err := json.Unmarshal(data, &replay); err != nil {
		t.Fatalf("decode replay: %v (%s)", err, data)
	}
	if replay.Run.ID != first.Run.ID || replay.Occurrence != first.Occurrence {
		t.Fatalf("replay = %+v, want the original run %s and occurrence %s", replay, first.Run.ID, first.Occurrence)
	}
	if items, err := st.Queue(); err != nil || len(items) != 1 {
		t.Fatalf("queue after replay = %+v, err %v, want no second occurrence", items, err)
	}

	// A new key is a new explicit occurrence.
	resp, data = do(t, c, "POST", "/v1/boxes/"+box.ID+"/schedules/nightly/run", api.ScheduleRunRequest{IdempotencyKey: "two"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("second run status = %d, body %s", resp.StatusCode, data)
	}
	if items, err := st.Queue(); err != nil || len(items) != 2 {
		t.Fatalf("queue after a new key = %+v, err %v, want two occurrences", items, err)
	}

	resp, data = do(t, c, "POST", "/v1/boxes/"+box.ID+"/schedules/missing/run", api.ScheduleRunRequest{IdempotencyKey: "x"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown schedule status = %d, want 404 (body %s)", resp.StatusCode, data)
	}
}

func TestScheduleRunRefusesAnUnapprovedContract(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.work]\ncommand = 'true'\n")
	box := createBoxAt(t, c, worktree)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, err := st.SetSchedules(box.ID, []state.Schedule{{Name: "nightly", Cron: "0 2 * * *", Job: "work"}}, armed); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	srv.Now = func() time.Time { return armed.Add(time.Hour) }

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/schedules/nightly/run", api.ScheduleRunRequest{IdempotencyKey: "one"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("unapproved run status = %d, want 409 (body %s)", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "approve") {
		t.Fatalf("refusal = %s, want the approval next step", data)
	}
	if items, err := st.Queue(); err != nil || len(items) != 0 {
		t.Fatalf("queue = %+v, err %v, want no occurrence", items, err)
	}
	got, err := st.Box(box.ID)
	if err != nil || got.Schedules[0].LastOutcome != state.ScheduleSkipped {
		t.Fatalf("schedule outcome = %+v (err %v), want the skip recorded", got.Schedules, err)
	}
	skipped := false
	for _, e := range listEvents(t, c, "?kind=schedule.skipped") {
		if e.Where.Schedule == "nightly" && strings.Contains(e.Reason, "approved") {
			skipped = true
		}
	}
	if !skipped {
		t.Fatal("no schedule.skipped event explains the refusal")
	}
}

func TestManualOccurrenceRunsAndRecordsTheOutcome(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{record: true})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.work]\ncommand = 'true'\n")
	box := createBoxAt(t, c, worktree)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "nightly", Cron: "0 2 * * *", Job: "work"})
	clock := &schedulerClock{now: armed.Add(30 * time.Second)}
	srv.Now = clock.Now

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/schedules/nightly/run", api.ScheduleRunRequest{IdempotencyKey: "one"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, body %s", resp.StatusCode, data)
	}
	var accepted api.ScheduleRunResponse
	if err := json.Unmarshal(data, &accepted); err != nil {
		t.Fatalf("decode run: %v (%s)", err, data)
	}
	runScheduler(t, srv, clock)

	waitFor(t, "the manual occurrence to finish", func() bool {
		got, err := st.Box(box.ID)
		return err == nil && got.Schedules[0].LastOutcome == state.ScheduleDone
	})
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	sched := got.Schedules[0]
	if sched.LastRunID != accepted.Run.ID {
		t.Fatalf("last_run_id = %q, want %q", sched.LastRunID, accepted.Run.ID)
	}
	if sched.LastJobID == "" || len(got.Jobs) != 1 || got.Jobs[0].ID != sched.LastJobID {
		t.Fatalf("last_job_id = %q, jobs = %+v, want the executed job", sched.LastJobID, got.Jobs)
	}
	if sched.LastFired != nil {
		t.Fatalf("last_fired = %v, want a manual run to leave the cron clock alone", sched.LastFired)
	}

	// The run's chain stays traceable from the schedule decision to the job.
	chain := listEvents(t, c, "?trace="+accepted.TraceID)
	kinds := make([]string, len(chain))
	for i, e := range chain {
		kinds[i] = e.Kind
	}
	for _, want := range []string{events.KindScheduleFired, events.KindJobStarted, events.KindJobDone} {
		if !containsString(kinds, want) {
			t.Fatalf("trace %s events = %v, want %s", accepted.TraceID, kinds, want)
		}
	}
}

func TestWarmUpOccurrenceRecordsWarmOutcomeAndStaysVisible(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	c := client(socket)
	box := createBox(t, c)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "warm", Cron: "0 3 * * *"})
	clock := &schedulerClock{now: armed.Add(30 * time.Second)}
	srv.Now = clock.Now

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/schedules/warm/run", api.ScheduleRunRequest{IdempotencyKey: "one"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("warm-up run status = %d, body %s", resp.StatusCode, data)
	}
	runScheduler(t, srv, clock)
	waitFor(t, "the warm-up to finish", func() bool {
		got, err := st.Box(box.ID)
		return err == nil && got.Schedules[0].LastOutcome == state.ScheduleWarm
	})
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.Schedules[0].LastJobID != "" {
		t.Fatalf("warm-up last_job_id = %q, want none", got.Schedules[0].LastJobID)
	}
	// It is still a visible schedule with its definition and next due.
	resp, data = do(t, c, "GET", "/v1/schedules?box="+box.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/schedules status = %d, body %s", resp.StatusCode, data)
	}
	var list api.ScheduleListResponse
	if err := json.Unmarshal(data, &list); err != nil || len(list.Schedules) != 1 {
		t.Fatalf("schedules = %+v, err %v, want the warm-up listed", list, err)
	}
	if list.Schedules[0].Name != "warm" || list.Schedules[0].Job != "" || list.Schedules[0].LastOutcome != state.ScheduleWarm {
		t.Fatalf("warm-up = %+v, want its definition and outcome", list.Schedules[0])
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestSchedulerSkipsInvalidCronWithADurableReason(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	c := client(socket)
	box := createBox(t, c)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, err := st.SetSchedules(box.ID, []state.Schedule{{Name: "broken", Cron: "not a cron"}}, armed); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	clock := &schedulerClock{now: armed.Add(time.Minute)}
	runScheduler(t, srv, clock)

	waitFor(t, "the invalid cron skip recorded", func() bool {
		got, err := st.Box(box.ID)
		return err == nil && got.Schedules[0].LastOutcome == state.ScheduleSkipped
	})
	skipped := listEvents(t, c, "?kind=schedule.skipped")
	if len(skipped) != 1 {
		t.Fatalf("schedule.skipped events = %d (%+v), want one decision", len(skipped), skipped)
	}
	if skipped[0].Where.Schedule != "broken" || !strings.Contains(skipped[0].Reason, "invalid cron") {
		t.Fatalf("skip event = %+v, want the schedule name and reason", skipped[0])
	}
	if tasks, err := st.Tasks(); err != nil || len(tasks) != 0 {
		t.Fatalf("tasks = %+v, err %v, want no occurrence for an invalid cron", tasks, err)
	}
}

func TestSchedulerRecordsOneCaughtUpEventPerMissedOccurrence(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{record: true})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.work]\ncommand = 'true'\n")
	box := createBoxAt(t, c, worktree)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "every-minute", Cron: "* * * * *", Job: "work"})
	if _, err := st.AdvanceSchedule(box.ID, "every-minute", armed.Add(30*time.Second)); err != nil {
		t.Fatalf("AdvanceSchedule: %v", err)
	}
	clock := &schedulerClock{now: armed.Add(4*time.Minute + 30*time.Second)}
	runScheduler(t, srv, clock)

	waitFor(t, "all missed occurrences", func() bool {
		tasks, err := st.Tasks()
		return err == nil && len(tasks) == 4
	})
	// Let every dispatched occurrence finish before the temp state directory
	// is torn down; the loop's goroutines write to it.
	waitFor(t, "all missed occurrences done", func() bool {
		items, err := st.Queue()
		if err != nil || len(items) != 4 {
			return false
		}
		for _, item := range items {
			if item.State != state.QueueDone {
				return false
			}
		}
		return true
	})
	caught := listEvents(t, c, "?kind=schedule.caught_up")
	if len(caught) != 3 {
		t.Fatalf("schedule.caught_up events = %d (%+v), want the three past minutes", len(caught), caught)
	}
	seen := map[string]bool{}
	for _, e := range caught {
		if e.Where.Schedule != "every-minute" {
			t.Fatalf("caught-up event = %+v, want the schedule name", e)
		}
		occurrence := e.Attrs["occurrence"]
		if occurrence == "" || seen[occurrence] {
			t.Fatalf("caught-up occurrence %q is empty or duplicated", occurrence)
		}
		if !strings.Contains(e.Reason, "missed cron slot") {
			t.Fatalf("caught-up reason = %q, want the missed slot", e.Reason)
		}
		seen[occurrence] = true
	}
	fired := listEvents(t, c, "?kind=schedule.fired")
	if len(fired) != 1 || fired[0].Attrs["occurrence"] != armed.Add(4*time.Minute).Format(time.RFC3339) {
		t.Fatalf("schedule.fired = %+v, want only the current minute", fired)
	}
}

func TestFailedScheduledJobRecordsTheScheduleFailure(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobFailed, 1, "boom")
		return &job, nil
	}})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.work]\ncommand = 'true'\n")
	box := createBoxAt(t, c, worktree)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "nightly", Cron: "0 2 * * *", Job: "work"})
	clock := &schedulerClock{now: armed.Add(30 * time.Second)}
	srv.Now = clock.Now
	if resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/schedules/nightly/run", api.ScheduleRunRequest{IdempotencyKey: "one"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d, body %s", resp.StatusCode, data)
	}
	runScheduler(t, srv, clock)

	waitFor(t, "the manual occurrence to fail", func() bool {
		got, err := st.Box(box.ID)
		return err == nil && got.Schedules[0].LastOutcome == state.ScheduleFailed
	})
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.Schedules[0].LastJobID == "" {
		t.Fatalf("last_job_id = %q, want the failed job", got.Schedules[0].LastJobID)
	}
	failed := listEvents(t, c, "?kind=schedule.failed")
	if len(failed) != 1 || failed[0].Where.Schedule != "nightly" || failed[0].Reason != "boom" {
		t.Fatalf("schedule.failed = %+v, want one failure with the reason", failed)
	}
}
