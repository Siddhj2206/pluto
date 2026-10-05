package state_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

func TestSchedulesSurviveReopen(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)
	box := createBox(t, st)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, err := st.SetSchedules(box.ID, []state.Schedule{
		{Name: "nightly", Cron: "0 2 * * *", Job: "test"},
		{Name: "warm", Cron: "*/5 * * * *"},
	}, armed); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	fired := armed.Add(14 * time.Hour)
	if _, err := st.AdvanceSchedule(box.ID, "nightly", fired); err != nil {
		t.Fatalf("AdvanceSchedule: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openStore(t, root)
	got, err := reopened.Box(box.ID)
	if err != nil {
		t.Fatalf("Box after reopen: %v", err)
	}
	if len(got.Schedules) != 2 {
		t.Fatalf("schedules after reopen = %+v, want two", got.Schedules)
	}
	nightly := got.Schedules[0]
	if nightly.Name != "nightly" || nightly.Cron != "0 2 * * *" || nightly.Job != "test" {
		t.Fatalf("nightly = %+v, want the declared schedule", nightly)
	}
	if !nightly.ArmedAt.Equal(armed) {
		t.Fatalf("nightly armed_at = %v, want %v", nightly.ArmedAt, armed)
	}
	if nightly.LastFired == nil || !nightly.LastFired.Equal(fired) {
		t.Fatalf("nightly last_fired = %v, want %v", nightly.LastFired, fired)
	}
	warm := got.Schedules[1]
	if warm.Name != "warm" || warm.Cron != "*/5 * * * *" || warm.Job != "" {
		t.Fatalf("warm = %+v, want a warm-up without a job", warm)
	}
	if warm.LastFired != nil {
		t.Fatalf("warm last_fired = %v, want none yet", warm.LastFired)
	}
}

func TestSetSchedulesPreservesUnchangedAndRearmsEdited(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, err := st.SetSchedules(box.ID, []state.Schedule{
		{Name: "nightly", Cron: "0 2 * * *", Job: "test"},
	}, t0); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	t1 := t0.Add(14 * time.Hour)
	if _, err := st.AdvanceSchedule(box.ID, "nightly", t1); err != nil {
		t.Fatalf("AdvanceSchedule: %v", err)
	}

	// A wake that applies the same schedule again must not re-arm it; a new
	// schedule is armed at that wake's time.
	t2 := t0.Add(24 * time.Hour)
	got, err := st.SetSchedules(box.ID, []state.Schedule{
		{Name: "nightly", Cron: "0 2 * * *", Job: "test"},
		{Name: "warm", Cron: "*/5 * * * *"},
	}, t2)
	if err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	if !got.Schedules[0].ArmedAt.Equal(t0) {
		t.Fatalf("unchanged armed_at = %v, want %v", got.Schedules[0].ArmedAt, t0)
	}
	if got.Schedules[0].LastFired == nil || !got.Schedules[0].LastFired.Equal(t1) {
		t.Fatalf("unchanged last_fired = %v, want %v", got.Schedules[0].LastFired, t1)
	}
	if !got.Schedules[1].ArmedAt.Equal(t2) || got.Schedules[1].LastFired != nil {
		t.Fatalf("new schedule = %+v, want it armed at %v with no firing yet", got.Schedules[1], t2)
	}

	// Editing the cron re-arms the schedule: occurrences before the edit
	// belonged to the old expression and must not be replayed.
	t3 := t2.Add(time.Hour)
	got, err = st.SetSchedules(box.ID, []state.Schedule{
		{Name: "nightly", Cron: "0 3 * * *", Job: "test"},
	}, t3)
	if err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	if len(got.Schedules) != 1 {
		t.Fatalf("schedules after edit = %+v, want only the edited one", got.Schedules)
	}
	if !got.Schedules[0].ArmedAt.Equal(t3) || got.Schedules[0].LastFired != nil {
		t.Fatalf("edited schedule = %+v, want it re-armed at %v", got.Schedules[0], t3)
	}
}

func TestAdvanceScheduleNeverMovesBackwards(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, err := st.SetSchedules(box.ID, []state.Schedule{{Name: "every-minute", Cron: "* * * * *"}}, armed); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	later := armed.Add(5 * time.Minute)
	if _, err := st.AdvanceSchedule(box.ID, "every-minute", later); err != nil {
		t.Fatalf("AdvanceSchedule: %v", err)
	}
	if _, err := st.AdvanceSchedule(box.ID, "every-minute", armed.Add(time.Minute)); err != nil {
		t.Fatalf("AdvanceSchedule backwards: %v", err)
	}
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.Schedules[0].LastFired == nil || !got.Schedules[0].LastFired.Equal(later) {
		t.Fatalf("last_fired = %v, want it held at %v", got.Schedules[0].LastFired, later)
	}

	if _, err := st.AdvanceSchedule(box.ID, "missing", later); err == nil {
		t.Fatal("AdvanceSchedule on an unknown schedule should fail")
	}
}

// A record written before schedules existed carries no schedules field and
// must still load: boxes survive pluto upgrades.
func TestRecordWithoutSchedulesStillLoads(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)
	box := createBox(t, st)
	path := root + "/boxes/" + box.ID + "/box.json"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if _, ok := raw["schedules"]; ok {
		t.Fatalf("record carries schedules despite none being stored: %s", data)
	}
	if _, err := st.Box(box.ID); err != nil {
		t.Fatalf("Box on a record without schedules: %v", err)
	}
	if _, err := st.AdvanceSchedule(box.ID, "missing", time.Now()); !errors.Is(err, state.ErrNoSchedule) {
		t.Fatalf("AdvanceSchedule error = %v, want ErrNoSchedule", err)
	}
}
