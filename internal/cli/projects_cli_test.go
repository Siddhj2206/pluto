package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Siddhj2206/pluto/internal/workspace"
)

// managedRemote creates a bare remote whose default branch is main, with one
// commit, plus any extra branches. It returns the remote path.
func managedRemote(t *testing.T, branches ...string) string {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	gitRun(t, "", "git", "init", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(dir, "seed")
	gitRun(t, "", "git", "clone", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "git", "add", "README.md")
	gitRun(t, seed, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	gitRun(t, seed, "git", "push", "origin", "main")
	for _, branch := range branches {
		gitRun(t, seed, "git", "checkout", "-B", branch, "main")
		if err := os.WriteFile(filepath.Join(seed, "branch.txt"), []byte(branch+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, seed, "git", "add", "branch.txt")
		gitRun(t, seed, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", branch)
		gitRun(t, seed, "git", "push", "origin", branch)
	}
	return remote
}

func gitRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v (%s)", name, strings.Join(args, " "), err, out)
	}
}

func managedHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("PLUTO_HOME", root)
	return root
}

func TestProjectAddLsAndShow(t *testing.T) {
	home := managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")

	code, out, errOut := runCLI(t, "--socket", socket, "project", "add", remote, "--name", "cipher")
	if code != 0 {
		t.Fatalf("project add exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"project cipher added", "default branch: main", "clone:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("project add output = %q, want %q", out, want)
		}
	}
	clone := filepath.Join(home, "repos", "cipher")
	if _, err := os.Stat(filepath.Join(clone, ".git")); err != nil {
		t.Fatalf("managed clone missing: %v", err)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "project", "add", remote, "--name", "cipher")
	if code != 0 || !strings.Contains(out, "already registered") {
		t.Fatalf("second project add = (%d, %q, %q)", code, out, errOut)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "project", "ls")
	if code != 0 {
		t.Fatalf("project ls exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"NAME", "cipher", remote, "main"} {
		if !strings.Contains(out, want) {
			t.Fatalf("project ls output = %q, want %q", out, want)
		}
	}

	// Create a branch box so show has a worktree and box to report.
	code, _, errOut = runCLI(t, "--socket", socket, "up", "cipher", "--branch", "feature")
	if code != 0 {
		t.Fatalf("up project branch exit = %d, stderr: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err %v", len(boxes), err)
	}
	box := boxes[0]

	code, out, errOut = runCLI(t, "--socket", socket, "project", "show", "cipher")
	if code != 0 {
		t.Fatalf("project show exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"name:           cipher", "source:", "default branch: main", "clone:", "worktrees:", "cipher-feature", "boxes:", shortID(box.ID)} {
		if !strings.Contains(out, want) {
			t.Fatalf("project show output = %q, want %q", out, want)
		}
	}

	code, out, errOut = runCLI(t, "--socket", socket, "worktree", "ls", "cipher")
	if code != 0 {
		t.Fatalf("worktree ls exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"KEY", "cipher-feature", "feature", shortID(box.ID)} {
		if !strings.Contains(out, want) {
			t.Fatalf("worktree ls output = %q, want %q", out, want)
		}
	}
}

func TestUpRepoCreatesAndResumesTheSameManagedBox(t *testing.T) {
	home := managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "demo/pluto")

	code, out, errOut := runCLI(t, "--socket", socket, "up", "--repo", remote, "--branch", "demo/pluto")
	if code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	worktree := filepath.Join(home, "worktrees", "remote-demo-pluto")
	if !strings.Contains(out, "created box") || !strings.Contains(out, "worktree: "+worktree) {
		t.Fatalf("up output = %q, want created box and worktree %s", out, worktree)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		t.Fatalf("managed worktree missing: %v", err)
	}
	if got := strings.TrimSpace(gitOutCLI(t, worktree, "branch", "--show-current")); got != "demo/pluto" {
		t.Fatalf("worktree branch = %q", got)
	}

	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err %v", len(boxes), err)
	}
	first := boxes[0]
	if first.Project != "remote" || first.Branch != "demo/pluto" || first.Worktree != worktree {
		t.Fatalf("box = %+v, want remote/demo/pluto at %s", first, worktree)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "up", "--repo", remote, "--branch", "demo/pluto")
	if code != 0 {
		t.Fatalf("second up exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "already exists") || !strings.Contains(out, shortID(first.ID)) {
		t.Fatalf("second up output = %q, want the same box resumed", out)
	}
	boxes, _, err = st.Boxes()
	if err != nil || len(boxes) != 1 || boxes[0].ID != first.ID {
		t.Fatalf("boxes after resume = %+v, err %v", boxes, err)
	}
}

func TestUpProjectUsesTheDefaultBranchCloneAndAnExactKey(t *testing.T) {
	home := managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")
	if code, _, errOut := runCLI(t, "--socket", socket, "project", "add", remote, "--name", "cipher"); code != 0 {
		t.Fatalf("project add exit = %d, stderr: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "up", "cipher")
	if code != 0 {
		t.Fatalf("up default exit = %d, stderr: %s", code, errOut)
	}
	clone := filepath.Join(home, "repos", "cipher")
	if !strings.Contains(out, "worktree: "+clone) {
		t.Fatalf("up default output = %q, want the managed clone", out)
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 1 || boxes[0].Worktree != clone || boxes[0].Branch != "main" {
		t.Fatalf("default boxes = %+v", boxes)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "up", "cipher@feature")
	if code != 0 {
		t.Fatalf("up exact key exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, filepath.Join(home, "worktrees", "cipher-feature")) {
		t.Fatalf("exact key output = %q", out)
	}
}

func TestUpMissingRemoteBranchIsAClearError(t *testing.T) {
	managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t)

	code, _, errOut := runCLI(t, "--socket", socket, "up", "--repo", remote, "--branch", "feature")
	if code != 1 {
		t.Fatalf("up missing branch exit = %d, want 1", code)
	}
	for _, want := range []string{`branch "feature" does not exist on origin`, "next: push the branch with 'git push -u origin feature'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 0 {
		t.Fatalf("missing branch created boxes: %+v", boxes)
	}
}

func TestUpFailsLoudlyWhenABoxWorktreeIsMissing(t *testing.T) {
	home := managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")

	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--repo", remote, "--branch", "feature"); code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 1 {
		t.Fatalf("boxes = %d", len(boxes))
	}
	box := boxes[0]
	if err := os.RemoveAll(box.Worktree); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runCLI(t, "--socket", socket, "up", "--repo", remote, "--branch", "feature")
	if code != 1 {
		t.Fatalf("up with missing worktree exit = %d, want 1", code)
	}
	for _, want := range []string{"which is missing", shortID(box.ID), "pluto worktree ls"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
	if _, err := os.Stat(box.Worktree); !os.IsNotExist(err) {
		t.Fatalf("missing worktree was silently recreated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "worktrees", "remote-feature")); !os.IsNotExist(err) {
		t.Fatalf("a replacement worktree was created: %v", err)
	}
}

func TestWorktreeRmRefusesLiveBoxAndDirtyCheckout(t *testing.T) {
	managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--repo", remote, "--branch", "feature"); code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 1 {
		t.Fatalf("boxes = %d", len(boxes))
	}
	box := boxes[0]

	code, _, errOut := runCLI(t, "--socket", socket, "worktree", "rm", "remote-feature")
	if code != 1 {
		t.Fatalf("worktree rm with box exit = %d, want 1", code)
	}
	for _, want := range []string{"has box " + shortID(box.ID), "pluto destroy " + shortID(box.ID)} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "destroy", box.ID, "--yes"); code != 0 {
		t.Fatalf("destroy exit = %d, stderr: %s", code, errOut)
	}
	if err := os.WriteFile(filepath.Join(box.Worktree, "scratch.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runCLI(t, "--socket", socket, "worktree", "rm", "remote-feature")
	if code != 1 || !strings.Contains(errOut, "uncommitted changes") {
		t.Fatalf("dirty worktree rm = (%d, %q), want a dirty refusal", code, errOut)
	}
	code, out, errOut := runCLI(t, "--socket", socket, "worktree", "rm", "remote-feature", "--force")
	if code != 0 {
		t.Fatalf("forced worktree rm exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "worktree remote-feature removed") {
		t.Fatalf("forced rm output = %q", out)
	}
	if _, err := os.Stat(box.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

func TestProjectRmRefusesBoxesAndDirtyCheckouts(t *testing.T) {
	managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--repo", remote, "--branch", "feature"); code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 1 {
		t.Fatalf("boxes = %d", len(boxes))
	}
	box := boxes[0]

	code, _, errOut := runCLI(t, "--socket", socket, "project", "rm", "remote")
	if code != 1 || !strings.Contains(errOut, "box(es)") {
		t.Fatalf("project rm with box = (%d, %q), want a box refusal", code, errOut)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "destroy", box.ID, "--yes"); code != 0 {
		t.Fatalf("destroy exit = %d, stderr: %s", code, errOut)
	}
	if err := os.WriteFile(filepath.Join(box.Worktree, "scratch.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runCLI(t, "--socket", socket, "project", "rm", "remote")
	if code != 1 || !strings.Contains(errOut, "uncommitted changes") {
		t.Fatalf("dirty project rm = (%d, %q), want a dirty refusal", code, errOut)
	}
	code, out, errOut := runCLI(t, "--socket", socket, "project", "rm", "remote", "--force")
	if code != 0 {
		t.Fatalf("forced project rm exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "project remote removed") {
		t.Fatalf("forced project rm output = %q", out)
	}
	w := workspace.Default()
	if _, err := w.Project("remote"); err == nil {
		t.Fatal("project still registered")
	}
}

func TestUpUsesTheClientContextWhenNoTargetIsGiven(t *testing.T) {
	home := managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")
	t.Setenv(workspace.ClientContextEnv, `{"project":"cipher","branch":"feature","source":"`+remote+`","remote":"origin"}`)

	code, out, errOut := runCLI(t, "--socket", socket, "up")
	if code != 0 {
		t.Fatalf("up with context exit = %d, stderr: %s", code, errOut)
	}
	want := filepath.Join(home, "worktrees", "cipher-feature")
	if !strings.Contains(out, "worktree: "+want) {
		t.Fatalf("up output = %q, want %s", out, want)
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 1 || boxes[0].Project != "cipher" || boxes[0].Branch != "feature" {
		t.Fatalf("context boxes = %+v", boxes)
	}
}

func TestConcurrentUpCallsDoNotRace(t *testing.T) {
	managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")

	const n = 6
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, errOut := runCLI(t, "--socket", socket, "up", "--repo", remote, "--branch", "feature")
			if code != 0 {
				errs <- errOut
			}
		}()
	}
	wg.Wait()
	close(errs)
	for errOut := range errs {
		t.Fatalf("concurrent up failed: %s", errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err %v, want one box", len(boxes), err)
	}
	w := workspace.Default()
	list, err := w.Worktrees("remote")
	if err != nil {
		t.Fatal(err)
	}
	linked := 0
	for _, wt := range list {
		if !wt.Default {
			linked++
		}
	}
	if linked != 1 {
		t.Fatalf("linked worktrees = %d, want one", linked)
	}
}

func TestUpWorktreeFlagOverridesTheManagedKey(t *testing.T) {
	home := managedHome(t)
	socket, st := startDaemon(t)
	remote := managedRemote(t, "feature")
	if code, _, errOut := runCLI(t, "--socket", socket, "project", "add", remote, "--name", "cipher"); code != 0 {
		t.Fatalf("project add exit = %d, stderr: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "up", "cipher", "--branch", "feature", "--worktree", "my-feature")
	if code != 0 {
		t.Fatalf("up with custom key exit = %d, stderr: %s", code, errOut)
	}
	want := filepath.Join(home, "worktrees", "my-feature")
	if !strings.Contains(out, "worktree: "+want) {
		t.Fatalf("up output = %q, want %s", out, want)
	}
	if _, err := os.Stat(filepath.Join(want, ".git")); err != nil {
		t.Fatalf("custom worktree missing: %v", err)
	}
	boxes, _, _ := st.Boxes()
	if len(boxes) != 1 || boxes[0].Branch != "feature" || boxes[0].Worktree != want {
		t.Fatalf("custom key boxes = %+v", boxes)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "worktree", "ls", "cipher")
	if code != 0 || !strings.Contains(out, "my-feature") {
		t.Fatalf("worktree ls = (%d, %q, %q), want the custom key", code, out, errOut)
	}
}

func TestProjectLsJSON(t *testing.T) {
	managedHome(t)
	socket, _ := startDaemon(t)
	remote := managedRemote(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "project", "add", remote, "--name", "cipher"); code != 0 {
		t.Fatalf("project add exit = %d, stderr: %s", code, errOut)
	}
	code, out, errOut := runCLI(t, "--socket", socket, "project", "ls", "--json")
	if code != 0 {
		t.Fatalf("project ls --json exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{`"schema_version":1`, `"name":"cipher"`, `"default_branch":"main"`, `"repo":`, `"worktrees":`} {
		if !strings.Contains(out, want) {
			t.Fatalf("json = %q, want %q", out, want)
		}
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func gitOutCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
