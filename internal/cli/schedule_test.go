package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	apiclient "github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// scheduleFixture creates a box for a real worktree, approves its contract,
// and stores the given schedules at a fixed arm time.
func scheduleFixture(t *testing.T, socket string, st *state.Store, armed time.Time, scheds ...state.Schedule) string {
	t.Helper()
	repo := gitRepo(t)
	writeJobContract(t, repo)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	box := boxes[0]
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		t.Fatalf("Load contract: %v", err)
	}
	if _, err := st.ApproveContract(box.Project, ct.Hash(), armed); err != nil {
		t.Fatalf("ApproveContract: %v", err)
	}
	if _, err := st.SetSchedules(box.ID, scheds, armed); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	return box.ID
}

func TestScheduleLsShowsNextDueAndLastOutcome(t *testing.T) {
	socket, st, srv := startDaemonFull(t, fakeRunner{})
	fixed := time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return fixed }
	boxID := scheduleFixture(t, socket, st, fixed.Add(-time.Hour),
		state.Schedule{Name: "nightly", Cron: "0 2 * * *", Job: "test"},
		state.Schedule{Name: "warm", Cron: "0 3 * * *"},
	)
	if _, err := st.SetScheduleOutcome(boxID, "nightly", state.ScheduleDone, "run1234", "job1234"); err != nil {
		t.Fatalf("SetScheduleOutcome: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "schedule", "ls")
	if code != 0 {
		t.Fatalf("schedule ls exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"BOX", "NAME", "CRON", "NEXT", "LAST FIRED", "OUTCOME", "RUN/JOB", shortID(boxID), "nightly", "0 2 * * *", "warm", "done", "run1234/job1234"} {
		if !strings.Contains(out, want) {
			t.Fatalf("schedule ls output = %q, want %q", out, want)
		}
	}
	// The next due is the API's value rendered in local time, not a guess.
	wantDue := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC).Local().Format("2006-01-02 15:04")
	if !strings.Contains(out, wantDue) {
		t.Fatalf("schedule ls output = %q, want the next due %q", out, wantDue)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "schedule", "ls", boxID, "--json")
	if code != 0 {
		t.Fatalf("schedule ls --json exit = %d, stderr: %s", code, errOut)
	}
	var list api.ScheduleListResponse
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decode schedules: %v (%s)", err, out)
	}
	if list.SchemaVersion != 1 || len(list.Schedules) != 2 || list.Schedules[0].Name != "nightly" {
		t.Fatalf("schedules = %+v, want the two stored schedules", list)
	}
	if list.Schedules[1].Job != "" {
		t.Fatalf("warm schedule = %+v, want a visible warm-up", list.Schedules[1])
	}

	code, out, _ = runCLI(t, "--socket", socket, "schedule", "ls", "no-such-worktree")
	if code == 0 {
		t.Fatal("schedule ls for an unknown target should fail")
	}
}

func TestScheduleShowListsDefinitionAndOccurrenceHistory(t *testing.T) {
	socket, st, srv := startDaemonFull(t, fakeRunner{})
	fixed := time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return fixed }
	boxID := scheduleFixture(t, socket, st, fixed.Add(-time.Hour),
		state.Schedule{Name: "nightly", Cron: "0 2 * * *", Job: "test"},
	)
	accepted, err := apiclient.New(socket).RunSchedule(boxID, "nightly", api.ScheduleRunRequest{IdempotencyKey: "one"})
	if err != nil {
		t.Fatalf("RunSchedule: %v", err)
	}
	if _, err := st.UpdateQueueItem(accepted.Run.QueueID, state.QueueDone, "job1234", "", fixed); err != nil {
		t.Fatalf("UpdateQueueItem: %v", err)
	}
	if _, err := st.SetScheduleOutcome(boxID, "nightly", state.ScheduleDone, accepted.Run.ID, "job1234"); err != nil {
		t.Fatalf("SetScheduleOutcome: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "schedule", "show", boxID, "nightly")
	if code != 0 {
		t.Fatalf("schedule show exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"cron:     0 2 * * *", "job:      test", "armed:", "next:", "last:     done", "OCCURRENCE", "SOURCE", "manual", shortID(accepted.Run.ID), "completed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("schedule show output = %q, want %q", out, want)
		}
	}

	code, out, errOut = runCLI(t, "--socket", socket, "schedule", "show", boxID, "nightly", "--json")
	if code != 0 {
		t.Fatalf("schedule show --json exit = %d, stderr: %s", code, errOut)
	}
	var show api.ScheduleShowResponse
	if err := json.Unmarshal([]byte(out), &show); err != nil {
		t.Fatalf("decode show: %v (%s)", err, out)
	}
	if show.SchemaVersion != 1 || show.Schedule.Name != "nightly" || len(show.Occurrences) != 1 {
		t.Fatalf("show = %+v, want the definition and one occurrence", show)
	}
	occ := show.Occurrences[0]
	if occ.Source != state.QueueManual || occ.RunID != accepted.Run.ID || occ.JobID != "job1234" || occ.State != state.TaskRunCompleted {
		t.Fatalf("occurrence = %+v, want the explicit run's history", occ)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "schedule", "show", boxID, "missing")
	if code == 0 || !strings.Contains(errOut, "no schedule") {
		t.Fatalf("schedule show for an unknown name = (%d, %q), want a no-schedule error", code, errOut)
	}
}

func TestScheduleRunQueuesOneOccurrenceWithoutMovingTheClock(t *testing.T) {
	socket, st, srv := startDaemonFull(t, fakeRunner{})
	fixed := time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return fixed }
	boxID := scheduleFixture(t, socket, st, fixed.Add(-time.Hour),
		state.Schedule{Name: "nightly", Cron: "0 2 * * *", Job: "test"},
	)

	code, out, errOut := runCLI(t, "--socket", socket, "schedule", "run", boxID, "nightly")
	if code != 0 {
		t.Fatalf("schedule run exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"occurrence manual:", "run ", "next:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("schedule run output = %q, want %q", out, want)
		}
	}
	items, err := st.Queue()
	if err != nil || len(items) != 1 {
		t.Fatalf("queue = %+v, err %v, want one explicit occurrence", items, err)
	}
	if items[0].Source != state.QueueManual || items[0].ScheduleName != "nightly" {
		t.Fatalf("queue item = %+v, want a manual schedule occurrence", items[0])
	}
	box, err := st.Box(boxID)
	if err != nil || box.Schedules[0].LastFired != nil {
		t.Fatalf("last_fired = %+v (err %v), want the cron clock unmoved", box.Schedules, err)
	}

	// Each invocation is a new explicit occurrence; the daemon dedupes only a
	// retried request carrying the same idempotency key.
	if code, _, errOut := runCLI(t, "--socket", socket, "schedule", "run", boxID, "nightly"); code != 0 {
		t.Fatalf("second schedule run exit = %d, stderr: %s", code, errOut)
	}
	if items, err := st.Queue(); err != nil || len(items) != 2 {
		t.Fatalf("queue after a second invocation = %+v, err %v, want two occurrences", items, err)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "schedule", "run", boxID, "nightly", "--json")
	if code != 0 {
		t.Fatalf("schedule run --json exit = %d, stderr: %s", code, errOut)
	}
	var accepted api.ScheduleRunResponse
	if err := json.Unmarshal([]byte(out), &accepted); err != nil {
		t.Fatalf("decode run: %v (%s)", err, out)
	}
	if accepted.SchemaVersion != 1 || accepted.Schedule != "nightly" || accepted.Run.Job != "test" || accepted.TraceID == "" {
		t.Fatalf("run = %+v, want the accepted occurrence", accepted)
	}
}

func TestScheduleRunRefusesAnUnapprovedContract(t *testing.T) {
	socket, st, srv := startDaemonFull(t, fakeRunner{})
	fixed := time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return fixed }
	repo := gitRepo(t)
	writeJobContract(t, repo)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	box := boxes[0]
	if _, err := st.SetSchedules(box.ID, []state.Schedule{{Name: "nightly", Cron: "0 2 * * *", Job: "test"}}, fixed.Add(-time.Hour)); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}

	code, _, errOut := runCLI(t, "--socket", socket, "schedule", "run", box.ID, "nightly")
	if code == 0 {
		t.Fatal("schedule run should refuse an unapproved contract")
	}
	if !strings.Contains(errOut, "approve") {
		t.Fatalf("stderr = %q, want the approval next step", errOut)
	}
	if items, err := st.Queue(); err != nil || len(items) != 0 {
		t.Fatalf("queue = %+v, err %v, want no occurrence", items, err)
	}
}

func TestStatusShowsScheduleNextDueAndLastOutcome(t *testing.T) {
	socket, st, srv := startDaemonFull(t, fakeRunner{})
	fixed := time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)
	srv.Now = func() time.Time { return fixed }
	boxID := scheduleFixture(t, socket, st, fixed.Add(-time.Hour),
		state.Schedule{Name: "nightly", Cron: "0 2 * * *", Job: "test"},
	)
	if _, err := st.SetScheduleOutcome(boxID, "nightly", state.ScheduleFailed, "run1234", "job1234"); err != nil {
		t.Fatalf("SetScheduleOutcome: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", boxID)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr: %s", code, errOut)
	}
	wantDue := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC).Local().Format("2006-01-02 15:04")
	for _, want := range []string{"schedule:", "nightly 0 2 * * *", "next " + wantDue, "last failed", "job job1234"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output = %q, want %q", out, want)
		}
	}
}

func TestScheduleHelpAndSubcommandErrors(t *testing.T) {
	code, out, errOut := runCLI(t, "help", "schedule")
	if code != 0 {
		t.Fatalf("help schedule exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"pluto schedule ls", "pluto schedule show", "pluto schedule run"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help schedule = %q, want %q", out, want)
		}
	}

	code, _, errOut = runCLI(t, "schedule")
	if code != 2 || !strings.Contains(errOut, "needs a subcommand") {
		t.Fatalf("bare schedule = (%d, %q), want a usage error", code, errOut)
	}
	code, _, errOut = runCLI(t, "schedule", "lss")
	if code != 2 || !strings.Contains(errOut, "did you mean 'pluto schedule ls'?") {
		t.Fatalf("unknown schedule subcommand = (%d, %q), want the closest match", code, errOut)
	}
}
