package state_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

func createBox(t *testing.T, st *state.Store) *state.Box {
	t.Helper()
	box, _, err := st.CreateBox("pluto", "main", "/src/pluto")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	return box
}

func runningJob(command string) state.Job {
	return state.Job{
		ID:        state.NewID(),
		Command:   command,
		State:     state.JobRunning,
		StartedAt: time.Now().UTC(),
	}
}

func finishJob(job state.Job, exit int) state.Job {
	job.StartedAt = time.Now().UTC().Add(-30 * time.Millisecond) // the job ran for a while
	outcome := state.JobDone
	if exit != 0 {
		outcome = state.JobFailed
	}
	job.Finish(outcome, exit, "")
	return job
}

func TestStartJobKeepsTheCommandDisplay(t *testing.T) {
	argv := state.StartJob(state.NewID(), []string{"make", "test"})
	if argv.Command != "make test" || argv.State != state.JobRunning || argv.StartedAt.IsZero() {
		t.Fatalf("argv job = %+v", argv)
	}
	declared := state.StartJobCommand(state.NewID(), "pnpm test")
	if declared.Command != "pnpm test" || declared.State != state.JobRunning || declared.StartedAt.IsZero() {
		t.Fatalf("declared job = %+v", declared)
	}
}

func TestBeginJobRecordsRunningJob(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	job := runningJob("make test")
	got, err := st.BeginJob(box.ID, job)
	if err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	if got.LatestJob() == nil || got.Job(job.ID) == nil || got.LatestJob().State != state.JobRunning {
		t.Fatalf("job = %+v, want %s running", got.LatestJob(), job.ID)
	}
	if !got.JobRunning() {
		t.Fatal("JobRunning should report true for a running job")
	}
}

func TestBeginJobRefusesWhileOneRuns(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	first := runningJob("make test")
	if _, err := st.BeginJob(box.ID, first); err != nil {
		t.Fatalf("first BeginJob: %v", err)
	}
	_, err := st.BeginJob(box.ID, runningJob("make lint"))
	if !errors.Is(err, state.ErrJobRunning) {
		t.Fatalf("second BeginJob error = %v, want ErrJobRunning", err)
	}

	// Once the first job is recorded as finished, a new run is allowed.
	if _, err := st.SetJob(box.ID, finishJob(first, 0)); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	second := runningJob("make lint")
	got, err := st.BeginJob(box.ID, second)
	if err != nil {
		t.Fatalf("BeginJob after finish: %v", err)
	}
	if got.LatestJob().ID != second.ID {
		t.Fatalf("job = %s, want %s", got.LatestJob().ID, second.ID)
	}
}

func TestSetJobPersistsOutcome(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)
	job := runningJob("make test")
	if _, err := st.BeginJob(box.ID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}

	done := finishJob(job, 3)
	done.Error = "exit 3"
	done.Log = "jobs/test.log"
	if _, err := st.SetJob(box.ID, done); err != nil {
		t.Fatalf("SetJob: %v", err)
	}

	reopened, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	got := reopened.LatestJob()
	if got == nil || got.State != state.JobFailed || got.ExitCode != 3 || got.Error != "exit 3" {
		t.Fatalf("job = %+v, want failed exit 3", got)
	}
	if got.DurationMS <= 0 || got.FinishedAt == nil {
		t.Fatalf("job = %+v, want a duration and finish time", got)
	}
	if got.Log != "jobs/test.log" {
		t.Fatalf("log = %q, want the recorded output reference", got.Log)
	}
	if reopened.JobRunning() {
		t.Fatal("JobRunning should report false for a finished job")
	}
}

func TestSetJobKeepsTerminalState(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)
	job := runningJob("make test")
	if _, err := st.BeginJob(box.ID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	if _, err := st.SetJob(box.ID, finishJob(job, 0)); err != nil {
		t.Fatalf("SetJob: %v", err)
	}

	// A stale "running" report (a refresh that raced the finish) must not
	// regress a finished job.
	if _, err := st.SetJob(box.ID, job); err != nil {
		t.Fatalf("stale SetJob: %v", err)
	}
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.LatestJob().State != state.JobDone {
		t.Fatalf("state = %q, want done", got.LatestJob().State)
	}
}

func TestSetJobIgnoresUnknownJob(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)
	job := runningJob("make test")
	if _, err := st.BeginJob(box.ID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}

	// Only the box's recorded job may be updated; a different job never
	// hijacks the record (BeginJob is the way a new job starts).
	if _, err := st.SetJob(box.ID, finishJob(runningJob("other"), 0)); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.LatestJob().ID != job.ID {
		t.Fatalf("job = %s, want %s", got.LatestJob().ID, job.ID)
	}
}

func TestSetJobKeepsLogReference(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)
	job := runningJob("make test")
	job.Log = "jobs/test.log"
	if _, err := st.BeginJob(box.ID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}

	// The agent reports outcomes without a host log path; the recorded
	// reference must survive the merge.
	done := finishJob(job, 0)
	done.Log = ""
	if _, err := st.SetJob(box.ID, done); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.LatestJob().Log != "jobs/test.log" {
		t.Fatalf("log = %q, want the old reference kept", got.LatestJob().Log)
	}
}

func TestSetJobAcceptsAgentJobWhenNoneRecorded(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	// After a crash the box record may have lost the job; the agent's
	// report is then the truth.
	job := finishJob(runningJob("make test"), 0)
	if _, err := st.SetJob(box.ID, job); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.LatestJob() == nil || got.LatestJob().ID != job.ID {
		t.Fatalf("job = %+v, want the agent's job", got.LatestJob())
	}
}
