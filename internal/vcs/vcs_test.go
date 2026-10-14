package vcs_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/vcs"
)

// gitRepo makes a temp git checkout with one commit and returns its path.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v (%s)", err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("symbolic-ref", "HEAD", "refs/heads/main")
	run("commit", "--allow-empty", "-q", "-m", "first")
	return dir
}

func head(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return string(out[:len(out)-1])
}

// TestGitResolveReturnsWorkspaceAndRevision proves the git adapter resolves a
// checkout to its path and current revision without naming a branch.
func TestGitResolveReturnsWorkspaceAndRevision(t *testing.T) {
	dir := gitRepo(t)

	ws, rev, err := vcs.NewGit().Resolve(context.Background(), dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ws.Path != dir {
		t.Fatalf("workspace path = %q, want %q", ws.Path, dir)
	}
	if rev.Commit != head(t, dir) {
		t.Fatalf("revision = %q, want HEAD %q", rev.Commit, head(t, dir))
	}
}

// TestGitResolveReportsRemote proves the adapter surfaces the repo identity
// (the origin remote) so the core can key a workspace to its repo.
func TestGitResolveReportsRemote(t *testing.T) {
	dir := gitRepo(t)
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://example.com/app.git").CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v (%s)", err, out)
	}

	ws, _, err := vcs.NewGit().Resolve(context.Background(), dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ws.Remote != "https://example.com/app.git" {
		t.Fatalf("remote = %q, want the origin URL", ws.Remote)
	}
}

// TestGitResolveRejectsNonWorkspace proves a directory that is not a checkout
// fails rather than yielding an empty revision.
func TestGitResolveRejectsNonWorkspace(t *testing.T) {
	if _, _, err := vcs.NewGit().Resolve(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Resolve on a non-workspace succeeded, want an error")
	}
}

// TestFakeResolve proves the controllable adapter returns its scripted facts.
func TestFakeResolve(t *testing.T) {
	f := &vcs.Fake{Workspace: vcs.Workspace{Path: "/src/app", Remote: "git@example.com:app"}, Revision: vcs.Revision{Commit: "abc123"}}
	ws, rev, err := f.Resolve(context.Background(), "/some/other")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ws.Path != "/src/app" || ws.Remote != "git@example.com:app" || rev.Commit != "abc123" {
		t.Fatalf("fake returned %+v %+v", ws, rev)
	}
	if len(f.Calls) != 1 || f.Calls[0] != "/some/other" {
		t.Fatalf("fake calls = %v, want the requested path", f.Calls)
	}
	if filepath.Base(ws.Path) != "app" {
		t.Fatalf("workspace path = %q", ws.Path)
	}
}
