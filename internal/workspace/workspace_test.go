package workspace_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Siddhj2206/pluto/internal/workspace"
)

// seedRemote creates a bare repository whose default branch is main, with one
// commit, plus any extra branches pushed to origin.
func seedRemote(t *testing.T, branches ...string) string {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	run(t, "", "git", "init", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(dir, "seed")
	run(t, "", "git", "clone", remote, seed)
	writeFile(t, filepath.Join(seed, "README.md"), "hello\n")
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	run(t, seed, "git", "push", "origin", "main")
	for _, branch := range branches {
		run(t, seed, "git", "checkout", "-B", branch, "main")
		writeFile(t, filepath.Join(seed, "branch.txt"), branch+"\n")
		run(t, seed, "git", "add", "branch.txt")
		run(t, seed, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", branch)
		run(t, seed, "git", "push", "origin", branch)
	}
	return remote
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v (%s)", name, strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	root := t.TempDir()
	t.Setenv("PLUTO_HOME", root)
	return workspace.Default()
}

func TestEnsureProjectClonesOnceAndDetectsDefaultBranch(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t)

	p, created, err := w.EnsureProject(remote, "", "")
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if !created {
		t.Fatal("first EnsureProject should create")
	}
	if p.Name != "remote" || p.DefaultBranch != "main" {
		t.Fatalf("project = %+v, want name remote on main", p)
	}
	if p.Repo != w.RepoPath("remote") {
		t.Fatalf("repo = %q, want %q", p.Repo, w.RepoPath("remote"))
	}
	if _, err := os.Stat(filepath.Join(p.Repo, ".git")); err != nil {
		t.Fatalf("managed clone missing: %v", err)
	}
	if _, err := os.Stat(w.ProjectsPath()); err != nil {
		t.Fatalf("registry missing: %v", err)
	}
	registry, err := os.ReadFile(w.ProjectsPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[project.remote]", `source = "`, `default_branch = "main"`, `repo = "`, "created_at = "} {
		if !strings.Contains(string(registry), want) {
			t.Fatalf("projects.toml = %q, want %q", registry, want)
		}
	}

	again, created, err := w.EnsureProject(remote, "", "")
	if err != nil {
		t.Fatalf("second EnsureProject: %v", err)
	}
	if created || again.Name != p.Name {
		t.Fatalf("second EnsureProject = %+v created=%v, want the same project", again, created)
	}

	projects, err := w.Projects()
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 1 || projects[0].Source != remote {
		t.Fatalf("projects = %+v", projects)
	}
}

func TestEnsureProjectRefusesAChangedSource(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t)
	other := seedRemote(t)
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.EnsureProject(other, "cipher", ""); err == nil {
		t.Fatal("registering a different source under the same name should fail")
	}
}

func TestEnsureWorktreeCreatesAPredictableTrackingBranch(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t, "demo/pluto")
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}

	wt, err := w.EnsureWorktree("cipher", "demo/pluto", nil)
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	if wt.Key != "cipher-demo-pluto" {
		t.Fatalf("key = %q, want cipher-demo-pluto", wt.Key)
	}
	if wt.Path != filepath.Join(w.WorktreesPath(), "cipher-demo-pluto") {
		t.Fatalf("path = %q, want under %q", wt.Path, w.WorktreesPath())
	}
	if !wt.Created {
		t.Fatal("first EnsureWorktree should create")
	}
	if got := strings.TrimSpace(gitOut(t, wt.Path, "branch", "--show-current")); got != "demo/pluto" {
		t.Fatalf("branch = %q", got)
	}
	if got := strings.TrimSpace(gitOut(t, wt.Path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")); got != "origin/demo/pluto" {
		t.Fatalf("upstream = %q, want origin/demo/pluto", got)
	}

	again, err := w.EnsureWorktree("cipher", "demo/pluto", nil)
	if err != nil {
		t.Fatalf("second EnsureWorktree: %v", err)
	}
	if again.Path != wt.Path || again.Created {
		t.Fatalf("second = %+v, want the same reused worktree", again)
	}
}

func TestEnsureWorktreeUsesTheCloneForTheDefaultBranch(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t)
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}
	wt, err := w.EnsureWorktree("cipher", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !wt.Default || wt.Path != w.RepoPath("cipher") {
		t.Fatalf("default worktree = %+v, want the managed clone", wt)
	}
}

func TestEnsureWorktreeRejectsAMissingRemoteBranch(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t)
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}
	_, err := w.EnsureWorktree("cipher", "feature", nil)
	if err == nil {
		t.Fatal("a branch absent from origin should fail")
	}
	if !strings.Contains(err.Error(), `branch "feature" does not exist on origin`) {
		t.Fatalf("error = %v, want a clear missing-branch message", err)
	}
}

func TestEnsureWorktreeAcceptsTheExactKeyForm(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t, "feature")
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.EnsureWorktree("cipher", "feature", nil); err != nil {
		t.Fatal(err)
	}
	wt, err := w.FindWorktree("cipher@feature")
	if err != nil {
		t.Fatalf("FindWorktree exact form: %v", err)
	}
	if wt.Key != "cipher-feature" {
		t.Fatalf("key = %q", wt.Key)
	}
	byKey, err := w.FindWorktree("cipher-feature")
	if err != nil || byKey.Path != wt.Path {
		t.Fatalf("short key = %+v, err %v", byKey, err)
	}
}

func TestEnsureWorktreeDisambiguatesSanitizedKeyCollisions(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t, "demo/pluto", "demo-pluto")
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}
	first, err := w.EnsureWorktree("cipher", "demo/pluto", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.EnsureWorktree("cipher", "demo-pluto", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Key == second.Key {
		t.Fatalf("colliding branches share key %q", first.Key)
	}
	if !strings.HasPrefix(second.Key, "cipher-demo-pluto-") {
		t.Fatalf("second key = %q, want a hash suffix", second.Key)
	}
	if first.Path == second.Path {
		t.Fatalf("colliding branches share path %q", first.Path)
	}
}

func TestConcurrentEnsureWorktreeIsSerialized(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t, "feature")
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}
	const n = 8
	paths := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wt, err := w.EnsureWorktree("cipher", "feature", nil)
			if err != nil {
				errs[i] = err
				return
			}
			paths[i] = wt.Path
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		if paths[i] != paths[0] {
			t.Fatalf("worker %d path = %q, want %q", i, paths[i], paths[0])
		}
	}
	list, err := w.Worktrees("cipher")
	if err != nil {
		t.Fatal(err)
	}
	var linked int
	for _, wt := range list {
		if !wt.Default {
			linked++
		}
	}
	if linked != 1 {
		t.Fatalf("linked worktrees = %d, want 1", linked)
	}
}

func TestManagedWorktreeCopiesOnlyCommittedRefs(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t, "feature")
	p, _, err := w.EnsureProject(remote, "cipher", "")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(p.Repo, "uncommitted.txt"), "not copied\n")
	wt, err := w.EnsureWorktree("cipher", "feature", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "uncommitted.txt")); !os.IsNotExist(err) {
		t.Fatalf("uncommitted file leaked into the managed worktree: %v", err)
	}
}

func TestRemoveWorktreeRefusesDirtyAndDefaultCheckouts(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t, "feature")
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}
	wt, err := w.EnsureWorktree("cipher", "feature", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.RemoveWorktree("cipher", false); err == nil {
		t.Fatal("removing the default clone should fail")
	}
	writeFile(t, filepath.Join(wt.Path, "scratch.txt"), "uncommitted\n")
	if _, err := w.RemoveWorktree(wt.Key, false); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty removal error = %v, want uncommitted refusal", err)
	}
	if _, err := w.RemoveWorktree(wt.Key, true); err != nil {
		t.Fatalf("forced removal: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

func TestRemoveProjectRefusesDirtyAndRemovesForced(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t, "feature")
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}
	wt, err := w.EnsureWorktree("cipher", "feature", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt.Path, "scratch.txt"), "uncommitted\n")
	if err := w.RemoveProject("cipher", false); err == nil {
		t.Fatal("dirty project removal should fail")
	}
	if err := w.RemoveProject("cipher", true); err != nil {
		t.Fatalf("forced project removal: %v", err)
	}
	if _, err := w.Project("cipher"); err == nil {
		t.Fatal("project should be gone from the registry")
	}
	if _, err := os.Stat(w.RepoPath("cipher")); !os.IsNotExist(err) {
		t.Fatalf("managed clone still exists: %v", err)
	}
}

func TestRemoveProjectRecoversAMissingClone(t *testing.T) {
	w := newWorkspace(t)
	remote := seedRemote(t)
	if _, _, err := w.EnsureProject(remote, "cipher", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(w.RepoPath("cipher")); err != nil {
		t.Fatal(err)
	}
	if err := w.RemoveProject("cipher", false); err != nil {
		t.Fatalf("RemoveProject with a missing clone: %v", err)
	}
	if _, err := w.Project("cipher"); err == nil {
		t.Fatal("project should be gone from the registry")
	}
}

func TestParseClientContext(t *testing.T) {
	ctx, err := workspace.ParseClientContext(`{"project":"cipher","branch":"demo/pluto","source":"https://example/cipher.git"}`)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Project != "cipher" || ctx.Branch != "demo/pluto" {
		t.Fatalf("context = %+v", ctx)
	}
	if _, err := workspace.ParseClientContext(`{"branch":"main"}`); err == nil {
		t.Fatal("a context without project or source should fail")
	}
	if ctx, err := workspace.ParseClientContext(""); err != nil || ctx != nil {
		t.Fatalf("empty context = %+v, %v", ctx, err)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
