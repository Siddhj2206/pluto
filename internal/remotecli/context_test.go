package remotecli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/clientcontext"
	"github.com/Siddhj2206/pluto/internal/testssh"
)

// gitRun runs git and fails the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmdArgs := args
	if dir != "" {
		cmdArgs = append([]string{"-C", dir}, args...)
	}
	if out, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
	}
}

// gitCheckout creates a committed checkout on main. With a project name it
// clones from a local bare origin of that name and returns the checkout; with
// an empty name it is a repository with no remote.
func gitCheckout(t *testing.T, project string) string {
	t.Helper()
	dir := t.TempDir()
	if project == "" {
		repo := filepath.Join(dir, "checkout")
		gitRun(t, "", "init", "--initial-branch=main", repo)
		seedCommit(t, repo)
		return repo
	}
	remote := filepath.Join(dir, project+".git")
	gitRun(t, "", "init", "--bare", "--initial-branch=main", remote)
	repo := filepath.Join(dir, "checkout")
	gitRun(t, "", "clone", remote, repo)
	seedCommit(t, repo)
	gitRun(t, repo, "push", "-u", "origin", "main")
	return repo
}

func seedCommit(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
}

// contextArg returns the PLUTO_CLIENT_CONTEXT word from an ssh argv and the
// argv without it.
func contextArg(t *testing.T, args []string) (*clientcontext.Context, []string) {
	t.Helper()
	for i, arg := range args {
		value, ok := strings.CutPrefix(arg, clientcontext.Env+"=")
		if !ok {
			continue
		}
		ctx, err := clientcontext.Parse(unquote(value))
		if err != nil {
			t.Fatalf("parse %s: %v", arg, err)
		}
		return ctx, append(append([]string{}, args[:i]...), args[i+1:]...)
	}
	t.Fatalf("ssh argv %q carries no %s", args, clientcontext.Env)
	return nil, nil
}

// A host command from a project checkout carries the local identity the host
// needs for a default target: project, branch, tracked remote, HEAD, and the
// dirty flag. The local path is never sent.
func TestRunSendsTheClientContextFromACheckout(t *testing.T) {
	deviceConfig(t)
	repo := gitCheckout(t, "cipher")
	t.Chdir(repo)
	log := testssh.Start(t, "", "", 0)

	code, _, errOut := runClient(t, "--device", "sid@host", "up")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}

	got := testssh.LoggedArgs(t, log)
	ctx, rest := contextArg(t, got)
	if ctx.Project != "cipher" || ctx.Branch != "main" || ctx.Remote != "origin" {
		t.Fatalf("context = %+v, want cipher/main on origin", ctx)
	}
	if want := gitOut(t, repo, "rev-parse", "HEAD"); ctx.Head != want {
		t.Fatalf("context head = %q, want %q", ctx.Head, want)
	}
	if !strings.HasSuffix(ctx.Source, filepath.Join("cipher.git")) {
		t.Fatalf("context source = %q, want the origin URL", ctx.Source)
	}
	if ctx.Dirty || ctx.NoRemote {
		t.Fatalf("context = %+v, want a clean checkout with a remote", ctx)
	}
	if strings.Contains(ctx.Source, repo) || strings.Contains(ctx.Head, repo) {
		t.Fatalf("context carries a local path: %+v", ctx)
	}

	want := append(append([]string{}, sshOptions...), "--", "sid@host", "env", "pluto", "up")
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("ssh argv without the context = %q, want %q", rest, want)
	}
}

// Uncommitted work is metadata: the box runs the committed branch, and the
// host can warn about it.
func TestRunSendsTheDirtyFlag(t *testing.T) {
	deviceConfig(t)
	repo := gitCheckout(t, "cipher")
	t.Chdir(repo)
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatalf("write scratch: %v", err)
	}
	log := testssh.Start(t, "", "", 0)

	if code, _, errOut := runClient(t, "--device", "sid@host", "status"); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	ctx, _ := contextArg(t, testssh.LoggedArgs(t, log))
	if !ctx.Dirty {
		t.Fatalf("context = %+v, want dirty", ctx)
	}
}

// A checkout with no remote cannot name a host project. The client says so in
// the context so the host fails with the next step instead of guessing.
func TestRunSendsNoRemoteContextForACheckoutWithoutARemote(t *testing.T) {
	deviceConfig(t)
	repo := gitCheckout(t, "")
	t.Chdir(repo)
	log := testssh.Start(t, "", "", 0)

	if code, _, errOut := runClient(t, "--device", "sid@host", "up"); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	ctx, _ := contextArg(t, testssh.LoggedArgs(t, log))
	if !ctx.NoRemote || ctx.Project != "" || ctx.Source != "" {
		t.Fatalf("context = %+v, want a no-remote context without a guessed project", ctx)
	}
	if ctx.Branch != "main" {
		t.Fatalf("context branch = %q, want main", ctx.Branch)
	}
}

// An explicit --worktree is a host-side key or path: the client forwards it
// unchanged, alongside the metadata the host may ignore.
func TestRunForwardsAHostWorktreeUnchanged(t *testing.T) {
	deviceConfig(t)
	repo := gitCheckout(t, "cipher")
	t.Chdir(repo)
	log := testssh.Start(t, "", "", 0)

	code, _, errOut := runClient(t, "--device", "sid@host", "up", "--worktree", "/srv/pluto/app")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	_, rest := contextArg(t, testssh.LoggedArgs(t, log))
	want := append(append([]string{}, sshOptions...),
		"--", "sid@host", "env", "pluto", "up", "--worktree", "/srv/pluto/app")
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("ssh argv = %q, want the host worktree preserved", rest)
	}
}

// Outside a project checkout the client sends no context, so the host can
// name the next step rather than inventing one.
func TestRunSendsNoContextOutsideACheckout(t *testing.T) {
	deviceConfig(t)
	log := testssh.Start(t, "", "", 0)

	if code, _, errOut := runClient(t, "--device", "sid@host", "status"); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	want := append(append([]string{}, sshOptions...), "--", "sid@host", "pluto", "status")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want no context", got)
	}
}

// unquote removes the single-quote wrapping shquote adds to the JSON value.
func unquote(word string) string {
	if len(word) >= 2 && word[0] == '\'' && word[len(word)-1] == '\'' {
		return strings.ReplaceAll(word[1:len(word)-1], `'\''`, `'`)
	}
	return word
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
