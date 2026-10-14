package cli_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// startBoxDaemon starts the real daemon and store over a real unix socket with
// the redrawn box lifecycle wired to a controllable fake engine and vcs
// adapter. The legacy runner is a no-op fake.
func startBoxDaemon(t *testing.T, engine container.Engine, adapter vcs.Adapter) (socket string, st *state.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	fr := fakeRunner{st: st}
	srv := daemon.New(st, fr, "test", daemon.WithBoxManager(box.New(st, engine, adapter)))
	socket = filepath.Join(dir, "pluto.sock")
	if err := srv.Listen(socket); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() {
		srv.Shutdown(context.Background())
		st.Close()
	})
	return socket, st
}

// TestBoxLifecycleThroughCLI proves a workspace resolves to one box through the
// vcs adapter, and pluto box create|ls|status|rm show and change its state, all
// the way through the daemon to the controllable fake engine.
func TestBoxLifecycleThroughCLI(t *testing.T) {
	repo := gitRepo(t)
	engine := container.NewFake()
	adapter := &vcs.Fake{
		Workspace: vcs.Workspace{Path: repo, Remote: "git@example.com:app"},
		Revision:  vcs.Revision{Commit: "deadbeef"},
	}
	socket, _ := startBoxDaemon(t, engine, adapter)

	// create: a box appears, running.
	code, out, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", repo)
	if code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "created box") || !strings.Contains(out, "running") {
		t.Fatalf("box create output = %q", out)
	}
	if len(adapter.Calls) != 1 || adapter.Calls[0] != repo {
		t.Fatalf("vcs calls = %v, want %q", adapter.Calls, repo)
	}

	// ls: the box lists with its workspace and state.
	code, out, errOut = runCLI(t, "--socket", socket, "box", "ls")
	if code != 0 {
		t.Fatalf("box ls exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "WORKSPACE") || !strings.Contains(out, repo) || !strings.Contains(out, "running") {
		t.Fatalf("box ls output = %q", out)
	}

	// status by workspace: the layer, volumes, and engine are visible.
	code, out, errOut = runCLI(t, "--socket", socket, "box", "status", repo)
	if code != 0 {
		t.Fatalf("box status exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"state:", "running", "workspace:", repo, "layer:", "volume:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("box status output = %q, want %q", out, want)
		}
	}

	// A second create resolves to the same one box.
	code, out, errOut = runCLI(t, "--socket", socket, "box", "create", "--workspace", repo)
	if code != 0 {
		t.Fatalf("second box create exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "already exists") {
		t.Fatalf("second box create output = %q, want 'already exists'", out)
	}

	// rm destroys the box, its volumes, and its record.
	code, out, errOut = runCLI(t, "--socket", socket, "box", "rm", repo, "--yes")
	if code != 0 {
		t.Fatalf("box rm exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "destroyed box") {
		t.Fatalf("box rm output = %q", out)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "box", "ls")
	if code != 0 {
		t.Fatalf("box ls after rm exit = %d, stderr: %s", code, errOut)
	}
	if strings.Contains(out, repo) {
		t.Fatalf("box ls after rm still lists the workspace: %q", out)
	}
	removed := 0
	for _, c := range engine.Calls() {
		if strings.HasPrefix(c, "destroy:") {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("engine destroy calls = %d, want 1 (%v)", removed, engine.Calls())
	}
}

// TestBoxStatusUnknownTargetFails proves an unknown workspace is a clean
// failure through the CLI.
func TestBoxStatusUnknownTargetFails(t *testing.T) {
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: "/nowhere", Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, _ := startBoxDaemon(t, engine, adapter)

	code, _, errOut := runCLI(t, "--socket", socket, "box", "status", "/does/not/exist")
	if code != 1 {
		t.Fatalf("box status exit = %d, want 1 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "pluto:") {
		t.Fatalf("box status stderr = %q", errOut)
	}
}

// TestBoxCreateDefaultsToCurrentWorkspace proves box create resolves the
// current directory when --workspace is omitted.
func TestBoxCreateDefaultsToCurrentWorkspace(t *testing.T) {
	repo := gitRepo(t)
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, _ := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "box", "create")
	if code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "created box") {
		t.Fatalf("box create output = %q", out)
	}
}
