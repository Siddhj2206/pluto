package state_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/state"
)

// runRecordedJob records one finished job on a box with a host log, the way
// the runner does: BeginJob, a host log written while it runs, then SetJob.
func runRecordedJob(t *testing.T, st *state.Store, boxID, command string) state.Job {
	t.Helper()
	job := state.StartJob(state.NewID(), []string{command})
	job.Log = filepath.Join("jobs", job.ID+".log")
	if _, err := st.BeginJob(boxID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	logPath := filepath.Join(st.Root(), "boxes", boxID, job.Log)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("create jobs dir: %v", err)
	}
	if err := os.WriteFile(logPath, []byte(command+" output\n"), 0o644); err != nil {
		t.Fatalf("write host log: %v", err)
	}
	if _, err := st.SetJob(boxID, finishJob(job, 0)); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	return job
}

func TestJobHistoryRetainsTwentyNewestFirst(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	for i := 0; i < 25; i++ {
		runRecordedJob(t, st, box.ID, fmt.Sprintf("job-%02d", i))
	}

	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if len(got.Jobs) != 20 {
		t.Fatalf("history length = %d, want 20", len(got.Jobs))
	}
	// Newest first: job-24 is the head, job-05 the oldest retained.
	for i := 0; i < 20; i++ {
		want := fmt.Sprintf("job-%02d", 24-i)
		if got.Jobs[i].Command != want {
			t.Fatalf("history[%d].Command = %q, want %q", i, got.Jobs[i].Command, want)
		}
	}
	if latest := got.LatestJob(); latest == nil || latest.Command != "job-24" {
		t.Fatalf("LatestJob = %+v, want job-24", latest)
	}
}

func TestJobHistoryPrunesOldestHostLogs(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	var ran []state.Job
	for i := 0; i < 25; i++ {
		ran = append(ran, runRecordedJob(t, st, box.ID, fmt.Sprintf("job-%02d", i)))
	}

	for i, job := range ran {
		path := filepath.Join(st.Root(), "boxes", box.ID, job.Log)
		_, err := os.Stat(path)
		if i < len(ran)-20 {
			if err == nil {
				t.Fatalf("host log of pruned job %d still exists at %s", i, path)
			}
			if !os.IsNotExist(err) {
				t.Fatalf("stat pruned log %s: %v", path, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("host log of retained job %d missing: %v", i, err)
		}
	}
}

func TestJobHistorySurvivesReopen(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)
	box := createBox(t, st)
	first := runRecordedJob(t, st, box.ID, "first")
	second := runRecordedJob(t, st, box.ID, "second")
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openStore(t, root)
	got, err := reopened.Box(box.ID)
	if err != nil {
		t.Fatalf("Box after reopen: %v", err)
	}
	if len(got.Jobs) != 2 || got.Jobs[0].ID != second.ID || got.Jobs[1].ID != first.ID {
		t.Fatalf("history after reopen = %+v, want second then first", got.Jobs)
	}
}

func TestDestroyRemovesJobHistory(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)
	job := runRecordedJob(t, st, box.ID, "make test")

	if err := st.DestroyBox(box.ID); err != nil {
		t.Fatalf("DestroyBox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), "boxes", box.ID)); !os.IsNotExist(err) {
		t.Fatalf("box dir still present after destroy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), "boxes", box.ID, job.Log)); !os.IsNotExist(err) {
		t.Fatalf("host log still present after destroy: %v", err)
	}
}

// TestReadBoxUpgradesLegacyJobRecord pins the one-way upgrade: a state dir
// written before history existed keeps its single job as the head.
func TestReadBoxUpgradesLegacyJobRecord(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)
	box := createBox(t, st)
	legacyJobID := "11111111-2222-4333-8444-555555555555"

	legacy := fmt.Sprintf(`{
  "schema": 1,
  "id": %q,
  "project": "pluto",
  "branch": "main",
  "worktree": "/src/pluto",
  "state": "paused",
  "job": {
    "id": %q,
    "command": "make test",
    "state": "failed",
    "exit_code": 2,
    "log": "jobs/legacy.log",
    "started_at": "2026-10-01T10:00:00Z",
    "finished_at": "2026-10-01T10:00:02Z",
    "duration_ms": 2000
  },
  "created_at": "2026-10-01T09:00:00Z",
  "updated_at": "2026-10-01T10:00:02Z"
}`, box.ID, legacyJobID)
	record := filepath.Join(root, "boxes", box.ID, "box.json")
	if err := os.WriteFile(record, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}

	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if len(got.Jobs) != 1 || got.Jobs[0].ID != legacyJobID {
		t.Fatalf("history = %+v, want the legacy job as head", got.Jobs)
	}
	if got.Jobs[0].Command != "make test" || got.Jobs[0].State != state.JobFailed {
		t.Fatalf("legacy job = %+v, want failed make test", got.Jobs[0])
	}

	// A new job lands on top; the migrated job stays in history.
	newJob := runRecordedJob(t, st, box.ID, "make lint")
	got, err = st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box after new job: %v", err)
	}
	if len(got.Jobs) != 2 || got.Jobs[0].ID != newJob.ID || got.Jobs[1].ID != legacyJobID {
		t.Fatalf("history = %+v, want the new job then the migrated one", got.Jobs)
	}
}

func TestResolveJobLastPrefixAndExact(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	first := state.StartJob("11111111-2222-4333-8444-555555555555", []string{"first"})
	if _, err := st.BeginJob(box.ID, first); err != nil {
		t.Fatalf("BeginJob first: %v", err)
	}
	if _, err := st.SetJob(box.ID, finishJob(first, 0)); err != nil {
		t.Fatalf("SetJob first: %v", err)
	}
	second := state.StartJob("11111111-2222-4333-8444-666666666666", []string{"second"})
	if _, err := st.BeginJob(box.ID, second); err != nil {
		t.Fatalf("BeginJob second: %v", err)
	}
	if _, err := st.SetJob(box.ID, finishJob(second, 0)); err != nil {
		t.Fatalf("SetJob second: %v", err)
	}

	stored, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	cases := []struct {
		arg     string
		want    string
		wantErr string
	}{
		{"last", second.ID, ""},
		{first.ID, first.ID, ""},
		{"11111111-2222-4333-8444-5555", first.ID, ""},
		{"11111111", "", "more than one"},
		{"deadbeef", "", "no job"},
	}
	for _, tc := range cases {
		got, err := stored.ResolveJob(tc.arg)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ResolveJob(%q) error = %v, want %q", tc.arg, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ResolveJob(%q): %v", tc.arg, err)
		}
		if got.ID != tc.want {
			t.Fatalf("ResolveJob(%q) = %s, want %s", tc.arg, got.ID, tc.want)
		}
	}

	if _, err := stored.ResolveJob("last"); err != nil {
		t.Fatalf("ResolveJob(last): %v", err)
	}
	emptyBox, _, err := st.CreateBox("pluto", "main", "/src/empty")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	empty, err := st.Box(emptyBox.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if _, err := empty.ResolveJob("last"); err == nil {
		t.Fatal("ResolveJob on a box with no history should fail")
	}
}
