package box_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// newWorktreeHarness is newHarness with a workspace the manager can load
// pluto.yml from; the fake adapter resolves whichever path was requested.
func newWorktreeHarness(t *testing.T) *harness {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	engine := container.NewFake()
	// An empty Path makes the fake resolve the requested workspace path, so
	// several worktrees can share the harness's engine cache.
	adapter := &vcs.Fake{
		Workspace: vcs.Workspace{Remote: "git@example.com:app"},
		Revision:  vcs.Revision{Commit: "abc123"},
	}
	return &harness{m: box.New(st, engine, adapter), st: st, engine: engine, adapter: adapter}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// countOps counts recorded engine calls whose op prefix matches.
func countOps(calls []string, op string) int {
	n := 0
	for _, c := range calls {
		if c == op || strings.HasPrefix(c, op+":") {
			n++
		}
	}
	return n
}

const provisionContract = `version: 1
provision:
  base: ubuntu:24.04
  packages: [build-essential, nodejs]
  watch: [Cargo.lock]
`

// TestEnsureBuildsBaseAndPackageLayersFromProvision is the core slice: a
// workspace's pluto.yml builds a base layer and, on top of it, a package layer
// keyed on the watched lockfiles; the box is seeded from both.
func TestEnsureBuildsBaseAndPackageLayersFromProvision(t *testing.T) {
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{
		"pluto.yml":  provisionContract,
		"Cargo.lock": "version = 3\n",
	})
	h := newWorktreeHarness(t)

	created, isNew, err := h.m.Ensure(context.Background(), box.CreateRequest{Workspace: repo})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !isNew {
		t.Fatal("first Ensure should create a box")
	}
	if len(created.Layers) != 2 {
		t.Fatalf("layers = %+v, want base and package", created.Layers)
	}
	if created.Layers[0].Kind != "base" || created.Layers[1].Kind != "package" {
		t.Fatalf("layers = %+v, want base then package", created.Layers)
	}
	if builds := countOps(h.engine.Calls(), "build-layer"); builds != 2 {
		t.Fatalf("build-layer calls = %d, want base and package (%v)", builds, h.engine.Calls())
	}
	if len(created.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want workspace and home", created.Volumes)
	}
}

// TestEnsurePackageLayerIsACacheHitWhenLockfilesAreUnchanged proves an
// unchanged lockfile set rebuilds nothing: a second workspace with the same
// contract resolves both layers from the cache.
func TestEnsurePackageLayerIsACacheHitWhenLockfilesAreUnchanged(t *testing.T) {
	one, two := t.TempDir(), t.TempDir()
	for _, dir := range []string{one, two} {
		writeTree(t, dir, map[string]string{"pluto.yml": provisionContract, "Cargo.lock": "version = 3\n"})
	}
	h := newWorktreeHarness(t)

	first, _, err := h.m.Ensure(context.Background(), box.CreateRequest{Workspace: one})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	second, _, err := h.m.Ensure(context.Background(), box.CreateRequest{Workspace: two})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if builds := countOps(h.engine.Calls(), "build-layer"); builds != 2 {
		t.Fatalf("build-layer calls = %d after two boxes, want 2 cache-loaded layers (%v)", builds, h.engine.Calls())
	}
	if !sameLayerKeys(first.Layers, second.Layers) {
		t.Fatalf("layers differ across identical workspaces: %+v vs %+v", first.Layers, second.Layers)
	}
}

// TestEnsureRebuildsOnlyThePackageLayerWhenALockfileChanges proves a lockfile
// change invalidates the package layer alone; the base is a cache hit.
func TestEnsureRebuildsOnlyThePackageLayerWhenALockfileChanges(t *testing.T) {
	one, two := t.TempDir(), t.TempDir()
	writeTree(t, one, map[string]string{"pluto.yml": provisionContract, "Cargo.lock": "version = 3\n"})
	writeTree(t, two, map[string]string{"pluto.yml": provisionContract, "Cargo.lock": "version = 3\nname = \"serde\"\n"})
	h := newWorktreeHarness(t)

	first, _, err := h.m.Ensure(context.Background(), box.CreateRequest{Workspace: one})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	second, _, err := h.m.Ensure(context.Background(), box.CreateRequest{Workspace: two})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if builds := countOps(h.engine.Calls(), "build-layer"); builds != 3 {
		t.Fatalf("build-layer calls = %d, want base once and package twice (%v)", builds, h.engine.Calls())
	}
	if sameLayerKeys(first.Layers, second.Layers) {
		t.Fatalf("a changed lockfile left the package key unchanged: %+v", first.Layers)
	}
	if first.Layers[0].Key != second.Layers[0].Key {
		t.Fatalf("base layer changed when only the lockfile did: %q vs %q", first.Layers[0].Key, second.Layers[0].Key)
	}
	if first.Layers[1].Key == second.Layers[1].Key {
		t.Fatal("package layer key did not change with the lockfile")
	}
}

// TestEnsureLeavesAnExistingBoxAndItsVolumesAlone proves a provision change
// reaches only newly created boxes: the second Ensure returns the same box,
// with the same volumes and layers, and touches the engine no further.
func TestEnsureLeavesAnExistingBoxAndItsVolumesAlone(t *testing.T) {
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{"pluto.yml": provisionContract, "Cargo.lock": "version = 3\n"})
	h := newWorktreeHarness(t)
	ctx := context.Background()

	first, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: repo})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	callsAfterCreate := len(h.engine.Calls())

	// Editing provision must not reach the existing box or replace its volumes.
	writeTree(t, repo, map[string]string{"pluto.yml": strings.Replace(provisionContract, "ubuntu:24.04", "ubuntu:22.04", 1)})
	second, isNew, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: repo})
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if isNew || second.ID != first.ID {
		t.Fatalf("provision change recreated the box: %+v isNew=%v", second, isNew)
	}
	if len(h.engine.Calls()) != callsAfterCreate {
		t.Fatalf("re-ensuring an existing box touched the engine: %v after %v", h.engine.Calls(), callsAfterCreate)
	}
	if !sameVolumes(first.Volumes, second.Volumes) || !sameLayerKeys(first.Layers, second.Layers) {
		t.Fatalf("existing box's layers/volumes changed: %+v -> %+v", first, second)
	}
}

// TestEnsureBaseFromDockerfileBuildsContentAddressedLayer proves the
// Dockerfile form of provision.base builds a base layer, and the package layer
// sits on its resolved key.
func TestEnsureBaseFromDockerfileBuildsContentAddressedLayer(t *testing.T) {
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{
		"pluto.yml":        "version: 1\nprovision:\n  base: { dockerfile: ./pluto/Dockerfile }\n  packages: [nodejs]\n",
		"pluto/Dockerfile": "FROM ubuntu:24.04\n",
	})
	h := newWorktreeHarness(t)

	created, _, err := h.m.Ensure(context.Background(), box.CreateRequest{Workspace: repo})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(created.Layers) != 2 || created.Layers[0].Kind != "base" || created.Layers[1].Kind != "package" {
		t.Fatalf("layers = %+v, want base and package", created.Layers)
	}
	if builds := countOps(h.engine.Calls(), "build-layer"); builds != 2 {
		t.Fatalf("build-layer calls = %d, want base and package (%v)", builds, h.engine.Calls())
	}
}

func sameLayerKeys(a, b []state.Layer) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameVolumes(a, b []state.Volume) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
