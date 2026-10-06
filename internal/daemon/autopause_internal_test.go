package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// stubRunner is the smallest BoxRunner that can drive the auto-pause bookkeeping
// directly. The HTTP-level behavior is covered from the external test package;
// these tests reach the sampling seam, which has no observable HTTP effect
// except over time.
type stubRunner struct {
	st       *state.Store
	window   time.Duration
	usage    state.SessionUsage
	usageSet bool
}

func (r *stubRunner) Up(context.Context, *state.Box) (*state.Box, error) {
	return nil, errors.New("unused")
}

func (r *stubRunner) Pause(box *state.Box) (*state.Box, error) {
	return r.st.Transition(box.ID, state.StatePaused)
}

func (r *stubRunner) Attach(context.Context, *state.Box) (api.AttachInfo, error) {
	return api.AttachInfo{}, nil
}

func (r *stubRunner) Reconcile(box *state.Box) (*state.Box, error) { return box, nil }

func (r *stubRunner) Refresh(box *state.Box) (*state.Box, error) {
	phases := state.Phases{Synced: true}
	if box.Phases != nil {
		phases = *box.Phases
	}
	if r.usageSet {
		usage := r.usage
		phases.SessionUsage = &usage
	}
	return r.st.SetPhases(box.ID, phases)
}

func (r *stubRunner) RunJob(context.Context, *state.Box, contract.Exec, func([]byte)) (*state.Box, *state.Job, error) {
	return nil, nil, errors.New("unused")
}

func (r *stubRunner) Logs(*state.Box, string, string, int) (string, error) { return "", nil }
func (r *stubRunner) JobLog(*state.Box, string, int) (string, error)       { return "", nil }
func (r *stubRunner) AutoPauseWindow(*state.Box) time.Duration             { return r.window }
func (r *stubRunner) ContractStale(*state.Box) bool                        { return false }
func (r *stubRunner) Destroy(id string) error                              { return r.st.DestroyBox(id) }
func (r *stubRunner) Import(string) (string, error)                        { return "", errors.New("unused") }
func (r *stubRunner) Images() ([]api.ImageInfo, error)                     { return nil, nil }

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// runningSessionBox creates a running box whose stored phases carry a session
// cgroup reading.
func runningSessionBox(t *testing.T, st *state.Store, usage state.SessionUsage) *state.Box {
	t.Helper()
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if _, err := st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	box, err = st.SetPhases(box.ID, state.Phases{Synced: true, SessionUsage: &usage})
	if err != nil {
		t.Fatalf("SetPhases: %v", err)
	}
	return box
}

func boxWithUsage(box *state.Box, usage state.SessionUsage) *state.Box {
	copied := *box
	phases := *box.Phases
	phases.SessionUsage = &usage
	copied.Phases = &phases
	return &copied
}

// A read look must compare against the loop's stored sample without advancing
// it: otherwise an interleaved `pluto status` resets the baseline and the next
// loop tick judges a rising session on too small a delta and sleeps the box.
func TestEvaluateAutoPauseDoesNotAdvanceTheSessionSample(t *testing.T) {
	st := newTestStore(t)
	box := runningSessionBox(t, st, state.SessionUsage{CPUUsec: 0})
	srv := &Server{store: st, runner: &stubRunner{st: st, window: time.Hour}}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	srv.rememberSessions(box)

	for i := int64(1); i <= 5; i++ {
		reading := state.SessionUsage{CPUUsec: i * 100_000}
		got, _ := srv.evaluateAutoPause(boxWithUsage(box, reading), now.Add(time.Duration(i)*time.Second))
		if got.IdleSince != nil {
			t.Fatalf("look %d started an idle clock while the session was working", i)
		}
	}
	if got := srv.sessionSamples[box.ID]; got.CPUUsec != 0 {
		t.Fatalf("read path advanced the sample to %d cpu usec, want the loop baseline 0", got.CPUUsec)
	}
}

// The loop tick advances the stored sample, so each tick judges the session
// against the previous tick rather than against every status look in between.
func TestPauseIdleAdvancesTheSessionSample(t *testing.T) {
	st := newTestStore(t)
	box := runningSessionBox(t, st, state.SessionUsage{})
	run := &stubRunner{st: st, window: time.Hour, usage: state.SessionUsage{CPUUsec: 10}, usageSet: true}
	srv := &Server{store: st, runner: run}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	srv.pauseIdle(now)
	if got := srv.sessionSamples[box.ID]; got.CPUUsec != 10 {
		t.Fatalf("after one tick sample = %d, want 10", got.CPUUsec)
	}
	run.usage = state.SessionUsage{CPUUsec: 20}
	srv.pauseIdle(now.Add(time.Second))
	if got := srv.sessionSamples[box.ID]; got.CPUUsec != 20 {
		t.Fatalf("after the next tick sample = %d, want 20", got.CPUUsec)
	}
}

// Destroying a box forgets its sample, so the bookkeeping map tracks live
// boxes rather than growing once per box ever created.
func TestDeleteForgetsTheBoxSessionSample(t *testing.T) {
	st := newTestStore(t)
	box := runningSessionBox(t, st, state.SessionUsage{})
	srv := &Server{
		store:          st,
		runner:         &stubRunner{st: st},
		sessionSamples: map[string]state.SessionUsage{box.ID: {CPUUsec: 99}},
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/boxes/"+box.ID, nil)
	req.SetPathValue("id", box.ID)
	rec := httptest.NewRecorder()

	srv.handleDelete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body %s", rec.Code, rec.Body.String())
	}
	if _, ok := srv.sessionSamples[box.ID]; ok {
		t.Fatal("sample for a destroyed box was kept")
	}
}
