package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/workspace"
)

// contextEnv points PLUTO_CLIENT_CONTEXT at a project branch on a fixture
// remote, the shape the client sends for `pluto --device host ...`.
func contextEnv(t *testing.T, remote, project, branch string) {
	t.Helper()
	t.Setenv(workspace.ClientContextEnv, `{"project":"`+project+`","branch":"`+branch+`","source":"`+remote+`","remote":"origin"}`)
}

// upFromContext creates the box the client checkout names.
func upFromContext(t *testing.T, socket string, st *state.Store) *state.Box {
	t.Helper()
	code, _, errOut := runCLI(t, "--socket", socket, "up")
	if code != 0 {
		t.Fatalf("up from context exit = %d, stderr: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err %v", len(boxes), err)
	}
	return boxes[0]
}

// No target and no box id: status, jobs, logs, and pause all resolve the
// project and branch the client checkout names.
func TestNoTargetCommandsUseTheClientContextBox(t *testing.T) {
	managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")
	contextEnv(t, remote, "cipher", "feature")
	box := upFromContext(t, socket, st)
	job := recordJob(t, st, box.ID, "test", 0, 1200)

	code, out, errOut := runCLI(t, "--socket", socket, "status")
	if code != 0 {
		t.Fatalf("status exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "id:       "+box.ID) || !strings.Contains(out, "project:  cipher") {
		t.Fatalf("status output = %q, want the context box", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "jobs")
	if code != 0 || !strings.Contains(out, shortID(job.ID)) {
		t.Fatalf("jobs = (%d, %q, %q), want the context box's job", code, out, errOut)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "logs", "--job", "last")
	if code != 0 || !strings.Contains(out, "job log of "+job.ID) {
		t.Fatalf("logs = (%d, %q, %q), want the context box's job log", code, out, errOut)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "pause")
	if code != 0 || !strings.Contains(out, "box "+shortID(box.ID)+" paused") {
		t.Fatalf("pause = (%d, %q, %q), want the context box paused", code, out, errOut)
	}
}

// run, job ls, and task create also default to the client's box instead of
// the host's current directory.
func TestRunJobLsAndTaskCreateUseTheClientContextBox(t *testing.T) {
	home := managedHome(t)
	var ranBox string
	socket, st := startDaemonWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		ranBox = box.ID
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	remote := managedRemote(t, "feature")
	contextEnv(t, remote, "cipher", "feature")
	box := upFromContext(t, socket, st)

	worktree := filepath.Join(home, "worktrees", "cipher-feature")
	if err := os.WriteFile(filepath.Join(worktree, contract.FileName), []byte("[jobs.test]\ncommand = \"true\"\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "job", "ls")
	if code != 0 || !strings.Contains(out, "test") {
		t.Fatalf("job ls = (%d, %q, %q), want the context worktree's jobs", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "run")
	if code != 0 || !strings.Contains(out, "test") {
		t.Fatalf("run listing = (%d, %q, %q), want the context worktree's jobs", code, out, errOut)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "run", "test")
	if code != 0 {
		t.Fatalf("run test exit = %d, stderr: %s", code, errOut)
	}
	if ranBox != box.ID {
		t.Fatalf("run test ran on box %q, want the context box %q", ranBox, box.ID)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "run", "--", "true")
	if code != 0 {
		t.Fatalf("run -- true exit = %d, stderr: %s", code, errOut)
	}
	if ranBox != box.ID {
		t.Fatalf("run -- true ran on box %q, want the context box %q", ranBox, box.ID)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "task", "create", "--job", "test", "--prompt", "fix it", "--json")
	if code != 0 {
		t.Fatalf("task create exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, `"box_id":"`+box.ID+`"`) {
		t.Fatalf("task create output = %q, want box %s", out, box.ID)
	}
}

// attach defaults to the client's box rather than the host's home directory.
func TestAttachUsesTheClientContextBox(t *testing.T) {
	managedHome(t)
	var attachedBox string
	socket, st := startDaemonWith(t, fakeRunner{attachID: &attachedBox})
	remote := managedRemote(t, "feature")
	contextEnv(t, remote, "cipher", "feature")
	box := upFromContext(t, socket, st)
	fakeDeviceSSH(t, "", "", 0)

	code, _, errOut := runCLI(t, "--socket", socket, "attach")
	if code != 0 {
		t.Fatalf("attach exit = %d, stderr: %s", code, errOut)
	}
	if attachedBox != box.ID {
		t.Fatalf("attach resolved box %q, want the context box %q", attachedBox, box.ID)
	}
}

// A second up from the same checkout resumes the box instead of creating a
// second one.
func TestUpFromContextResumesTheSameBox(t *testing.T) {
	managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")
	contextEnv(t, remote, "cipher", "feature")
	first := upFromContext(t, socket, st)

	code, out, errOut := runCLI(t, "--socket", socket, "up")
	if code != 0 {
		t.Fatalf("second up exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "already exists") || !strings.Contains(out, shortID(first.ID)) {
		t.Fatalf("second up output = %q, want the same box resumed", out)
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 1 || boxes[0].ID != first.ID {
		t.Fatalf("boxes after resume = %+v, want one box %s", boxes, first.ID)
	}
}

// A branch absent from origin is refused with the push hint, like the
// --repo path.
func TestUpContextBranchMissingFromOriginFailsWithAPushHint(t *testing.T) {
	managedHome(t)
	socket, _ := startDaemon(t)
	remote := managedRemote(t)
	contextEnv(t, remote, "cipher", "ghost")

	code, _, errOut := runCLI(t, "--socket", socket, "up")
	if code != 1 {
		t.Fatalf("up exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{`branch "ghost" does not exist on origin`, "next: push the branch with 'git push -u origin ghost'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

// A commit pushed after the managed clone was made is still on origin, so up
// succeeds; the head check fetches before deciding the branch is ahead.
func TestUpFromContextSeesACommitPushedAfterTheClone(t *testing.T) {
	managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t)
	contextEnv(t, remote, "cipher", "main")
	upFromContext(t, socket, st)

	clone := filepath.Join(t.TempDir(), "checkout")
	gitRun(t, "", "git", "clone", remote, clone)
	if err := os.WriteFile(filepath.Join(clone, "next.txt"), []byte("next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, clone, "git", "add", "next.txt")
	gitRun(t, clone, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "next")
	gitRun(t, clone, "git", "push", "origin", "main")
	head := strings.TrimSpace(gitOutCLI(t, clone, "rev-parse", "HEAD"))

	t.Setenv(workspace.ClientContextEnv, `{"project":"cipher","branch":"main","source":"`+remote+`","head":"`+head+`"}`)
	code, _, errOut := runCLI(t, "--socket", socket, "up")
	if code != 0 {
		t.Fatalf("up after push exit = %d, stderr: %s", code, errOut)
	}
}

// A relative --worktree is a custom host-side key for the client's project
// and branch, not a laptop path.
func TestUpWorktreeCustomKeyUsesTheClientProject(t *testing.T) {
	home := managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")
	contextEnv(t, remote, "cipher", "feature")

	code, out, errOut := runCLI(t, "--socket", socket, "up", "--worktree", "my-feature")
	if code != 0 {
		t.Fatalf("up --worktree exit = %d, stderr: %s", code, errOut)
	}
	want := filepath.Join(home, "worktrees", "my-feature")
	if !strings.Contains(out, "worktree: "+want) {
		t.Fatalf("up output = %q, want the custom host key %s", out, want)
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 1 || boxes[0].Project != "cipher" || boxes[0].Branch != "feature" || boxes[0].Worktree != want {
		t.Fatalf("boxes = %+v, want cipher/feature at %s", boxes, want)
	}
}

// An explicit box id, project@branch, managed key, or host path always wins
// over the client context.
func TestExplicitTargetWinsOverTheClientContext(t *testing.T) {
	managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature", "other")
	contextEnv(t, remote, "cipher", "feature")
	contextBox := upFromContext(t, socket, st)

	if code, _, errOut := runCLI(t, "--socket", socket, "up", "cipher@other"); code != 0 {
		t.Fatalf("up cipher@other exit = %d, stderr: %s", code, errOut)
	}
	boxes, _, _ := st.Boxes()
	var other *state.Box
	for _, b := range boxes {
		if b.Branch == "other" {
			other = b
		}
	}
	if other == nil {
		t.Fatalf("boxes = %+v, want a box for the other branch", boxes)
	}

	for _, target := range []string{other.ID, shortID(other.ID), "cipher@other", "cipher-other", other.Worktree} {
		code, out, errOut := runCLI(t, "--socket", socket, "status", target)
		if code != 0 {
			t.Fatalf("status %s exit = %d, stderr: %s", target, code, errOut)
		}
		if !strings.Contains(out, "id:       "+other.ID) || !strings.Contains(out, "branch:   other") {
			t.Fatalf("status %s = %q, want the explicit box %s", target, out, other.ID)
		}
	}

	// The context still names the original box: an explicit target did not
	// move it.
	code, out, errOut := runCLI(t, "--socket", socket, "status")
	if code != 0 || !strings.Contains(out, "id:       "+contextBox.ID) {
		t.Fatalf("status from context = (%d, %q, %q), want %s", code, out, errOut, contextBox.ID)
	}

	// --worktree is an absolute host path: it beats the context.
	hostRepo := gitRepo(t)
	code, out, errOut = runCLI(t, "--socket", socket, "up", "--worktree", hostRepo)
	if code != 0 {
		t.Fatalf("up --worktree exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "created box") {
		t.Fatalf("up --worktree output = %q, want a box for the host path", out)
	}
	boxes, _, _ = st.Boxes()
	found := false
	for _, b := range boxes {
		if b.Worktree == hostRepo {
			found = true
		}
	}
	if !found {
		t.Fatalf("boxes = %+v, want one for the explicit host worktree %s", boxes, hostRepo)
	}
}

// Outside a project checkout a no-target command fails and names the
// registered projects instead of guessing a last-used box.
func TestNoTargetOutsideACheckoutListsProjects(t *testing.T) {
	managedHome(t)
	socket, _ := startDaemon(t)
	remote := managedRemote(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "project", "add", remote, "--name", "cipher"); code != 0 {
		t.Fatalf("project add exit = %d, stderr: %s", code, errOut)
	}
	t.Chdir(t.TempDir())
	t.Setenv(workspace.ClientContextEnv, "")

	for _, cmd := range []string{"status", "pause", "jobs", "logs", "attach"} {
		code, _, errOut := runCLI(t, "--socket", socket, cmd)
		if code != 1 {
			t.Fatalf("%s without a target exit = %d, want 1 (stderr %q)", cmd, code, errOut)
		}
		for _, want := range []string{"pluto: no target", "projects: cipher", "next: cd into the project checkout"} {
			if !strings.Contains(errOut, want) {
				t.Fatalf("%s stderr = %q, want %q", cmd, errOut, want)
			}
		}
	}
}

// A checkout with no remote cannot name a host project: up says how to fix
// the checkout instead of guessing one.
func TestUpWithNoRemoteContextNamesTheNextStep(t *testing.T) {
	managedHome(t)
	socket, _ := startDaemon(t)
	t.Setenv(workspace.ClientContextEnv, `{"branch":"main","head":"0123456789abcdef","no_remote":true}`)

	code, _, errOut := runCLI(t, "--socket", socket, "up")
	if code != 1 {
		t.Fatalf("up exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"no usable git remote", "git remote add origin", "--worktree <host path>"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

// A local commit that is not on origin would make the box run older code, so
// up refuses with the push hint.
func TestUpLocalAheadFailsWithAPushHint(t *testing.T) {
	managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")

	// A laptop clone with one extra, unpushed commit on the branch.
	dir := t.TempDir()
	clone := filepath.Join(dir, "checkout")
	gitRun(t, "", "git", "clone", remote, clone)
	gitRun(t, clone, "git", "checkout", "feature")
	if err := os.WriteFile(filepath.Join(clone, "local.txt"), []byte("not pushed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, clone, "git", "add", "local.txt")
	gitRun(t, clone, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "local only")
	head := strings.TrimSpace(gitOutCLI(t, clone, "rev-parse", "HEAD"))

	t.Setenv(workspace.ClientContextEnv, `{"project":"cipher","branch":"feature","source":"`+remote+`","head":"`+head+`"}`)
	code, _, errOut := runCLI(t, "--socket", socket, "up")
	if code != 1 {
		t.Fatalf("up exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"commits that are not on origin", "next: push the branch with 'git push -u origin feature'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 0 {
		t.Fatalf("an unpushed branch created boxes: %+v", boxes)
	}
}
