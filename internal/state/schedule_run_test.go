package state_test

import (
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

// scheduledCandidate builds the pending run a schedule occurrence proposes, the
// way the daemon does before handing it to the store.
func scheduledCandidate(task, boxID, workspace, schedule string, occurrence time.Time) state.Run {
	run := state.NewScheduledRun(state.NewID(), task, boxID, workspace, schedule, occurrence, occurrence.Add(time.Second))
	run.Steps = []state.Step{state.NewPendingStep("run", "make test")}
	return run
}

func scheduledAt(t *testing.T, st *state.Store, task, boxID, schedule string, occurrence time.Time) *state.Run {
	t.Helper()
	run, _, err := st.AcceptScheduledRun(scheduledCandidate(task, boxID, "ws", schedule, occurrence), occurrence.Add(time.Second))
	if err != nil {
		t.Fatalf("AcceptScheduledRun: %v", err)
	}
	return run
}

// A distinct occurrence adds one pending run to the task's history, carrying the
// schedule and occurrence it came from.
func TestAcceptScheduledRunCreatesOnePendingRunPerOccurrence(t *testing.T) {
	st := openRunStore(t)
	occ := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)

	run, created, err := st.AcceptScheduledRun(scheduledCandidate("build", "b1", "/src/app", "nightly", occ), occ.Add(time.Second))
	if err != nil {
		t.Fatalf("AcceptScheduledRun: %v", err)
	}
	if !created {
		t.Fatal("the first occurrence must create a run")
	}
	if run.State != state.RunPending {
		t.Fatalf("state = %q, want pending", run.State)
	}
	if run.Trigger != state.TriggerSchedule {
		t.Fatalf("trigger = %q, want schedule", run.Trigger)
	}
	if run.TriggerContext == nil || run.TriggerContext.Schedule != "nightly" || run.TriggerContext.Occurrence == nil || !run.TriggerContext.Occurrence.Equal(occ) {
		t.Fatalf("trigger context = %+v, want the nightly occurrence", run.TriggerContext)
	}
	if run.Task != "build" || run.BoxID != "b1" {
		t.Fatalf("run = %+v, want the build task on box b1", run)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one", runs, err)
	}
}

// Redelivering the same occurrence reuses the accepted run identity instead of
// creating duplicate history.
func TestAcceptScheduledRunReusesRedeliveredOccurrence(t *testing.T) {
	st := openRunStore(t)
	occ := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)

	first := scheduledAt(t, st, "build", "b1", "nightly", occ)
	again, created, err := st.AcceptScheduledRun(scheduledCandidate("build", "b1", "ws", "nightly", occ), occ.Add(time.Minute))
	if err != nil {
		t.Fatalf("AcceptScheduledRun redelivery: %v", err)
	}
	if created {
		t.Fatal("a redelivered occurrence must not create a run")
	}
	if again.ID != first.ID {
		t.Fatalf("redelivery reused %s, want the accepted %s", again.ID, first.ID)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one after a redelivery", runs, err)
	}
}

// A task has at most one pending run: a later occurrence coalesces into it,
// keeps only the latest trigger time, and notes the skipped occurrence.
func TestAcceptScheduledRunCoalescesIntoThePendingRun(t *testing.T) {
	st := openRunStore(t)
	t0 := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	pending := scheduledAt(t, st, "build", "b1", "nightly", t0)
	coalesced, created, err := st.AcceptScheduledRun(scheduledCandidate("build", "b1", "ws", "nightly", t1), t1.Add(time.Second))
	if err != nil {
		t.Fatalf("AcceptScheduledRun coalesce: %v", err)
	}
	if created {
		t.Fatal("a coalesced occurrence must not create a second run")
	}
	if coalesced.ID != pending.ID {
		t.Fatalf("coalesced into %s, want the pending %s", coalesced.ID, pending.ID)
	}
	if got := coalesced.TriggerContext.Occurrence; got == nil || !got.Equal(t1) {
		t.Fatalf("occurrence = %v, want the latest %v", got, t1)
	}
	if len(coalesced.TriggerContext.Skipped) != 1 || !coalesced.TriggerContext.Skipped[0].At.Equal(t0) {
		t.Fatalf("skipped = %+v, want the earlier occurrence noted", coalesced.TriggerContext.Skipped)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one pending run", runs, err)
	}
}

// An out-of-order occurrence also coalesces: the latest trigger time still wins
// and the earlier one is noted as skipped.
func TestAcceptScheduledRunNotesAnEarlierOccurrenceAsSkipped(t *testing.T) {
	st := openRunStore(t)
	t0 := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	scheduledAt(t, st, "build", "b1", "nightly", t1)
	got := scheduledAt(t, st, "build", "b1", "nightly", t0)
	if got.TriggerContext.Occurrence == nil || !got.TriggerContext.Occurrence.Equal(t1) {
		t.Fatalf("occurrence = %v, want the later %v to still win", got.TriggerContext.Occurrence, t1)
	}
	if len(got.TriggerContext.Skipped) != 1 || !got.TriggerContext.Skipped[0].At.Equal(t0) {
		t.Fatalf("skipped = %+v, want the earlier occurrence noted", got.TriggerContext.Skipped)
	}
}

func openRunStore(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
