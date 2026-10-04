package state_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/state"
)

func TestBoxesListsInCreationOrder(t *testing.T) {
	st := openStore(t, t.TempDir())
	a, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox alpha: %v", err)
	}
	b, _, err := st.CreateBox("beta", "dev", "/src/beta")
	if err != nil {
		t.Fatalf("CreateBox beta: %v", err)
	}

	boxes, recordErrs, err := st.Boxes()
	if err != nil {
		t.Fatalf("Boxes: %v", err)
	}
	if len(recordErrs) != 0 {
		t.Fatalf("unexpected record errors: %v", recordErrs)
	}
	if len(boxes) != 2 {
		t.Fatalf("got %d boxes, want 2", len(boxes))
	}
	if boxes[0].ID != a.ID || boxes[1].ID != b.ID {
		t.Fatalf("order = [%s %s], want [%s %s]", boxes[0].ID, boxes[1].ID, a.ID, b.ID)
	}
}

func TestCorruptRecordIsReportedWithoutHidingOthers(t *testing.T) {
	st := openStore(t, t.TempDir())
	good, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	badDir := filepath.Join(st.Root(), "boxes", "11111111-2222-4333-8444-555555555555")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir corrupt box: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "box.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}

	boxes, recordErrs, err := st.Boxes()
	if err != nil {
		t.Fatalf("Boxes: %v", err)
	}
	if len(boxes) != 1 || boxes[0].ID != good.ID {
		t.Fatalf("boxes = %+v, want only %s", boxes, good.ID)
	}
	if len(recordErrs) != 1 {
		t.Fatalf("recordErrs = %v, want 1", recordErrs)
	}
	if recordErrs[0].Path == "" || recordErrs[0].Err == "" {
		t.Fatalf("record error missing detail: %+v", recordErrs[0])
	}
}

func TestTransitionValidatesLifecycle(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}

	running, err := st.Transition(box.ID, state.StateRunning)
	if err != nil {
		t.Fatalf("created -> running: %v", err)
	}
	if running.State != state.StateRunning {
		t.Fatalf("state = %q, want running", running.State)
	}
	paused, err := st.Transition(box.ID, state.StatePaused)
	if err != nil {
		t.Fatalf("running -> paused: %v", err)
	}
	if paused.State != state.StatePaused {
		t.Fatalf("state = %q, want paused", paused.State)
	}
	if _, err := st.Transition(box.ID, state.StateCreated); err == nil {
		t.Fatal("paused -> created should be refused")
	}
}

func TestSetImagePinsVersion(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if box.Image != "" {
		t.Fatalf("new box image = %q, want empty", box.Image)
	}
	got, err := st.SetImage(box.ID, "0123456789abcdef")
	if err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	if got.Image != "0123456789abcdef" {
		t.Fatalf("image = %q", got.Image)
	}
	reloaded, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if reloaded.Image != "0123456789abcdef" {
		t.Fatalf("persisted image = %q", reloaded.Image)
	}
}

func TestSetPhasesPersists(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	got, err := st.SetPhases(box.ID, state.Phases{
		Synced:    true,
		Worktree:  "/home/dev/work/alpha",
		Provision: state.PhaseStatus{State: state.PhaseDone, ExitCode: 0},
		Wake:      state.PhaseStatus{State: state.PhaseRunning},
		Services:  []state.ServiceStatus{{Name: "web", State: "active", Port: 3000}},
	})
	if err != nil {
		t.Fatalf("SetPhases: %v", err)
	}
	if got.Phases == nil || !got.Phases.Synced || got.Phases.Provision.State != state.PhaseDone {
		t.Fatalf("phases = %+v", got.Phases)
	}
	reloaded, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if reloaded.Phases == nil || reloaded.Phases.Wake.State != state.PhaseRunning || len(reloaded.Phases.Services) != 1 {
		t.Fatalf("persisted phases = %+v", reloaded.Phases)
	}
}

func TestFailedBoxCanBePaused(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if _, err := st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatalf("running: %v", err)
	}
	if _, err := st.Transition(box.ID, state.StateFailed); err != nil {
		t.Fatalf("failed: %v", err)
	}
	paused, err := st.Transition(box.ID, state.StatePaused)
	if err != nil {
		t.Fatalf("failed -> paused: %v", err)
	}
	if paused.State != state.StatePaused {
		t.Fatalf("state = %q, want paused", paused.State)
	}
}

func TestTransitionRejectsUnknownStateAndPersists(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if _, err := st.Transition(box.ID, state.BoxState("melting")); err == nil {
		t.Fatal("unknown state should be rejected")
	}

	if _, err := st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatalf("created -> running: %v", err)
	}
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("persisted state = %q, want running", got.State)
	}
}

func TestCreateBoxIgnoresCorruptRecords(t *testing.T) {
	st := openStore(t, t.TempDir())
	badDir := filepath.Join(st.Root(), "boxes", "11111111-2222-4333-8444-555555555555")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir corrupt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "box.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	box, created, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox with corrupt record present: %v", err)
	}
	if !created || box.ID == "" {
		t.Fatalf("created=%v box=%+v", created, box)
	}

	boxes, recordErrs, err := st.Boxes()
	if err != nil {
		t.Fatalf("Boxes: %v", err)
	}
	if len(boxes) != 1 || len(recordErrs) != 1 {
		t.Fatalf("boxes=%d recordErrs=%d, want 1/1", len(boxes), len(recordErrs))
	}
}

func TestCreateBoxRefreshesBranchOnReUp(t *testing.T) {
	st := openStore(t, t.TempDir())
	first, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}

	again, created, err := st.CreateBox("alpha", "dev", "/src/alpha")
	if err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if created {
		t.Fatal("re-up should not create a second box")
	}
	if again.ID != first.ID {
		t.Fatalf("re-up id = %s, want %s", again.ID, first.ID)
	}
	if again.Branch != "dev" {
		t.Fatalf("branch = %q, want dev", again.Branch)
	}
	got, err := st.Box(first.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.Branch != "dev" {
		t.Fatalf("persisted branch = %q, want dev", got.Branch)
	}
}

func TestDestroyRemovesRecordAndDisk(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if err := st.DestroyBox(box.ID); err != nil {
		t.Fatalf("DestroyBox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), "boxes", box.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("box dir still present: %v", err)
	}
	if _, err := st.Box(box.ID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("Box after destroy = %v, want ErrNotFound", err)
	}
	if err := st.DestroyBox(box.ID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("second DestroyBox = %v, want ErrNotFound", err)
	}
}

func TestUnsupportedStateVersionIsRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "boxes"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "version"), []byte("99\n"), 0o644); err != nil {
		t.Fatalf("write version: %v", err)
	}
	if _, err := state.Open(root); err == nil {
		t.Fatal("Open should refuse an unsupported state version")
	}
}

func TestInvalidBoxIDIsRejected(t *testing.T) {
	st := openStore(t, t.TempDir())
	if _, err := st.Box("../../etc/passwd"); err == nil {
		t.Fatal("path traversal id should be rejected")
	}
	if err := st.DestroyBox("not-a-uuid"); err == nil {
		t.Fatal("invalid id should be rejected")
	}
}
