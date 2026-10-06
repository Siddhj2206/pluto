package state_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/state"
)

// TestBoxResourcesRoundTrip pins the record field the runner sizes the machine
// from: resources persist and read back unchanged.
func TestBoxResourcesRoundTrip(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateBox("app", "main", "/src/app")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	res := &state.Resources{CPUs: 4, MemoryMiB: 8192}
	updated, err := st.SetResources(box.ID, res)
	if err != nil {
		t.Fatalf("SetResources: %v", err)
	}
	if updated.Resources == nil || *updated.Resources != *res {
		t.Fatalf("returned resources = %+v, want %+v", updated.Resources, res)
	}

	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.Resources == nil || got.Resources.CPUs != 4 || got.Resources.MemoryMiB != 8192 {
		t.Fatalf("read back resources = %+v, want 4/8192", got.Resources)
	}
}

// TestBoxWithoutResourcesDecodesNil guards backward compatibility: a record
// written before the field existed (RecordSchema stays 1) reads as "unset",
// and the runner's defaults apply.
func TestBoxWithoutResourcesDecodesNil(t *testing.T) {
	st := openStore(t, t.TempDir())
	box, _, err := st.CreateBox("app", "main", "/src/app")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	path := filepath.Join(st.Root(), "boxes", box.ID, "box.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	// Rewrite the record without the resources key, as an older pluto would.
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	delete(fields, "resources")
	old, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatalf("write old record: %v", err)
	}

	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box after downgrade: %v", err)
	}
	if got.Schema != state.RecordSchema {
		t.Fatalf("schema = %d, want %d (no bump)", got.Schema, state.RecordSchema)
	}
	if got.Resources != nil {
		t.Fatalf("resources = %+v, want nil for a legacy record", got.Resources)
	}
}
