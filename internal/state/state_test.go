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

func TestCreateBoxPersistsAndReadsBack(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)

	box, created, err := st.CreateBox("pluto", "main", "/home/dev/src/pluto")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if !created {
		t.Fatal("first CreateBox should report created=true")
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
	if got.Project != "pluto" || got.Branch != "main" || got.Worktree != "/home/dev/src/pluto" {
		t.Fatalf("read back %+v, want project/branch/worktree pluto/main//home/dev/src/pluto", got)
	}
	if !got.CreatedAt.Equal(box.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, box.CreatedAt)
	}
}

func TestCreateBoxIsIdempotentPerWorktree(t *testing.T) {
	st := openStore(t, t.TempDir())

	first, created, err := st.CreateBox("pluto", "main", "/home/dev/src/pluto")
	if err != nil || !created {
		t.Fatalf("first CreateBox: created=%v err=%v", created, err)
	}
	second, created, err := st.CreateBox("pluto", "main", "/home/dev/src/pluto")
	if err != nil {
		t.Fatalf("second CreateBox: %v", err)
	}
	if created {
		t.Fatal("second CreateBox should report created=false")
	}
	if second.ID != first.ID {
		t.Fatalf("second box ID = %s, want %s", second.ID, first.ID)
	}
}

func TestRecordsSurviveReopen(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)
	box, _, err := st.CreateBox("pluto", "main", "/home/dev/src/pluto")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
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
