package state_test

import (
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/state"
)

func openStore(t *testing.T, root string) *state.Store {
	t.Helper()
	st, err := state.Open(root)
	if err != nil {
		t.Fatalf("Open(%q): %v", root, err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// createBox makes a container box for tests that need an existing record.
func createBox(t *testing.T, st *state.Store) *state.Box {
	t.Helper()
	box, _, err := st.CreateContainerBox(t.TempDir(), "repo", "rev")
	if err != nil {
		t.Fatalf("CreateContainerBox: %v", err)
	}
	return box
}

func TestCreateContainerBoxPersistsAndReadsBack(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)

	box, created, err := st.CreateContainerBox("/home/dev/src/pluto", "git@example.com:pluto", "deadbeef")
	if err != nil {
		t.Fatalf("CreateContainerBox: %v", err)
	}
	if !created {
		t.Fatal("first CreateContainerBox should report created=true")
	}
	if box.ID == "" {
		t.Fatal("box ID is empty")
	}
	if box.State != state.StateCreated {
		t.Fatalf("state = %q, want %q", box.State, state.StateCreated)
	}
	if box.Schema != state.RecordSchema {
		t.Fatalf("schema = %d, want %d", box.Schema, state.RecordSchema)
	}

	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box(%s): %v", box.ID, err)
	}
	if got.WorkspacePath != "/home/dev/src/pluto" || got.Repo != "git@example.com:pluto" || got.Revision != "deadbeef" {
		t.Fatalf("read back %+v", got)
	}
	if !got.CreatedAt.Equal(box.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, box.CreatedAt)
	}
}

func TestCreateContainerBoxIsIdempotentPerWorkspace(t *testing.T) {
	st := openStore(t, t.TempDir())

	first, created, err := st.CreateContainerBox("/home/dev/src/pluto", "pluto", "abc")
	if err != nil || !created {
		t.Fatalf("first CreateContainerBox: created=%v err=%v", created, err)
	}
	second, created, err := st.CreateContainerBox("/home/dev/src/pluto", "pluto", "abc")
	if err != nil {
		t.Fatalf("second CreateContainerBox: %v", err)
	}
	if created {
		t.Fatal("second CreateContainerBox should report created=false")
	}
	if second.ID != first.ID {
		t.Fatalf("second box ID = %s, want %s", second.ID, first.ID)
	}
}

func TestRecordsSurviveReopen(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)
	box, _, err := st.CreateContainerBox("/home/dev/src/pluto", "pluto", "abc")
	if err != nil {
		t.Fatalf("CreateContainerBox: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openStore(t, root)
	got, err := reopened.Box(box.ID)
	if err != nil {
		t.Fatalf("Box after reopen: %v", err)
	}
	if got.ID != box.ID {
		t.Fatalf("ID after reopen = %s, want %s", got.ID, box.ID)
	}
	if _, err := filepath.Glob(filepath.Join(root, "version")); err != nil {
		t.Fatalf("version file: %v", err)
	}
}

func TestSecondOpenIsRefusedWhileLocked(t *testing.T) {
	root := t.TempDir()
	openStore(t, root) // held until cleanup

	second, err := state.Open(root)
	if err == nil {
		second.Close()
		t.Fatal("second Open should fail while the first holds the lock")
	}
}
