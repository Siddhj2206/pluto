package state_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/state"
)

func TestSetContractHashPersists(t *testing.T) {
	st := openStore(t, t.TempDir())
	box := createBox(t, st)

	updated, err := st.SetContractHash(box.ID, "a1b2c3")
	if err != nil {
		t.Fatalf("SetContractHash: %v", err)
	}
	if updated.ContractHash != "a1b2c3" {
		t.Fatalf("contract_hash = %q, want a1b2c3", updated.ContractHash)
	}

	// Box reads the record from disk, so this also proves persistence.
	reread, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if reread.ContractHash != "a1b2c3" {
		t.Fatalf("record contract_hash = %q, want it to survive the write", reread.ContractHash)
	}
}

// A record written before contract staleness existed carries no
// contract_hash and must still load: boxes survive pluto upgrades.
func TestRecordWithoutContractHashStillLoads(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)
	id := state.NewID()
	dir := filepath.Join(root, "boxes", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir box dir: %v", err)
	}
	old := fmt.Sprintf(`{"schema":%d,"id":%q,"project":"pluto","branch":"main","worktree":"/src/pluto","state":"created"}`, state.RecordSchema, id)
	if err := os.WriteFile(filepath.Join(dir, "box.json"), []byte(old), 0o644); err != nil {
		t.Fatalf("write old record: %v", err)
	}

	got, err := st.Box(id)
	if err != nil {
		t.Fatalf("Box on a pre-staleness record: %v", err)
	}
	if got.ContractHash != "" {
		t.Fatalf("contract_hash = %q, want empty on an old record", got.ContractHash)
	}
}
