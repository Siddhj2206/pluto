package cli_test

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/cli"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
)

func startDaemon(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	srv := daemon.New(st, "test")
	socket := filepath.Join(dir, "pluto.sock")
	if err := srv.Listen(socket); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() {
		srv.Shutdown(context.Background())
		st.Close()
	})
	return socket
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
	socket := startDaemon(t)
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

	code, out, errOut = runCLI(t, "--socket", socket, "ls")
	if code != 0 {
		t.Fatalf("ls exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "main") || !strings.Contains(out, "created") {
		t.Fatalf("ls output = %q, want branch and state", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, repo) || !strings.Contains(out, "created") {
		t.Fatalf("status output = %q, want worktree and state", out)
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
	socket := startDaemon(t)
	code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", t.TempDir())
	if code == 0 {
		t.Fatal("up outside a git worktree should fail")
	}
	if !strings.Contains(errOut, "git") {
		t.Fatalf("stderr = %q, want a git hint", errOut)
	}
}
