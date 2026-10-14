package state_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

// A run round-trips through the store with its task, box, timing, exit code,
// and one implicit step.
func TestRunHistoryRoundTrips(t *testing.T) {
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	run := state.StartRun(state.NewID(), "dev", "11111111-1111-4111-8111-111111111111", "/src/app", state.TriggerManual)
	step := state.NewStep("run", "pnpm dev")
	started := time.Now().UTC()
	step.Start(started)
	run.Steps = []state.Step{step}
	if _, err := st.BeginRun(run); err != nil {
		t.Fatalf("BeginRun: %v", err)
	}

	run.Steps[0].Finish(0, "", started.Add(1500*time.Millisecond))
	run.Finish(started.Add(1500 * time.Millisecond))
	if run.State != state.RunDone || run.ExitCode != 0 {
		t.Fatalf("finished run = %+v, want done exit 0", run)
	}
	if run.DurationMS != 1500 {
		t.Fatalf("DurationMS = %d, want 1500", run.DurationMS)
	}
	if _, err := st.SetRun(run); err != nil {
		t.Fatalf("SetRun: %v", err)
	}

	got, err := st.Run(run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Task != "dev" || got.BoxID != run.BoxID || got.State != state.RunDone || got.ExitCode != 0 {
		t.Fatalf("Run = %+v, want the recorded dev run", got)
	}
	if len(got.Steps) != 1 || got.Steps[0].Name != "run" || got.Steps[0].Command != "pnpm dev" || got.Steps[0].State != state.RunDone {
		t.Fatalf("steps = %+v, want one done step", got.Steps)
	}
}

// A failed step fails the run and carries its exit code.
func TestRunFailsWhenAStepFails(t *testing.T) {
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	run := state.StartRun(state.NewID(), "build", "11111111-1111-4111-8111-111111111111", "/src/app", state.TriggerManual)
	now := time.Now().UTC()
	step := state.NewStep("run", "make test")
	step.Start(now)
	step.Finish(3, "", now.Add(2*time.Second))
	run.Steps = []state.Step{step}
	run.Finish(now.Add(2 * time.Second))
	if run.State != state.RunFailed || run.ExitCode != 3 {
		t.Fatalf("run = %+v, want failed exit 3", run)
	}
}

// InterruptRunningRuns marks every in-flight run failed, so an orphan left by a
// crashed daemon cannot block its task's dispatch. A pending or finished run is
// left untouched.
func TestInterruptRunningRunsMarksOrphansFailed(t *testing.T) {
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	const box = "11111111-1111-4111-8111-111111111111"

	orphan := state.StartRun(state.NewID(), "build", box, "/src/app", state.TriggerSchedule)
	step := state.NewStep("run", "make test")
	step.Start(time.Now().UTC())
	orphan.Steps = []state.Step{step}
	if _, err := st.BeginRun(orphan); err != nil {
		t.Fatalf("BeginRun orphan: %v", err)
	}

	now := time.Now().UTC()
	pending := state.NewScheduledRun(state.NewID(), "build", box, "/src/app", "nightly", now, now)
	if _, _, err := st.AcceptScheduledRun(pending, now); err != nil {
		t.Fatalf("AcceptScheduledRun: %v", err)
	}

	n, err := st.InterruptRunningRuns(now)
	if err != nil {
		t.Fatalf("InterruptRunningRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("interrupted = %d, want the one orphan", n)
	}
	got, err := st.Run(orphan.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.State != state.RunFailed || got.Error == "" || got.FinishedAt == nil {
		t.Fatalf("orphan = %+v, want failed with a reason and a finish time", got)
	}
	if len(got.Steps) != 1 || got.Steps[0].State != state.RunFailed {
		t.Fatalf("orphan steps = %+v, want the in-flight step failed", got.Steps)
	}
	if still, err := st.Run(pending.ID); err != nil || still.State != state.RunPending {
		t.Fatalf("pending run = %+v (%v), want it left pending", still, err)
	}
}

// Runs list newest first and resolve by id or an unambiguous prefix.
func TestRunsListNewestFirstAndResolve(t *testing.T) {
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	first := state.StartRun(state.NewID(), "dev", "b1", "/src/app", state.TriggerManual)
	first.CreatedAt = time.Now().UTC().Add(-time.Minute)
	if _, err := st.BeginRun(first); err != nil {
		t.Fatalf("BeginRun first: %v", err)
	}
	second := state.StartRun(state.NewID(), "build", "b1", "/src/app", state.TriggerManual)
	if _, err := st.BeginRun(second); err != nil {
		t.Fatalf("BeginRun second: %v", err)
	}

	runs, err := st.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != second.ID || runs[1].ID != first.ID {
		t.Fatalf("runs = %+v, want newest first", runs)
	}

	resolved, err := st.ResolveRun(second.ID[:8])
	if err != nil {
		t.Fatalf("ResolveRun prefix: %v", err)
	}
	if resolved.ID != second.ID {
		t.Fatalf("ResolveRun = %s, want %s", resolved.ID, second.ID)
	}

	if _, err := st.Run(state.NewID()); !errors.Is(err, state.ErrRunNotFound) {
		t.Fatalf("unknown run error = %v, want ErrRunNotFound", err)
	}
}
