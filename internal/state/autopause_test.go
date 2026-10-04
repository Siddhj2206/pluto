package state_test

import (
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

func TestSetAutoPauseAndIdleSincePersist(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	updated, err := st.SetAutoPause(box.ID, "45m")
	if err != nil {
		t.Fatalf("SetAutoPause: %v", err)
	}
	if updated.AutoPause != "45m" {
		t.Fatalf("auto_pause = %q, want 45m", updated.AutoPause)
	}

	since := time.Now().UTC().Truncate(time.Millisecond)
	updated, err = st.SetIdleSince(box.ID, &since)
	if err != nil {
		t.Fatalf("SetIdleSince: %v", err)
	}
	if updated.IdleSince == nil || !updated.IdleSince.Equal(since) {
		t.Fatalf("idle_since = %v, want %s", updated.IdleSince, since)
	}

	// Box reads the record from disk, so this also proves persistence.
	reread, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if reread.AutoPause != "45m" || reread.IdleSince == nil || !reread.IdleSince.Equal(since) {
		t.Fatalf("record = %+v, want the auto-pause fields to survive", reread)
	}
}

func TestSetIdleSinceNilClearsTheClock(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)
	since := time.Now().UTC()
	if _, err := st.SetIdleSince(box.ID, &since); err != nil {
		t.Fatalf("SetIdleSince: %v", err)
	}

	updated, err := st.SetIdleSince(box.ID, nil)
	if err != nil {
		t.Fatalf("SetIdleSince(nil): %v", err)
	}
	if updated.IdleSince != nil {
		t.Fatalf("idle_since = %v, want cleared", updated.IdleSince)
	}
}

func TestTransitionClearsIdleSince(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)
	if _, err := st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatalf("running: %v", err)
	}
	since := time.Now().UTC()
	if _, err := st.SetIdleSince(box.ID, &since); err != nil {
		t.Fatalf("SetIdleSince: %v", err)
	}

	got, err := st.Transition(box.ID, state.StatePaused)
	if err != nil {
		t.Fatalf("paused: %v", err)
	}
	if got.IdleSince != nil {
		t.Fatalf("idle_since = %v, want the clock reset on a state change", got.IdleSince)
	}
}
