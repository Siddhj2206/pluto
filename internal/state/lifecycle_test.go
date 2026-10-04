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
