package state_test

import (
	"testing"

	"github.com/Siddhj2206/pluto/internal/state"
)

// TestEnsureWorkspaceIsIdempotentPerPath proves a workspace is a repo checkout
// identified by its path, and repeated ensures reuse one record.
func TestEnsureWorkspaceIsIdempotentPerPath(t *testing.T) {
	st := openStore(t, t.TempDir())

	first, err := st.EnsureWorkspace("/home/dev/src/app", "git@example.com:app")
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if first.ID == "" || first.Path != "/home/dev/src/app" || first.Repo != "git@example.com:app" {
		t.Fatalf("workspace = %+v", first)
	}
	second, err := st.EnsureWorkspace("/home/dev/src/app", "git@example.com:app")
	if err != nil {
		t.Fatalf("second EnsureWorkspace: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second ensure made a new workspace: %s != %s", second.ID, first.ID)
	}

	list, err := st.Workspaces()
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("workspaces = %d, want 1", len(list))
	}
}

// TestCreateContainerBoxIsOnePerWorkspace proves a workspace resolves to
// exactly one box, keyed by the workspace path and carrying no branch or
// worktree vocabulary.
func TestCreateContainerBoxIsOnePerWorkspace(t *testing.T) {
	st := openStore(t, t.TempDir())

	first, created, err := st.CreateContainerBox("/src/app", "git@example.com:app", "abc123")
	if err != nil {
		t.Fatalf("CreateContainerBox: %v", err)
	}
	if !created {
		t.Fatal("first create should report created")
	}
	if first.WorkspacePath != "/src/app" || first.Repo != "git@example.com:app" || first.Revision != "abc123" {
		t.Fatalf("box = %+v", first)
	}
	if first.State != state.StateCreated {
		t.Fatalf("state = %q, want created", first.State)
	}

	second, created, err := st.CreateContainerBox("/src/app", "git@example.com:app", "def456")
	if err != nil {
		t.Fatalf("second CreateContainerBox: %v", err)
	}
	if created || second.ID != first.ID {
		t.Fatalf("second create = %+v created=%v, want the existing box", second, created)
	}
}

// TestSetBoxRuntimeRecordsEngineLayersAndVolumes proves the box record keeps
// the engine handle and the layers and volumes it was composed from.
func TestSetBoxRuntimeRecordsEngineLayersAndVolumes(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateContainerBox("/src/app", "app", "abc123")
	if err != nil {
		t.Fatalf("CreateContainerBox: %v", err)
	}

	updated, err := st.SetBoxRuntime(box.ID, "container-7", []state.Layer{{Kind: "base", Key: "base:1"}, {Kind: "package", Key: "package:2"}}, []state.Volume{{Kind: "workspace", Name: "pluto-1-workspace"}, {Kind: "home", Name: "pluto-1-home"}})
	if err != nil {
		t.Fatalf("SetBoxRuntime: %v", err)
	}
	if updated.EngineID != "container-7" {
		t.Fatalf("engine id = %q", updated.EngineID)
	}

	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if len(got.Layers) != 2 || got.Layers[0].Key != "base:1" || got.Layers[1].Kind != "package" {
		t.Fatalf("layers = %+v", got.Layers)
	}
	if len(got.Volumes) != 2 || got.Volumes[0].Name != "pluto-1-workspace" {
		t.Fatalf("volumes = %+v", got.Volumes)
	}
}

// TestSetWorkspaceBoxLinksOneBox proves the workspace points at its one box.
func TestSetWorkspaceBoxLinksOneBox(t *testing.T) {
	st := openStore(t, t.TempDir())
	ws, err := st.EnsureWorkspace("/src/app", "app")
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	box, _, err := st.CreateContainerBox("/src/app", "app", "abc")
	if err != nil {
		t.Fatalf("CreateContainerBox: %v", err)
	}
	if err := st.SetWorkspaceBox(ws.ID, box.ID); err != nil {
		t.Fatalf("SetWorkspaceBox: %v", err)
	}
	got, err := st.WorkspaceByPath("/src/app")
	if err != nil {
		t.Fatalf("WorkspaceByPath: %v", err)
	}
	if got.BoxID != box.ID {
		t.Fatalf("linked box = %q, want %q", got.BoxID, box.ID)
	}
}

// TestContainerBoxLifecycleStates proves the container lifecycle moves
// created -> running -> stopped -> running, and that stopped and frozen are
// valid recorded states.
func TestContainerBoxLifecycleStates(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateContainerBox("/src/app", "app", "abc")
	if err != nil {
		t.Fatalf("CreateContainerBox: %v", err)
	}
	for _, next := range []state.BoxState{state.StateRunning, state.StateStopped, state.StateRunning, state.StateFrozen, state.StateStopped} {
		got, err := st.Transition(box.ID, next)
		if err != nil {
			t.Fatalf("Transition(%s): %v", next, err)
		}
		if got.State != next {
			t.Fatalf("state = %q, want %q", got.State, next)
		}
	}
	if !state.StateFrozen.Valid() || !state.StateStopped.Valid() {
		t.Fatal("frozen/stopped should be valid box states")
	}
}
