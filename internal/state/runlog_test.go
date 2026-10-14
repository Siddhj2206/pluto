package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

// A run's step output is written to disk while the run is still going, so it
// survives the process and is readable by reference once the run is over.
func TestRunLogIsWrittenDuringTheRunAndReadBack(t *testing.T) {
	root := t.TempDir()
	st, err := state.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	run := state.StartRun(state.NewID(), "dev", "11111111-1111-4111-8111-111111111111", "/src/app", state.TriggerManual)
	log, err := st.CreateRunLog(run.ID, 0)
	if err != nil {
		t.Fatalf("CreateRunLog: %v", err)
	}
	ref := st.RunLogRef(run.ID, 0)
	if ref != filepath.ToSlash(filepath.Join("runs", run.ID, "0.log")) {
		t.Fatalf("RunLogRef = %q, want the run's step log path", ref)
	}
	if _, err := log.Write([]byte("first\n")); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	// The log is on disk and readable before the run (or the writer) finishes.
	got, err := st.ReadRunLog(ref)
	if err != nil {
		t.Fatalf("ReadRunLog mid-run: %v", err)
	}
	if got != "first\n" {
		t.Fatalf("mid-run log = %q, want the first chunk", got)
	}
	if _, err := log.Write([]byte("second\n")); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err = st.ReadRunLog(ref)
	if err != nil {
		t.Fatalf("ReadRunLog: %v", err)
	}
	if got != "first\nsecond\n" {
		t.Fatalf("retained log = %q, want both chunks", got)
	}

	// A run whose log was never written reads empty, not an error.
	missing, err := st.ReadRunLog(st.RunLogRef(state.NewID(), 0))
	if err != nil {
		t.Fatalf("ReadRunLog missing: %v", err)
	}
	if missing != "" {
		t.Fatalf("missing log = %q, want empty", missing)
	}
}

// The record version is the run record's own schema, set on every new run.
func TestStartRunStampsTheRunSchemaAndTriggerContext(t *testing.T) {
	run := state.StartRun(state.NewID(), "dev", "b1", "/src/app", state.TriggerManual)
	if run.Schema != state.RunSchema {
		t.Fatalf("run schema = %d, want %d", run.Schema, state.RunSchema)
	}
	if run.Trigger != state.TriggerManual {
		t.Fatalf("run trigger = %q, want manual", run.Trigger)
	}
	if run.TriggerContext == nil || run.TriggerContext.FiredAt.IsZero() {
		t.Fatalf("trigger context = %+v, want the fire time recorded", run.TriggerContext)
	}
}

// Retrieval prunes expired records and their logs together, keeps a still
// running run, and leaves fresh work alone.
func TestPruneRunsDropsExpiredRecordsAndLogs(t *testing.T) {
	root := t.TempDir()
	st, err := state.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()

	old := state.StartRun(state.NewID(), "dev", "b1", "/src/app", state.TriggerManual)
	old.CreatedAt = now.Add(-40 * 24 * time.Hour)
	finished := now.Add(-40 * 24 * time.Hour)
	old.FinishedAt = &finished
	old.State = state.RunDone
	old.Steps = []state.Step{{Name: "run", State: state.RunDone, Log: st.RunLogRef(old.ID, 0)}}
	if _, err := st.SetRun(old); err != nil {
		t.Fatalf("SetRun old: %v", err)
	}
	oldLog, err := st.CreateRunLog(old.ID, 0)
	if err != nil {
		t.Fatalf("CreateRunLog old: %v", err)
	}
	_, _ = oldLog.Write([]byte("stale output\n"))
	_ = oldLog.Close()

	fresh := state.StartRun(state.NewID(), "dev", "b1", "/src/app", state.TriggerManual)
	fresh.Steps = []state.Step{{Name: "run", State: state.RunDone, Log: st.RunLogRef(fresh.ID, 0)}}
	if _, err := st.SetRun(fresh); err != nil {
		t.Fatalf("SetRun fresh: %v", err)
	}
	freshLog, err := st.CreateRunLog(fresh.ID, 0)
	if err != nil {
		t.Fatalf("CreateRunLog fresh: %v", err)
	}
	_, _ = freshLog.Write([]byte("current output\n"))
	_ = freshLog.Close()

	// A run that never finished has no finish time; retention must not reap it.
	running := state.StartRun(state.NewID(), "dev", "b1", "/src/app", state.TriggerManual)
	running.CreatedAt = now.Add(-60 * 24 * time.Hour)
	if _, err := st.SetRun(running); err != nil {
		t.Fatalf("SetRun running: %v", err)
	}

	pruned, err := st.PruneRuns(now.Add(-state.DefaultRunRetention))
	if err != nil {
		t.Fatalf("PruneRuns: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want the one expired run", pruned)
	}
	if _, err := st.Run(old.ID); err == nil {
		t.Fatal("expired run record survived pruning")
	}
	if _, err := os.Stat(filepath.Join(root, "runs", old.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired run log dir survived pruning: %v", err)
	}
	if _, err := st.Run(fresh.ID); err != nil {
		t.Fatalf("fresh run was pruned: %v", err)
	}
	if got, err := st.ReadRunLog(fresh.Steps[0].Log); err != nil || got != "current output\n" {
		t.Fatalf("fresh run log = %q (%v), want it retained", got, err)
	}
	if _, err := st.Run(running.ID); err != nil {
		t.Fatalf("still-running run was pruned: %v", err)
	}
}
