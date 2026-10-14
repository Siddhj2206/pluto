package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// TestTomlContractIsNotTheContract is the cutover acceptance slice (#151):
// pluto.yml is the only supported Pluto contract. A workspace whose only
// contract is a legacy .pluto.toml declares no tasks, so `pluto run` refuses
// before any work reaches a box, and nothing is recorded.
func TestTomlContractIsNotTheContract(t *testing.T) {
	repo := gitRepo(t)
	legacy := "[jobs.dev]\ncommand = \"echo legacy\"\n"
	if err := os.WriteFile(filepath.Join(repo, ".pluto.toml"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write .pluto.toml: %v", err)
	}
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	code, _, errOut := runCLI(t, "--socket", socket, "run", "dev")
	if code == 0 {
		t.Fatalf("run dev succeeded against a .pluto.toml; pluto.yml must be the only contract")
	}
	if !strings.Contains(errOut, "no pluto.yml") {
		t.Fatalf("run dev stderr = %q, want a no-pluto.yml complaint", errOut)
	}
	if hasCall(engine.Calls(), "exec") {
		t.Fatalf("engine calls = %v, want no exec for a .pluto.toml task", engine.Calls())
	}
	if runs, err := st.Runs(); err != nil || len(runs) != 0 {
		t.Fatalf("runs = %v (%v), want none recorded", runs, err)
	}
}
