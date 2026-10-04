package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/cli"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
)

// fakeRunner stands in for the VM lifecycle: it moves records through the
// same states the real runner would.
type fakeRunner struct{ st *state.Store }

func (f fakeRunner) Up(ctx context.Context, box *state.Box) (*state.Box, error) {
	return f.st.Transition(box.ID, state.StateRunning)
}

func (f fakeRunner) Pause(box *state.Box) (*state.Box, error) {
	return f.st.Transition(box.ID, state.StatePaused)
}

func (f fakeRunner) Attach(ctx context.Context, box *state.Box) (api.AttachInfo, error) {
	return api.AttachInfo{User: "dev", UDS: "/tmp/v.sock", Key: "/tmp/id", Port: 22}, nil
}

func (f fakeRunner) Reconcile(box *state.Box) (*state.Box, error) { return box, nil }

func (f fakeRunner) Refresh(box *state.Box) (*state.Box, error) { return box, nil }

func (f fakeRunner) Logs(box *state.Box, phase, service string, lines int) (string, error) {
	return "log of " + phase + service, nil
}

func (f fakeRunner) Destroy(id string) error { return f.st.DestroyBox(id) }

func (f fakeRunner) Import(srcDir string) (string, error) { return "ver123", nil }

func (f fakeRunner) Images() ([]api.ImageInfo, error) {
	return []api.ImageInfo{{Version: "ver123"}}, nil
}

func startDaemon(t *testing.T) (socket string, st *state.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	srv := daemon.New(st, fakeRunner{st}, "test")
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

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("set branch: %v (%s)", err, out)
	}
	return dir
}

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = cli.Run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestUpLsStatusDestroy(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)

	code, out, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo)
	if code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "created box") {
		t.Fatalf("up output = %q, want 'created box'", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "up", "--worktree", repo)
	if code != 0 {
		t.Fatalf("second up exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "already exists") {
		t.Fatalf("second up output = %q, want 'already exists'", out)
	}
	if !strings.Contains(out, "running") {
		t.Fatalf("second up output = %q, want running", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "ls")
	if code != 0 {
		t.Fatalf("ls exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "main") || !strings.Contains(out, "running") {
		t.Fatalf("ls output = %q, want branch and running state", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, repo) || !strings.Contains(out, "running") {
		t.Fatalf("status output = %q, want worktree and running state", out)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "destroy", repo)
	if code == 0 {
		t.Fatal("destroy without --yes in a non-interactive session should fail")
	}
	if !strings.Contains(errOut, "--yes") {
		t.Fatalf("destroy stderr = %q, want a --yes hint", errOut)
	}

	code, out, _ = runCLI(t, "--socket", socket, "ls")
	if !strings.Contains(out, "main") {
		t.Fatal("box should still exist after a refused destroy")
	}

	code, out, errOut = runCLI(t, "--socket", socket, "destroy", repo, "--yes")
	if code != 0 {
		t.Fatalf("destroy --yes exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "destroyed") {
		t.Fatalf("destroy output = %q, want 'destroyed'", out)
	}

	code, out, _ = runCLI(t, "--socket", socket, "ls")
	if strings.Contains(out, "main") {
		t.Fatalf("box still listed after destroy: %q", out)
	}
}

func TestLsWithoutDaemonIsLegible(t *testing.T) {
	code, _, errOut := runCLI(t, "--socket", filepath.Join(t.TempDir(), "missing.sock"), "ls")
	if code == 0 {
		t.Fatal("ls should fail without a daemon")
	}
	if !strings.Contains(errOut, "daemon") {
		t.Fatalf("stderr = %q, want a daemon hint", errOut)
	}
}

func TestUpOutsideGitWorktreeFails(t *testing.T) {
	socket, _ := startDaemon(t)
	code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", t.TempDir())
	if code == 0 {
		t.Fatal("up outside a git worktree should fail")
	}
	if !strings.Contains(errOut, "git") {
		t.Fatalf("stderr = %q, want a git hint", errOut)
	}
}

func TestPauseFlow(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	code, out, errOut := runCLI(t, "--socket", socket, "pause", repo)
	if code != 0 {
		t.Fatalf("pause exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "paused") {
		t.Fatalf("pause output = %q, want paused", out)
	}
	code, out, _ = runCLI(t, "--socket", socket, "ls")
	if code != 0 || !strings.Contains(out, "paused") {
		t.Fatalf("ls after pause = %q, want paused state", out)
	}
}

func TestImageCommands(t *testing.T) {
	socket, _ := startDaemon(t)
	code, out, errOut := runCLI(t, "--socket", socket, "image", "import", t.TempDir())
	if code != 0 {
		t.Fatalf("image import exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "ver123") {
		t.Fatalf("image import output = %q, want the version", out)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "image", "ls")
	if code != 0 {
		t.Fatalf("image ls exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "ver123") {
		t.Fatalf("image ls output = %q, want the version", out)
	}
}

func TestLogsCommand(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "logs", repo)
	if code != 0 {
		t.Fatalf("logs exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "== provision ==") || !strings.Contains(out, "== wake ==") {
		t.Fatalf("logs output = %q, want both phase sections", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "logs", repo, "--service", "web")
	if code != 0 {
		t.Fatalf("service logs exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "== service web ==") {
		t.Fatalf("service logs output = %q", out)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "logs", repo, "--phase", "bogus")
	if code != 2 || !strings.Contains(errOut, "unknown phase") {
		t.Fatalf("bad phase: exit %d, stderr %q", code, errOut)
	}
}

func TestDestroyByIDRepairsCorruptRecord(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	id := boxes[0].ID

	record := filepath.Join(st.Root(), "boxes", id, "box.json")
	if err := os.WriteFile(record, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("corrupt record: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "destroy", id, "--yes")
	if code != 0 {
		t.Fatalf("destroy corrupt record exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "destroyed") {
		t.Fatalf("output = %q, want 'destroyed'", out)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), "boxes", id)); !os.IsNotExist(err) {
		t.Fatalf("box dir still present: %v", err)
	}
}
