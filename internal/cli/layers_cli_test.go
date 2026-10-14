package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// writeWorkspace writes files under a temp checkout.
func writeWorkspace(t *testing.T, root string, files map[string]string) {
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

func provenanceContract(base string) string {
	return "version: 1\nprovision:\n  base: " + base + "\n  packages: [nodejs]\n  watch: [pnpm-lock.yaml]\n"
}

// boxRecord reads the one box a workspace resolved to, through the store the
// daemon wrote.
func boxRecord(t *testing.T, st *state.Store, workspace string) *state.Box {
	t.Helper()
	ws, err := st.WorkspaceByPath(workspace)
	if err != nil {
		t.Fatalf("WorkspaceByPath(%s): %v", workspace, err)
	}
	box, err := st.Box(ws.BoxID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	return box
}

// TestBoxCreateBuildsLayersFromProvision is the acceptance slice: a workspace's
// pluto.yml drives layer building through the real CLI, daemon, and store, and
// pluto box status surfaces both content-addressed layers.
func TestBoxCreateBuildsLayersFromProvision(t *testing.T) {
	repo := gitRepo(t)
	writeWorkspace(t, repo, map[string]string{
		"pluto.yml":      provenanceContract("ubuntu:24.04"),
		"pnpm-lock.yaml": "lockfileVersion: 9\n",
	})
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Remote: "git@example.com:app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)

	code, out, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", repo)
	if code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "created box") {
		t.Fatalf("box create output = %q", out)
	}
	record := boxRecord(t, st, repo)
	if len(record.Layers) != 2 || record.Layers[0].Kind != "base" || record.Layers[1].Kind != "package" {
		t.Fatalf("layers = %+v, want base and package", record.Layers)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "box", "status", repo)
	if code != 0 {
		t.Fatalf("box status exit = %d, stderr: %s", code, errOut)
	}
	if got := strings.Count(out, "layer:"); got != 2 {
		t.Fatalf("box status output = %q, want two layers", out)
	}
	if !strings.Contains(out, "layer:     base "+record.Layers[0].Key) || !strings.Contains(out, "layer:     package "+record.Layers[1].Key) {
		t.Fatalf("box status output = %q, want both layer keys", out)
	}
}

// TestProvisionChangeReachesNewBoxesNotExistingOnes proves a provision change
// flows to newly created boxes while an existing box keeps its layers and
// volumes.
func TestProvisionChangeReachesNewBoxesNotExistingOnes(t *testing.T) {
	one, two := gitRepo(t), gitRepo(t)
	writeWorkspace(t, one, map[string]string{"pluto.yml": provenanceContract("ubuntu:24.04"), "pnpm-lock.yaml": "lockfileVersion: 9\n"})
	writeWorkspace(t, two, map[string]string{"pluto.yml": provenanceContract("ubuntu:22.04"), "pnpm-lock.yaml": "lockfileVersion: 9\n"})
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Remote: "git@example.com:app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)

	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", one); code != 0 {
		t.Fatalf("box create one exit = %d, stderr: %s", code, errOut)
	}
	before := boxRecord(t, st, one)

	// Changing provision must not reach the existing box.
	writeWorkspace(t, one, map[string]string{"pluto.yml": provenanceContract("ubuntu:22.04"), "pnpm-lock.yaml": "lockfileVersion: 9\n"})
	code, out, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", one)
	if code != 0 {
		t.Fatalf("second box create exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "already exists") {
		t.Fatalf("second box create output = %q, want 'already exists'", out)
	}
	after := boxRecord(t, st, one)
	if after.ID != before.ID {
		t.Fatalf("provision change recreated the box: %s -> %s", before.ID, after.ID)
	}
	if len(after.Layers) != len(before.Layers) || after.Layers[0].Key != before.Layers[0].Key {
		t.Fatalf("existing box's layers changed: %+v -> %+v", before.Layers, after.Layers)
	}
	if len(after.Volumes) != len(before.Volumes) || after.Volumes[0].Name != before.Volumes[0].Name {
		t.Fatalf("existing box's volumes changed: %+v -> %+v", before.Volumes, after.Volumes)
	}

	// The new provision reaches a newly created box.
	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", two); code != 0 {
		t.Fatalf("box create two exit = %d, stderr: %s", code, errOut)
	}
	newBox := boxRecord(t, st, two)
	if len(newBox.Layers) != 2 {
		t.Fatalf("new box layers = %+v, want base and package", newBox.Layers)
	}
	if newBox.Layers[0].Key == before.Layers[0].Key {
		t.Fatalf("changed base produced the same layer key: %q", newBox.Layers[0].Key)
	}
}
