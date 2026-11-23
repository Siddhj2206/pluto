package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/github"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

// gitRepo is a real git repository standing in for a repo on GitHub. HEAD stays
// on main, which is what a remote's default branch looks like to ls-remote.
type gitRepo struct {
	t   *testing.T
	dir string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	g := &gitRepo{t: t, dir: t.TempDir()}
	g.git(nil, "init", "--quiet", "-b", "main")
	return g
}

func (g *gitRepo) git(env []string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "user.email=a@example.com", "-c", "user.name=Test"}, args...)...)
	cmd.Dir = g.dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *gitRepo) hasBranch(branch string) bool {
	return exec.Command("git", "-C", g.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// commit writes files on a branch, which it creates from main if it's new, as
// author, and returns the new commit.
func (g *gitRepo) commit(branch, author string, files map[string]string) string {
	g.t.Helper()
	if g.hasBranch(branch) {
		g.git(nil, "checkout", "--quiet", branch)
	} else if g.hasBranch("main") {
		g.git(nil, "checkout", "--quiet", "-b", branch, "main")
	} else {
		g.git(nil, "checkout", "--quiet", "-b", branch)
	}
	for name, body := range files {
		path := filepath.Join(g.dir, filepath.FromSlash(name))
		must(g.t, os.MkdirAll(filepath.Dir(path), 0o755))
		must(g.t, os.WriteFile(path, []byte(body), 0o644))
	}
	g.git(nil, "add", "-A")
	g.git([]string{"GIT_AUTHOR_NAME=" + author}, "commit", "--quiet", "--allow-empty", "-m", "change")
	sha := g.git(nil, "rev-parse", "HEAD")
	if branch != "main" && g.hasBranch("main") {
		g.git(nil, "checkout", "--quiet", "main")
	}
	return sha
}

// pushTask is a task with the given secrets.
func pushTask(secrets ...string) task.Task {
	return task.Task{Name: "build", Run: []string{"make"}, Push: true, Timeout: time.Hour, Secrets: secrets}
}

func (g *gitRepo) deleteBranch(branch string) { g.git(nil, "branch", "--quiet", "-D", branch) }

const pushConfig = `tasks:
  build:
    run: [make]
    push: true
    secrets: [TOKEN]
  lint:
    run: [make lint]
    push: true
  docs:
    run: [make docs]
`

var pushFiles = map[string]string{".pluto/pluto.yml": pushConfig, "app.txt": "one"}

// newWatchHarness is a task harness whose test repo is a local git repository.
func newWatchHarness(t *testing.T) (*harness, *gitRepo) {
	t.Helper()
	x := newTaskHarness(t)
	g := newGitRepo(t)
	x.h.Config.WatchEvery = 20 * time.Second
	x.h.RepoURL = func(string) string { return "file://" + g.dir }
	return x, g
}

// check advances time past a watch interval and looks at the repo once.
func (x *harness) check() {
	x.t.Helper()
	x.now = x.now.Add(x.h.Config.WatchEvery)
	x.h.checkRepo(context.Background(), testRepo)
}

func (x *harness) allRuns() []store.Run {
	x.t.Helper()
	runs, err := x.h.Store.Runs(context.Background(), 100)
	must(x.t, err)
	return runs
}

// runsOf lists the runs of one task, oldest first.
func (x *harness) runsOf(job string) []store.Run {
	x.t.Helper()
	var out []store.Run
	for _, r := range x.allRuns() {
		if r.Job == job {
			out = append([]store.Run{r}, out...)
		}
	}
	return out
}

func (x *harness) watch() store.Watch {
	x.t.Helper()
	w, err := x.h.Store.Watch(context.Background(), testRepo)
	must(x.t, err)
	return w
}

func TestTheFirstCheckRecordsBranchesAndRunsNothing(t *testing.T) {
	x, g := newWatchHarness(t)
	g.commit("main", "Sam", pushFiles)
	side := g.commit("side", "Sam", pushFiles)
	x.check()
	if runs := x.allRuns(); len(runs) != 0 {
		t.Fatalf("the first check ran %+v", runs)
	}
	heads, err := x.h.Store.Heads(context.Background(), testRepo)
	must(t, err)
	if len(heads) != 2 || heads["side"] != side {
		t.Fatalf("heads = %v", heads)
	}
	if w := x.watch(); !w.Baselined || w.DefaultBranch != "main" || w.Problem != "" {
		t.Fatalf("watch = %+v", w)
	}
}

func TestACommitOnTheDefaultBranchQueuesOneRunPerPushTask(t *testing.T) {
	x, g := newWatchHarness(t)
	g.commit("main", "Sam", pushFiles)
	x.check()
	sha := g.commit("main", "Sam Author", map[string]string{"app.txt": "two"})
	x.check()
	runs := x.allRuns()
	if len(runs) != 2 { // build and lint, not docs.
		t.Fatalf("runs = %+v", runs)
	}
	for _, name := range []string{"build", "lint"} {
		rs := x.runsOf(name)
		if len(rs) != 1 {
			t.Fatalf("%s runs = %+v", name, rs)
		}
		r := rs[0]
		if r.State != store.StateQueued || r.Trigger != store.TriggerPush || r.Ref != "main" || r.SHA != sha || r.Actor != "Sam Author" || r.Kind != store.KindTask {
			t.Errorf("%s run = %+v", name, r)
		}
		if data, _ := json.Marshal(r); !strings.Contains(string(data), `"actor":"Sam Author"`) {
			t.Errorf("json = %s", data)
		}
	}
	// Looking again with nothing new queues nothing more.
	x.check()
	if got := len(x.allRuns()); got != 2 {
		t.Fatalf("%d runs after a quiet check", got)
	}
	if heads, _ := x.h.Store.Heads(context.Background(), testRepo); heads["main"] != sha {
		t.Fatalf("head = %q, want %q", heads["main"], sha)
	}
}

func TestACommitWithNoPushTasksQueuesNothing(t *testing.T) {
	x, g := newWatchHarness(t)
	g.commit("main", "Sam", map[string]string{".pluto/pluto.yml": "tasks:\n  docs:\n    run: [make]\n"})
	x.check()
	g.commit("main", "Sam", map[string]string{"app.txt": "two"})
	x.check()
	g.commit("other", "Sam", map[string]string{"app.txt": "three"})
	x.check()
	if runs := x.allRuns(); len(runs) != 0 {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestABrokenConfigIsRecordedAsAFailedConfigRun(t *testing.T) {
	x, g := newWatchHarness(t)
	g.commit("main", "Sam", pushFiles)
	x.check()
	sha := g.commit("main", "Sam", map[string]string{".pluto/pluto.yml": "tasks:\n  build:\n    run: [make]\n    retries: 3\n"})
	x.check()
	runs := x.allRuns()
	if len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	r := runs[0]
	if r.Job != "config" || r.State != store.StateFailed || r.Trigger != store.TriggerPush || r.Ref != "main" || r.SHA != sha || r.Actor != "Sam" || !strings.Contains(r.Reason, "retries") {
		t.Fatalf("run = %+v", r)
	}
	if heads, _ := x.h.Store.Heads(context.Background(), testRepo); heads["main"] != sha {
		t.Fatal("a broken config should still count as seen")
	}
	// Nothing runs it: it is already over.
	must(t, x.h.startQueued(context.Background()))
	x.h.prepares.Wait()
	if len(x.eng.boxes) != 0 {
		t.Fatal("a box was started for a config failure")
	}
}

func TestABotsSideBranchIsRefusedButItsDefaultBranchRuns(t *testing.T) {
	x, g := newWatchHarness(t)
	g.commit("main", "Sam", pushFiles)
	x.check()
	g.commit("bump", "dependabot[bot]", map[string]string{"app.txt": "bumped"})
	x.check()
	for _, name := range []string{"build", "lint"} {
		rs := x.runsOf(name)
		if len(rs) != 1 || rs[0].State != store.StateRefused || rs[0].Actor != "dependabot[bot]" || !strings.Contains(rs[0].Reason, "dependabot[bot]") || rs[0].Ref != "bump" {
			t.Fatalf("%s runs = %+v", name, rs)
		}
	}
	must(t, x.h.startQueued(context.Background()))
	x.h.prepares.Wait()
	if len(x.eng.boxes) != 0 {
		t.Fatal("a refused run started a box")
	}
	g.commit("main", "dependabot[bot]", map[string]string{"app.txt": "merged"})
	x.check()
	for _, name := range []string{"build", "lint"} {
		rs := x.runsOf(name)
		if len(rs) != 2 || rs[1].State != store.StateQueued || rs[1].Ref != "main" {
			t.Fatalf("%s runs = %+v", name, rs)
		}
	}
}

func TestANewerPushReplacesTheQueuedRunForItsBranchOnly(t *testing.T) {
	x, g := newWatchHarness(t)
	g.commit("main", "Sam", pushFiles)
	x.check()
	g.commit("main", "Sam", map[string]string{"app.txt": "1"})
	g.commit("side", "Sam", map[string]string{"app.txt": "2"})
	x.check()
	newest := g.commit("main", "Sam", map[string]string{"app.txt": "3"})
	x.check()
	builds := x.runsOf("build")
	if len(builds) != 3 {
		t.Fatalf("builds = %+v", builds)
	}
	byRef := map[string][]store.Run{}
	for _, r := range builds {
		byRef[r.Ref] = append(byRef[r.Ref], r)
	}
	main := byRef["main"]
	if len(main) != 2 || main[0].State != store.StateCancelled || main[0].Reason != "a newer push replaced it" || main[1].State != store.StateQueued || main[1].SHA != newest {
		t.Fatalf("main runs = %+v", main)
	}
	if side := byRef["side"]; len(side) != 1 || side[0].State != store.StateQueued {
		t.Fatalf("side runs = %+v", side)
	}
}

func TestARunThatHasStartedIsNotReplaced(t *testing.T) {
	x, g := newWatchHarness(t)
	g.commit("main", "Sam", pushFiles)
	x.check()
	g.commit("main", "Sam", map[string]string{"app.txt": "1"})
	x.check()
	for _, r := range x.runsOf("build") {
		r.State = store.StateRunning
		must(t, x.h.Store.PutRun(context.Background(), r))
	}
	g.commit("main", "Sam", map[string]string{"app.txt": "2"})
	x.check()
	builds := x.runsOf("build")
	if len(builds) != 2 || builds[0].State != store.StateRunning || builds[1].State != store.StateQueued {
		t.Fatalf("builds = %+v", builds)
	}
}

func TestADeletedBranchIsForgottenAndCountsAsNewIfItComesBack(t *testing.T) {
	x, g := newWatchHarness(t)
	g.commit("main", "Sam", pushFiles)
	g.commit("side", "Sam", map[string]string{"app.txt": "1"})
	x.check()
	g.deleteBranch("side")
	x.check()
	if heads, _ := x.h.Store.Heads(context.Background(), testRepo); len(heads) != 1 || heads["main"] == "" {
		t.Fatalf("heads = %v", heads)
	}
	if len(x.allRuns()) != 0 {
		t.Fatal("deleting a branch ran something")
	}
	g.commit("side", "Sam", map[string]string{"app.txt": "1"})
	x.check()
	if len(x.runsOf("build")) != 1 {
		t.Fatalf("runs = %+v", x.allRuns())
	}
}

func TestBranchesWithNamesPlutoWontUseAreSkipped(t *testing.T) {
	heads, def := parseHeads("ref: refs/heads/main\tHEAD\n" +
		"aaa\tHEAD\n" +
		"bbb\trefs/heads/main\n" +
		"ccc\trefs/heads/feature/x\n" +
		"ddd\trefs/heads/-bad\n" +
		"eee\trefs/heads/we ird\n" +
		"fff\trefs/tags/v1\n")
	if def != "main" || len(heads) != 2 || heads["main"] != "bbb" || heads["feature/x"] != "ccc" {
		t.Fatalf("heads = %v, default = %q", heads, def)
	}
	if _, def := parseHeads("aaa\trefs/heads/main\n"); def != "" {
		t.Fatalf("default = %q with no symref", def)
	}
}

func TestOnlyTheDefaultBranchGetsSecretsOnPush(t *testing.T) {
	x, g := newWatchHarness(t)
	ctx := context.Background()
	must(t, x.h.Store.SetSecret(ctx, testRepo, "TOKEN", "hunter2"))
	g.commit("main", "Sam", pushFiles)
	x.check()
	g.commit("main", "Sam", map[string]string{"app.txt": "1"})
	g.commit("side", "Sam", map[string]string{"app.txt": "2"})
	x.check()
	runOn := func(job, ref string) store.Run {
		for _, r := range x.runsOf(job) {
			if r.Ref == ref {
				return r
			}
		}
		t.Fatalf("no %s run on %s", job, ref)
		return store.Run{}
	}
	var plan Plan
	side := runOn("build", "side")
	must(t, json.Unmarshal([]byte(side.Spec), &plan))
	env, note, err := x.h.checkAllowed(ctx, side, plan)
	if err != nil || len(env) != 0 || note != "secrets withheld: only the default branch gets them on push" {
		t.Fatalf("side: env %v, note %q, err %v", env, note, err)
	}
	main := runOn("build", "main")
	env, note, err = x.h.checkAllowed(ctx, main, plan)
	if err != nil || env["TOKEN"] != "hunter2" || note != "" {
		t.Fatalf("main: env %v, note %q, err %v", env, note, err)
	}
	// A task that lists no secrets has nothing to withhold, so says nothing.
	lint := runOn("lint", "side")
	var lintPlan Plan
	must(t, json.Unmarshal([]byte(lint.Spec), &lintPlan))
	if _, note, err := x.h.checkAllowed(ctx, lint, lintPlan); err != nil || note != "" {
		t.Fatalf("lint: note %q, err %v", note, err)
	}
	// A side branch doesn't fail for a secret that isn't set.
	_, err = x.h.Store.DeleteSecret(ctx, testRepo, "TOKEN")
	must(t, err)
	if _, note, err := x.h.checkAllowed(ctx, side, plan); err != nil || note == "" {
		t.Fatalf("missing secret on a side branch: note %q, err %v", note, err)
	}
	if _, _, err := x.h.checkAllowed(ctx, main, plan); err == nil || !strings.Contains(err.Error(), "pluto secret set TOKEN") {
		t.Fatalf("missing secret on the default branch: %v", err)
	}
}

func TestSecretsAreWithheldWhenTheDefaultBranchIsUnknown(t *testing.T) {
	x := newTaskHarness(t)
	ctx := context.Background()
	must(t, x.h.Store.SetSecret(ctx, testRepo, "TOKEN", "hunter2"))
	plan := Plan{Task: pushTask("TOKEN")}
	r := store.Run{Repo: testRepo, Trigger: store.TriggerPush, Ref: "main"}
	if env, note, err := x.h.checkAllowed(ctx, r, plan); err != nil || len(env) != 0 || note == "" {
		t.Fatalf("no watch: env %v, note %q, err %v", env, note, err)
	}
	must(t, x.h.Store.PutWatch(ctx, store.Watch{Repo: testRepo, Baselined: true}))
	if env, _, err := x.h.checkAllowed(ctx, r, plan); err != nil || len(env) != 0 {
		t.Fatalf("empty default branch: env %v, err %v", env, err)
	}
	// Every other trigger gets them as before.
	for _, trigger := range []string{store.TriggerManual, store.TriggerSchedule, store.TriggerBranch} {
		r.Trigger = trigger
		if env, note, err := x.h.checkAllowed(ctx, r, plan); err != nil || env["TOKEN"] != "hunter2" || note != "" {
			t.Fatalf("%s: env %v, note %q, err %v", trigger, env, note, err)
		}
	}
}

func TestAPushRunPreparesFromThePushedCommitsTree(t *testing.T) {
	x, g := newWatchHarness(t)
	ctx := context.Background()
	must(t, x.h.Store.SetSecret(ctx, testRepo, "TOKEN", "hunter2"))
	g.commit("main", "Sam", pushFiles)
	x.check()
	sha := g.commit("main", "Sam", map[string]string{"app.txt": "pushed"})
	g.commit("main", "Sam", map[string]string{"app.txt": "later, and not pushed yet"})
	g.git(nil, "reset", "--quiet", "--hard", sha) // The remote's branch is at sha.
	x.check()
	must(t, x.h.startQueued(ctx))
	x.h.prepares.Wait()
	if len(x.eng.trees) == 0 || x.eng.trees[0]["app.txt"] != "pushed" {
		t.Fatalf("trees = %v", x.eng.trees)
	}
	var started bool
	for _, e := range x.eng.execs {
		if e.env["PLUTO_SHA"] == sha && e.env["PLUTO_REF"] == "main" {
			started = true
		}
	}
	if !started {
		t.Fatalf("no run started with the pushed commit: %+v", x.eng.execs)
	}
	// The repo's box runs one thing at a time, so the other push task waits.
	running := 0
	for _, r := range x.allRuns() {
		if r.State == store.StateRunning {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("%d runs started: %+v", running, x.allRuns())
	}
}

func TestAPushRunFetchesACommitTheBareCopyLost(t *testing.T) {
	x, g := newWatchHarness(t)
	ctx := context.Background()
	g.commit("main", "Sam", pushFiles)
	x.check()
	sha := g.commit("main", "Sam", map[string]string{"app.txt": "pushed"})
	x.check()
	must(t, os.RemoveAll(x.h.sourceDir(testRepo)))
	tree, err := x.h.archiveCommit(ctx, testRepo, sha)
	if err != nil {
		t.Fatal(err)
	}
	names, err := tarNames(tree)
	must(t, err)
	if !strings.Contains(strings.Join(names, " "), "app.txt") {
		t.Fatalf("names = %v", names)
	}
}

func TestAPushRunWhoseCommitIsGoneFailsSayingSo(t *testing.T) {
	x, g := newWatchHarness(t)
	ctx := context.Background()
	g.commit("main", "Sam", pushFiles)
	x.check()
	r, err := x.h.Submit(ctx, testRepo, Plan{Task: pushTask()}, Submission{Ref: "main", SHA: strings.Repeat("0123456789", 4), Trigger: store.TriggerPush})
	must(t, err)
	must(t, x.h.startQueued(ctx))
	x.h.prepares.Wait()
	got, err := x.h.Store.Run(ctx, r.ID)
	must(t, err)
	if got.State != store.StateFailed || !strings.Contains(got.Reason, "is gone") || !strings.Contains(got.Reason, "force-push") {
		t.Fatalf("run = %+v", got)
	}
}

func TestPushRunsAreWorkSomeoneWaitsOn(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for trigger, want := range map[string]int{
		store.TriggerSchedule: bandBackground,
		store.TriggerPush:     bandWaitedOn,
		store.TriggerManual:   bandWaitedOn,
		store.TriggerBranch:   bandWaitedOn,
	} {
		if got := band(store.Run{Trigger: trigger, CreatedAt: now}, now); got != want {
			t.Errorf("%s band = %d, want %d", trigger, got, want)
		}
	}
}

func TestAFailedCheckShowsAProblemAndBacksOff(t *testing.T) {
	x, g := newWatchHarness(t)
	ctx := context.Background()
	g.commit("main", "Sam", pushFiles)
	missing := filepath.Join(t.TempDir(), "nothing-here")
	x.h.RepoURL = func(string) string { return "file://" + missing }
	x.h.Config.MaxBackoff = 70 * time.Second
	x.check()
	if p := x.watch().Problem; !strings.Contains(p, "ls-remote") {
		t.Fatalf("problem = %q", p)
	}
	if st := x.h.Status(testRepo); !strings.Contains(st.WatchProblem, "ls-remote") || st.Problem != "" {
		t.Fatalf("status = %+v", st)
	}
	if x.watch().Baselined {
		t.Fatal("a failed first look counts as the baseline")
	}
	// The wait doubles: 20s, 40s, then the 70s cap.
	var waits []time.Duration
	for range 4 {
		x.h.mu.Lock()
		waits = append(waits, x.h.watchWait[testRepo].until.Sub(x.now))
		x.h.mu.Unlock()
		x.now = x.now.Add(waits[len(waits)-1])
		due, err := x.h.watchDue(ctx, testRepo)
		must(t, err)
		if !due {
			t.Fatal("not due once the wait is over")
		}
		x.h.checkRepo(ctx, testRepo)
	}
	if waits[0] != 20*time.Second || waits[1] != 40*time.Second || waits[2] != 70*time.Second || waits[3] != 70*time.Second {
		t.Fatalf("waits = %v", waits)
	}
	due, _ := x.h.watchDue(ctx, testRepo)
	if due {
		t.Fatal("due again straight after a failure")
	}
	// Once it works, the problem and the backoff are gone.
	x.h.RepoURL = func(string) string { return "file://" + g.dir }
	x.now = x.now.Add(5 * time.Minute)
	x.h.checkRepo(ctx, testRepo)
	if w := x.watch(); w.Problem != "" || !w.Baselined {
		t.Fatalf("watch = %+v", w)
	}
	if x.h.Status(testRepo).WatchProblem != "" {
		t.Fatal("the problem stayed after a good check")
	}
	x.h.mu.Lock()
	_, waiting := x.h.watchWait[testRepo]
	x.h.mu.Unlock()
	if waiting {
		t.Fatal("still backing off after a good check")
	}
}

func TestAPushThatCantBeFetchedIsRetriedOnTheNextCheck(t *testing.T) {
	x, g := newWatchHarness(t)
	ctx := context.Background()
	g.commit("main", "Sam", pushFiles)
	x.check()
	sha := g.commit("main", "Sam", map[string]string{"app.txt": "1"})
	// ls-remote works, but the bare copy is broken, so the fetch can't.
	must(t, os.MkdirAll(x.h.sourceDir(testRepo), 0o700))
	must(t, os.WriteFile(filepath.Join(x.h.sourceDir(testRepo), "HEAD"), []byte("not a git dir"), 0o600)) // Breaks the bare copy.
	x.check()
	if x.watch().Problem == "" {
		t.Fatal("no problem shown for a failed fetch")
	}
	if heads, _ := x.h.Store.Heads(ctx, testRepo); heads["main"] == sha {
		t.Fatal("the head moved although the push wasn't handled")
	}
	must(t, os.RemoveAll(x.h.sourceDir(testRepo)))
	x.now = x.now.Add(time.Hour)
	x.h.checkRepo(ctx, testRepo)
	if len(x.runsOf("build")) != 1 || x.watch().Problem != "" {
		t.Fatalf("runs = %+v, watch = %+v", x.allRuns(), x.watch())
	}
}

func TestAHintNamesTheTokenPermissionOnlyWhenATokenWasUsed(t *testing.T) {
	base := errors.New("git ls-remote: denied")
	if got := withContentsHint(base, ""); got != base {
		t.Fatalf("no token: %v", got)
	}
	if got := withContentsHint(base, "t"); !strings.Contains(got.Error(), "Contents: read") {
		t.Fatalf("token: %v", got)
	}
}

func TestThePushTokenOnlyGoesToTheReposOwnGitHubURL(t *testing.T) {
	x, _ := newWatchHarness(t)
	ctx := context.Background()
	x.h.RepoURL = nil
	if got := x.h.repoURL(testRepo); got != "https://github.com/o/r.git" {
		t.Fatalf("url = %q", got)
	}
	if x.h.tokenFor(ctx, testRepo, x.h.repoURL(testRepo)) != "t" {
		t.Fatal("the repo's own URL should get its token")
	}
	if x.h.tokenFor(ctx, testRepo, "file:///somewhere") != "" {
		t.Fatal("a local URL got the token")
	}
}

func TestWatchReposChecksRepoOnceWhenDue(t *testing.T) {
	x, g := newWatchHarness(t)
	ctx := context.Background()
	g.commit("main", "Sam", pushFiles)
	x.h.watchRepos(ctx)
	x.h.prepares.Wait()
	if !x.watch().Baselined {
		t.Fatal("the first watch didn't take the baseline")
	}
	g.commit("main", "Sam", map[string]string{"app.txt": "1"})
	x.h.watchRepos(ctx) // Not due yet.
	x.h.prepares.Wait()
	if len(x.allRuns()) != 0 {
		t.Fatal("looked again before the interval")
	}
	x.now = x.now.Add(x.h.Config.WatchEvery)
	x.h.watchRepos(ctx)
	x.h.prepares.Wait()
	if len(x.runsOf("build")) != 1 {
		t.Fatalf("runs = %+v", x.allRuns())
	}
	x.h.Config.WatchEvery = 0 // Off.
	g.commit("main", "Sam", map[string]string{"app.txt": "2"})
	x.now = x.now.Add(time.Hour)
	x.h.watchRepos(ctx)
	x.h.prepares.Wait()
	if len(x.runsOf("build")) != 1 {
		t.Fatal("watched while turned off")
	}
}

func TestPruneSourcesKeepsAConnectedReposCopy(t *testing.T) {
	x := newTaskHarness(t)
	ctx := context.Background()
	must(t, x.h.Store.PutRepo(ctx, store.Repo{Name: "o/other", Token: "t", ConnectedAt: x.now}))
	root := filepath.Join(x.h.StateDir, "sources")
	connected := filepath.Join(root, slug("o/other")+".git")
	stranger := filepath.Join(root, slug("x/gone")+".git")
	for _, d := range []string{connected, stranger} {
		must(t, os.MkdirAll(d, 0o700))
	}
	must(t, x.h.pruneSources(ctx))
	if _, err := os.Stat(connected); err != nil {
		t.Errorf("a connected repo's copy was removed: %v", err)
	}
	if _, err := os.Stat(stranger); err == nil {
		t.Error("an unconnected repo's copy was kept")
	}
}

// reportable stores a push run for the test repo.
func (x *harness) reportable(job, state string) store.Run {
	x.t.Helper()
	return x.putRun(store.Run{Job: job, State: state, Trigger: store.TriggerPush, Ref: "main", SHA: "abc123", CreatedAt: x.now})
}

func (x *harness) setState(id, state, reason string) {
	x.t.Helper()
	ctx := context.Background()
	r, err := x.h.Store.Run(ctx, id)
	must(x.t, err)
	r.State, r.Reason = state, reason
	if state != store.StateQueued && state != store.StateWaiting {
		r.StartedAt, r.FinishedAt = x.now.Add(-41*time.Second), x.now
	}
	must(x.t, x.h.Store.PutRun(ctx, r))
}

func TestStatusesGoOutAsPendingThenTheResult(t *testing.T) {
	x := newTaskHarness(t)
	ctx := context.Background()
	r := x.reportable("build", store.StateQueued)
	must(t, x.h.reportPushRuns(ctx))
	x.setState(r.ID, store.StateWaiting, "") // Reads the same on GitHub as queued... not quite: Running.
	must(t, x.h.reportPushRuns(ctx))
	x.setState(r.ID, store.StateRunning, "") // Reads the same as waiting.
	must(t, x.h.reportPushRuns(ctx))
	x.setState(r.ID, store.StatePassed, "")
	must(t, x.h.reportPushRuns(ctx))
	must(t, x.h.reportPushRuns(ctx)) // Nothing new.
	want := []statusCall{
		{testRepo, "abc123", "pending", "pluto/build", "Queued"},
		{testRepo, "abc123", "pending", "pluto/build", "Running"},
		{testRepo, "abc123", "success", "pluto/build", "Passed in 41s"},
	}
	if len(x.gh.statuses) != len(want) {
		t.Fatalf("statuses = %+v", x.gh.statuses)
	}
	for i := range want {
		if x.gh.statuses[i] != want[i] {
			t.Errorf("status %d = %+v, want %+v", i, x.gh.statuses[i], want[i])
		}
	}
	if got, _ := x.h.Store.Run(ctx, r.ID); got.Reported != store.StatePassed {
		t.Fatalf("reported = %q", got.Reported)
	}
}

func TestEveryStateMapsToAStatus(t *testing.T) {
	tests := []struct {
		state, reason, wantState, wantDesc string
	}{
		{store.StateQueued, "", "pending", "Queued"},
		{store.StateWaiting, "", "pending", "Running"},
		{store.StateRunning, "", "pending", "Running"},
		{store.StatePassed, "", "success", "Passed"},
		{store.StateFailed, "a step exited with code 2", "failure", "Failed: a step exited with code 2"},
		{store.StateFailed, "bad\nconfig:\n  line 3", "failure", "Failed: bad config: line 3"},
		{store.StateCancelled, "a newer push replaced it", "error", "Cancelled: a newer push replaced it"},
		{store.StateCancelled, "", "error", "Cancelled"},
		{store.StateLost, "the box disappeared", "error", "Lost: the box disappeared"},
		{store.StateRefused, "x is a bot", "error", "x is a bot"},
		{store.StateSkipped, "", "error", "Skipped"},
	}
	for _, tt := range tests {
		gotState, gotDesc := commitStatus(store.Run{State: tt.state, Reason: tt.reason})
		if gotState != tt.wantState || gotDesc != tt.wantDesc {
			t.Errorf("%s %q: %s %q, want %s %q", tt.state, tt.reason, gotState, gotDesc, tt.wantState, tt.wantDesc)
		}
	}
	for d, want := range map[time.Duration]string{41 * time.Second: "41s", 125 * time.Second: "2m05s", 3720 * time.Second: "1h02m"} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestAFailedRunAndTheConfigJobReportUnderTheirOwnNames(t *testing.T) {
	x := newTaskHarness(t)
	ctx := context.Background()
	x.reportable("config", store.StateFailed)
	b := x.reportable("build", store.StateRunning)
	x.setState(b.ID, store.StateFailed, "a step exited with code 2")
	must(t, x.h.reportPushRuns(ctx))
	got := map[string]statusCall{}
	for _, s := range x.gh.statuses {
		got[s.label] = s
	}
	if got["pluto/config"].state != "failure" || got["pluto/build"].state != "failure" || got["pluto/build"].description != "Failed: a step exited with code 2" {
		t.Fatalf("statuses = %+v", x.gh.statuses)
	}
}

func TestOnlyRecentPushRunsAreReported(t *testing.T) {
	x := newTaskHarness(t)
	ctx := context.Background()
	x.putRun(store.Run{Job: "old", State: store.StatePassed, Trigger: store.TriggerPush, SHA: "a", CreatedAt: x.now.Add(-8 * 24 * time.Hour)})
	x.putRun(store.Run{Job: "manual", State: store.StatePassed, Trigger: store.TriggerManual, SHA: "a"})
	must(t, x.h.reportPushRuns(ctx))
	if len(x.gh.statuses) != 0 {
		t.Fatalf("statuses = %+v", x.gh.statuses)
	}
}

func TestARefusedStatusIsNotedOnceAndNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound} {
		x := newTaskHarness(t)
		ctx := context.Background()
		x.gh.statusErr = &github.StatusError{Code: code, Path: "/statuses/abc123", Message: "Resource not accessible"}
		r := x.reportable("build", store.StateQueued)
		must(t, x.h.reportPushRuns(ctx))
		must(t, x.h.reportPushRuns(ctx))
		must(t, x.h.reportPushRuns(ctx))
		if x.gh.statusTry != 1 {
			t.Fatalf("%d: tried %d times", code, x.gh.statusTry)
		}
		got, _ := x.h.Store.Run(ctx, r.ID)
		note := needsStatusWrite
		if code == http.StatusNotFound {
			note = cantSeeCommit
		}
		if got.Reported != store.StateQueued || got.Note != note {
			t.Fatalf("%d: run = %+v", code, got)
		}
		// A later state is tried again, but the note isn't repeated.
		x.setState(r.ID, store.StatePassed, "")
		must(t, x.h.reportPushRuns(ctx))
		got, _ = x.h.Store.Run(ctx, r.ID)
		if x.gh.statusTry != 2 || got.Note != note || got.Reported != store.StatePassed {
			t.Fatalf("%d: tries %d, run = %+v", code, x.gh.statusTry, got)
		}
	}
}

func TestAnUnprocessableStatusNotesGitHubsWords(t *testing.T) {
	x := newTaskHarness(t)
	ctx := context.Background()
	x.gh.statusErr = &github.StatusError{Code: http.StatusUnprocessableEntity, Path: "/statuses/abc123", Message: "No commit found for SHA"}
	r := x.reportable("build", store.StateQueued)
	must(t, x.h.reportPushRuns(ctx))
	got, _ := x.h.Store.Run(ctx, r.ID)
	if !strings.HasPrefix(got.Note, "couldn't report to GitHub: ") || !strings.Contains(got.Note, "No commit found") || got.Reported != store.StateQueued {
		t.Fatalf("run = %+v", got)
	}
}

func TestATransientStatusErrorIsRetriedNoOftenerThanOnceAMinute(t *testing.T) {
	x := newTaskHarness(t)
	ctx := context.Background()
	x.gh.statusErr = errors.New("connection reset")
	r := x.reportable("build", store.StateQueued)
	if err := x.h.reportPushRuns(ctx); err == nil {
		t.Fatal("a failed send should be an error to log")
	}
	x.now = x.now.Add(30 * time.Second)
	must(t, x.h.reportPushRuns(ctx))
	if x.gh.statusTry != 1 {
		t.Fatalf("tried %d times within a minute", x.gh.statusTry)
	}
	x.now = x.now.Add(31 * time.Second)
	_ = x.h.reportPushRuns(ctx)
	if x.gh.statusTry != 2 {
		t.Fatalf("tried %d times after a minute", x.gh.statusTry)
	}
	if got, _ := x.h.Store.Run(ctx, r.ID); got.Reported != "" || got.Note != "" {
		t.Fatalf("run = %+v", got)
	}
	x.gh.statusErr = nil
	x.now = x.now.Add(time.Minute)
	must(t, x.h.reportPushRuns(ctx))
	if got, _ := x.h.Store.Run(ctx, r.ID); got.Reported != store.StateQueued || len(x.gh.statuses) != 1 {
		t.Fatalf("run = %+v, statuses = %+v", got, x.gh.statuses)
	}
}

func TestTheLoopReportsInTheBackgroundWithoutHoldingARunUp(t *testing.T) {
	x := newTaskHarness(t)
	x.gh.statusErr = errors.New("GitHub is down")
	r := x.reportable("build", store.StateQueued)
	x.h.startReporting(context.Background())
	x.h.prepares.Wait()
	// The run is where it was, whatever happened to its report.
	if got, _ := x.h.Store.Run(context.Background(), r.ID); got.State != store.StateQueued {
		t.Fatalf("run = %+v", got)
	}
}

func TestAPushLookedAtTwiceRunsOnce(t *testing.T) {
	x, g := newWatchHarness(t)
	ctx := context.Background()
	g.commit("main", "Sam", pushFiles)
	x.check()
	g.commit("main", "Sam", map[string]string{"app.txt": "two"})
	// The host stopped after queueing the runs and before saving the commit, so
	// the next look sees the same push again.
	for range 2 {
		if _, err := x.h.handlePush(ctx, testRepo, x.h.repoURL(testRepo), "main", "main"); err != nil {
			t.Fatal(err)
		}
	}
	x.check()
	for _, name := range []string{"build", "lint"} {
		if rs := x.runsOf(name); len(rs) != 1 || rs[0].State != store.StateQueued {
			t.Fatalf("%s runs = %+v", name, rs)
		}
	}
}

func TestARefusedRunPutsNoStatusOnTheCommit(t *testing.T) {
	x := newTaskHarness(t)
	ctx := context.Background()
	r := x.reportable("build", store.StateRefused)
	must(t, x.h.reportPushRuns(ctx))
	must(t, x.h.reportPushRuns(ctx))
	if len(x.gh.statuses) != 0 {
		t.Fatalf("statuses = %+v", x.gh.statuses)
	}
	if got, _ := x.h.Store.Run(ctx, r.ID); got.Reported != store.StateRefused {
		t.Fatalf("reported = %q", got.Reported)
	}
}
