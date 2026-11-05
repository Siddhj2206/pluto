package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/engine"
	"github.com/Siddhj2206/pluto/internal/github"
	"github.com/Siddhj2206/pluto/internal/store"
)

type fakeBox struct {
	running bool
	status  string
	spec    engine.Spec
}

type fakeEngine struct {
	boxes    map[string]*fakeBox
	startErr error
	removed  []string
	noImage  atomic.Bool
	pulls    atomic.Int32
	pullErr  error
}

func (f *fakeEngine) Name() string                            { return "fake" }
func (f *fakeEngine) Version(context.Context) (string, error) { return "1", nil }
func (f *fakeEngine) HasImage(context.Context, string) (bool, error) {
	return !f.noImage.Load(), nil
}
func (f *fakeEngine) Pull(context.Context, string) error {
	f.pulls.Add(1)
	if f.pullErr != nil {
		return f.pullErr
	}
	f.noImage.Store(false)
	return nil
}
func (f *fakeEngine) EnterArgv(name, dir string) []string { return []string{"enter", name, dir} }
func (f *fakeEngine) Start(_ context.Context, s engine.Spec) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.boxes[s.Name] = &fakeBox{running: true, spec: s}
	return nil
}
func (f *fakeEngine) Running(_ context.Context, name string) (bool, error) {
	b, ok := f.boxes[name]
	if !ok {
		return false, engine.ErrNotFound
	}
	return b.running, nil
}
func (f *fakeEngine) Exec(_ context.Context, name string, _ ...string) ([]byte, error) {
	b, ok := f.boxes[name]
	if !ok {
		return nil, engine.ErrNotFound
	}
	return []byte(b.status), nil
}
func (f *fakeEngine) Resume(_ context.Context, name string) error {
	f.boxes[name].running = true
	return nil
}
func (f *fakeEngine) Remove(_ context.Context, name string) error {
	delete(f.boxes, name)
	f.removed = append(f.removed, name)
	return nil
}
func (f *fakeEngine) List(context.Context) ([]string, error) {
	var out []string
	for n := range f.boxes {
		out = append(out, n)
	}
	return out, nil
}

type fakeGitHub struct {
	jobs     []github.Job
	created  []string
	labels   [][]string
	deleted  []int64
	nextID   int64
	jobsErr  error
	adminErr error
	log      []byte
	runners  []github.Runner
	busy     bool
}

func (g *fakeGitHub) Runners(context.Context, string) ([]github.Runner, error) {
	return g.runners, nil
}
func (g *fakeGitHub) RunnerBusy(context.Context, string, int64) (bool, error) {
	return g.busy, nil
}

func (g *fakeGitHub) CheckAdmin(context.Context, string) error { return g.adminErr }
func (g *fakeGitHub) QueuedJobs(context.Context, string) ([]github.Job, error) {
	return g.jobs, g.jobsErr
}
func (g *fakeGitHub) CreateRunner(_ context.Context, _, name string, labels []string) (int64, string, error) {
	g.nextID++
	g.created = append(g.created, name)
	g.labels = append(g.labels, labels)
	return g.nextID, "secret-jit", nil
}
func (g *fakeGitHub) DeleteRunner(_ context.Context, _ string, id int64) error {
	g.deleted = append(g.deleted, id)
	return nil
}
func (g *fakeGitHub) JobLog(context.Context, string, int64, string) ([]byte, error) {
	return g.log, nil
}

type harness struct {
	t   *testing.T
	h   *Host
	eng *fakeEngine
	gh  *fakeGitHub
	now time.Time
}

const testRepo = "o/r"

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	x := &harness{t: t, eng: &fakeEngine{boxes: map[string]*fakeBox{}}, gh: &fakeGitHub{}, now: time.Unix(1_800_000_000, 0)}
	cfg := DefaultConfig()
	x.h = &Host{
		Store:  db,
		Engine: x.eng,
		GitHub: func(string) github.API { return x.gh },
		Config: cfg,
		Now:    func() time.Time { return x.now },
	}
	if err := db.PutRepo(context.Background(), store.Repo{Name: testRepo, Token: "t", ConnectedAt: x.now}); err != nil {
		t.Fatal(err)
	}
	return x
}

// tick advances time past the poll interval and runs one loop.
func (x *harness) tick() {
	x.t.Helper()
	x.now = x.now.Add(x.h.Config.PollEvery)
	if err := x.h.Tick(context.Background()); err != nil {
		x.t.Fatalf("tick: %v", err)
	}
}

func (x *harness) queueJob() {
	x.gh.jobs = []github.Job{{ID: 1, RunID: 9, Name: "check", Status: "queued", Labels: []string{"self-hosted", "pluto", "linux"}}}
}

func (x *harness) onlyRun() store.Run {
	x.t.Helper()
	runs, err := x.h.Store.Runs(context.Background(), 100)
	if err != nil {
		x.t.Fatal(err)
	}
	if len(runs) != 1 {
		x.t.Fatalf("want one visible run, got %d: %+v", len(runs), runs)
	}
	return runs[0]
}

// startJob gets a box for a queued job, and has that job reach it.
func (x *harness) startJob() store.Run {
	x.t.Helper()
	x.queueJob()
	x.tick()
	x.gh.jobs = nil
	r := x.onlyRun()
	x.eng.boxes[r.Box].status = "github_run_id=9\nworkflow=ci\njob=check\nevent=push\nref=refs/heads/m\nsha=abc\nworkspace=/w\n"
	x.tick()
	return x.onlyRun()
}

func (x *harness) finishJob(r store.Run, extra string) store.Run {
	x.t.Helper()
	x.eng.boxes[r.Box].status += "exit=0\n" + extra
	x.tick()
	return x.onlyRun()
}

func TestAQueuedJobGetsABoxWithAOneTimeRunner(t *testing.T) {
	x := newHarness(t)
	x.queueJob()
	x.tick()
	r := x.onlyRun()
	if r.State != store.StateWaiting {
		t.Errorf("state = %s", r.State)
	}
	b := x.eng.boxes[r.Box]
	if b == nil || b.spec.Env["PLUTO_JIT"] != "secret-jit" || b.spec.Image != runnerImage {
		t.Fatalf("box = %+v", b)
	}
	if b.spec.Env["ACTIONS_RUNNER_HOOK_JOB_STARTED"] == "" {
		t.Error("the refusal hook isn't installed")
	}
	if got := strings.Join(x.gh.labels[0], ","); !strings.Contains(got, "self-hosted") || !strings.Contains(got, "pluto") {
		t.Errorf("labels = %s; GitHub adds no defaults to one-time runners", got)
	}
}

func TestSlotsAndWaitingBoxesLimitNewBoxes(t *testing.T) {
	x := newHarness(t)
	x.queueJob()
	x.tick()
	x.tick()
	if len(x.gh.created) != 1 {
		t.Fatalf("a waiting box should cover its queued job, created %d", len(x.gh.created))
	}
	x.gh.jobs = append(x.gh.jobs, github.Job{ID: 2, Name: "two", Status: "queued", Labels: []string{"pluto"}})
	x.tick()
	if len(x.gh.created) != 1 {
		t.Fatalf("MaxJobs is 1, but created %d boxes", len(x.gh.created))
	}
	x.h.Config.MaxJobs = 2
	x.tick()
	if len(x.gh.created) != 2 {
		t.Fatalf("with a free slot, want 2 boxes, got %d", len(x.gh.created))
	}
}

func TestJobsNotMeantForPlutoAreIgnored(t *testing.T) {
	x := newHarness(t)
	x.gh.jobs = []github.Job{{Name: "hosted", Status: "queued", Labels: []string{"ubuntu-latest"}}}
	x.tick()
	if len(x.gh.created) != 0 {
		t.Fatal("started a box for a job without the pluto label")
	}
	if st := x.h.Status(testRepo); len(st.Stuck) != 0 || st.Queued != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestAJobWithLabelsWeCantSatisfyIsReportedStuck(t *testing.T) {
	x := newHarness(t)
	x.gh.jobs = []github.Job{{Name: "gpu", Status: "queued", Labels: []string{"self-hosted", "pluto", "gpu"}}}
	x.tick()
	if len(x.gh.created) != 0 {
		t.Fatal("started a box that could never take the job")
	}
	if st := x.h.Status(testRepo); len(st.Stuck) != 1 || st.Stuck[0].Name != "gpu" {
		t.Fatalf("status = %+v", st)
	}
}

func TestPassedRunRemovesItsBox(t *testing.T) {
	x := newHarness(t)
	r := x.startJob()
	if r.State != store.StateRunning || r.Job != "check" || r.StartedAt.IsZero() {
		t.Fatalf("running run = %+v", r)
	}
	r = x.finishJob(r, "result=Succeeded\n")
	if r.State != store.StatePassed || r.BoxKept {
		t.Fatalf("run = %+v", r)
	}
	if _, ok := x.eng.boxes[r.Box]; ok {
		t.Error("a passed run's box is still there")
	}
}

func TestFailedRunKeepsItsBoxToEnter(t *testing.T) {
	x := newHarness(t)
	r := x.finishJob(x.startJob(), "result=Failed\n")
	if r.State != store.StateFailed || !r.BoxKept || r.KeepUntil.IsZero() {
		t.Fatalf("run = %+v", r)
	}
	if _, ok := x.eng.boxes[r.Box]; !ok {
		t.Fatal("the failed box is gone")
	}
	x.eng.boxes[r.Box].running = false // The machine restarted.
	x.tick()
	if got := x.onlyRun(); !got.BoxKept {
		t.Fatal("a stopped kept box should stay kept")
	}
	resp := x.call(http.MethodPost, "/v1/runs/"+r.ID+"/enter")
	if resp.Code != http.StatusOK {
		t.Fatalf("enter: %d %s", resp.Code, resp.Body)
	}
	if !x.eng.boxes[r.Box].running {
		t.Error("enter should restart a stopped box")
	}
	var e EnterResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &e); err != nil || !strings.Contains(strings.Join(e.Argv, " "), "/w") {
		t.Fatalf("argv = %+v (%v)", e, err)
	}
}

func TestKeptBoxesExpireAndAreLimited(t *testing.T) {
	x := newHarness(t)
	r := x.finishJob(x.startJob(), "result=Failed\n")
	x.now = x.now.Add(25 * time.Hour)
	x.tick()
	got := x.onlyRun()
	if got.BoxKept {
		t.Fatal("the box outlived its time")
	}
	if _, ok := x.eng.boxes[r.Box]; ok {
		t.Fatal("the expired box is still there")
	}

	y := newHarness(t)
	y.h.Config.MaxKept = 1
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		y.now = y.now.Add(time.Minute)
		run := store.Run{ID: "r_" + id, Repo: testRepo, Box: "pluto-" + id, State: store.StateFailed, BoxKept: true, CreatedAt: y.now, KeepUntil: y.now.Add(time.Hour)}
		if err := y.h.Store.PutRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		y.eng.boxes[run.Box] = &fakeBox{running: true}
	}
	y.tick()
	if _, ok := y.eng.boxes["pluto-a"]; ok {
		t.Error("the oldest kept box should go first")
	}
	if _, ok := y.eng.boxes["pluto-b"]; !ok {
		t.Error("the newest kept box should stay")
	}
}

func TestRefusedRunRemovesItsBox(t *testing.T) {
	x := newHarness(t)
	r := x.finishJob(x.startJob(), "refused=1\nresult=Failed\n")
	if r.State != store.StateRefused || r.BoxKept {
		t.Fatalf("run = %+v", r)
	}
	if _, ok := x.eng.boxes[r.Box]; ok {
		t.Error("a refused run's box is still there")
	}
}

func TestABoxWithNoJobExpiresAndLosesItsRegistration(t *testing.T) {
	x := newHarness(t)
	x.queueJob()
	x.tick()
	x.gh.jobs = nil
	x.now = x.now.Add(x.h.Config.WaitLimit + time.Minute)
	x.tick()
	if len(x.eng.boxes) != 0 {
		t.Fatal("the idle box wasn't removed")
	}
	if len(x.gh.deleted) != 1 {
		t.Fatalf("registration not deleted: %v", x.gh.deleted)
	}
	runs, _ := x.h.Store.Runs(context.Background(), 10)
	if len(runs) != 0 {
		t.Fatalf("an expired box shouldn't be listed as a run: %+v", runs)
	}
}

func TestABoxThatStopsMidRunIsLost(t *testing.T) {
	x := newHarness(t)
	r := x.startJob()
	x.eng.boxes[r.Box].running = false
	x.tick()
	if got := x.onlyRun(); got.State != store.StateLost || got.Reason == "" {
		t.Fatalf("run = %+v", got)
	}
}

func TestABoxThatDisappearsIsLost(t *testing.T) {
	x := newHarness(t)
	r := x.startJob()
	delete(x.eng.boxes, r.Box)
	x.tick()
	if got := x.onlyRun(); got.State != store.StateLost {
		t.Fatalf("run = %+v", got)
	}
}

func TestARestartedHostPicksUpBoxesAndRemovesOrphans(t *testing.T) {
	x := newHarness(t)
	r := x.startJob()
	x.eng.boxes["pluto-stray"] = &fakeBox{running: true}
	restarted := &Host{Store: x.h.Store, Engine: x.eng, GitHub: x.h.GitHub, Config: x.h.Config, Now: x.h.Now}
	x.h = restarted
	x.tick()
	if _, ok := x.eng.boxes["pluto-stray"]; ok {
		t.Error("an unrecorded box wasn't removed")
	}
	if _, ok := x.eng.boxes[r.Box]; !ok {
		t.Fatal("a restart must not touch a running box")
	}
	got := x.finishJob(x.onlyRun(), "result=Succeeded\n")
	if got.State != store.StatePassed {
		t.Fatalf("run = %+v", got)
	}
}

func TestARunnerThatExitsWithoutAJobIsFailedAndNotKept(t *testing.T) {
	x := newHarness(t)
	x.queueJob()
	x.tick()
	x.gh.jobs = nil
	r := x.onlyRun()
	x.eng.boxes[r.Box].status = "exit=1\n"
	x.tick()
	got := x.onlyRun()
	if got.State != store.StateFailed || got.BoxKept || !strings.Contains(got.Reason, "before taking a job") {
		t.Fatalf("run = %+v", got)
	}
	if len(x.gh.deleted) != 1 {
		t.Error("its registration should be removed")
	}
}

func TestLowDiskRefusesNewBoxesWithAReason(t *testing.T) {
	x := newHarness(t)
	x.h.FreeBytes = func(context.Context) (uint64, error) { return 1 << 30, nil }
	x.queueJob()
	x.now = x.now.Add(x.h.Config.PollEvery)
	if err := x.h.Tick(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	if len(x.gh.created) != 0 {
		t.Fatal("started a box on a full disk")
	}
	if st := x.h.Status(testRepo); !strings.Contains(st.Problem, "disk") {
		t.Fatalf("status = %+v", st)
	}
}

func TestABoxThatWontStartIsRecordedAndUnregistered(t *testing.T) {
	x := newHarness(t)
	x.eng.startErr = errors.New("engine broke")
	x.queueJob()
	x.now = x.now.Add(x.h.Config.PollEvery)
	if err := x.h.Tick(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	if len(x.gh.deleted) != 1 {
		t.Error("registration not cleaned up")
	}
	if got := x.onlyRun(); got.State != store.StateFailed || !strings.Contains(got.Reason, "engine broke") {
		t.Fatalf("run = %+v", got)
	}
}

func (x *harness) call(method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	x.h.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestEnterExplainsAGoneBoxAndNamesTheNextCommand(t *testing.T) {
	x := newHarness(t)
	r := x.finishJob(x.startJob(), "result=Succeeded\n")
	rec := x.call(http.MethodPost, "/v1/runs/"+r.ID+"/enter")
	var f Failure
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict || !strings.Contains(f.Next, "pluto logs") {
		t.Fatalf("%d %+v", rec.Code, f)
	}
}

func TestConnectValidatesAndNeverEchoesTheToken(t *testing.T) {
	x := newHarness(t)
	body := `{"repo":"a/b","token":"super-secret"}`
	rec := httptest.NewRecorder()
	x.h.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/repos", strings.NewReader(body)))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "super-secret") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	x.gh.adminErr = errors.New("no admin access")
	rec = httptest.NewRecorder()
	x.h.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/repos", strings.NewReader(body)))
	if rec.Code == http.StatusOK {
		t.Fatal("a token without admin access was accepted")
	}
	list := x.call(http.MethodGet, "/v1/repos")
	if strings.Contains(list.Body.String(), "super-secret") {
		t.Fatal("repo listing leaked the token")
	}
}

func TestPreJobHookDecisions(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	const self = "o/r"
	cases := []struct {
		name, event, payload string
		allowed              bool
	}{
		{"push", "push", `{}`, true},
		{"manual", "workflow_dispatch", `{}`, true},
		{"schedule", "schedule", `{}`, true},
		{"fork code via comment", "issue_comment", `{}`, false},
		{"merge queue", "merge_group", `{}`, false},
		{"event we've never heard of", "something_new", `{}`, false},
		{"same repo PR", "pull_request", `{"pull_request":{"head":{"repo":{"full_name":"o/r"}}}}`, true},
		{"fork PR", "pull_request", `{"pull_request":{"head":{"repo":{"full_name":"evil/r","fork":true}}}}`, false},
		{"fork via target", "pull_request_target", `{"pull_request":{"head":{"repo":{"full_name":"evil/r"}}}}`, false},
		{"deleted fork", "pull_request", `{"pull_request":{"head":{"repo":null}}}`, false},
		{"unreadable payload", "pull_request", `not json`, false},
		{"fork via workflow_run", "workflow_run", `{"workflow_run":{"head_repository":{"full_name":"evil/r"}}}`, false},
		{"same repo workflow_run", "workflow_run", `{"workflow_run":{"head_repository":{"full_name":"o/r"}}}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "hook.sh")
			payload := filepath.Join(dir, "event.json")
			if err := os.WriteFile(script, []byte(preJobHook), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(payload, []byte(c.payload), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", script)
			cmd.Env = append(os.Environ(), "PLUTO_STATE_DIR="+dir, "GITHUB_EVENT_NAME="+c.event, "GITHUB_EVENT_PATH="+payload, "GITHUB_REPOSITORY="+self, "GITHUB_JOB=check")
			out, err := cmd.CombinedOutput()
			if c.allowed != (err == nil) {
				t.Fatalf("allowed = %v, want %v: %s", err == nil, c.allowed, out)
			}
			_, statErr := os.Stat(filepath.Join(dir, "refused"))
			if c.allowed == (statErr == nil) {
				t.Errorf("refused marker: %v", statErr)
			}
			if !c.allowed && !strings.Contains(string(out), "Nothing in this job ran") {
				t.Errorf("output = %s", out)
			}
		})
	}
}

func TestAJobAssignedJustBeforeItsHookRunsIsntExpired(t *testing.T) {
	x := newHarness(t)
	x.queueJob()
	x.tick()
	x.gh.jobs = nil
	x.gh.busy = true // GitHub has assigned the job, but the hook hasn't run.
	x.now = x.now.Add(x.h.Config.WaitLimit + time.Minute)
	x.tick()
	r := x.onlyRun()
	if r.State != store.StateWaiting {
		t.Fatalf("run = %+v", r)
	}
	if _, ok := x.eng.boxes[r.Box]; !ok {
		t.Fatal("removed a box that had just been given a job")
	}
}

func TestACancelledJobFreesItsSlotSoon(t *testing.T) {
	x := newHarness(t)
	x.queueJob()
	x.tick()
	x.gh.jobs = nil // Cancelled before reaching the box.
	x.tick()
	if len(x.eng.boxes) != 1 {
		t.Fatal("too soon: GitHub can take a moment to assign a job")
	}
	x.now = x.now.Add(x.h.Config.IdleLimit)
	x.tick()
	if len(x.eng.boxes) != 0 {
		t.Fatal("the idle box should give up its slot well before the 10-minute limit")
	}
}

func TestFailingBoxesBackOffInsteadOfRetryingEveryPoll(t *testing.T) {
	x := newHarness(t)
	x.eng.startErr = errors.New("engine broke")
	x.queueJob()
	for range 40 { // Ten minutes of polling.
		x.now = x.now.Add(x.h.Config.PollEvery)
		_ = x.h.Tick(context.Background())
	}
	if n := len(x.gh.created); n > 6 {
		t.Fatalf("%d attempts in 10 minutes; want backoff", n)
	}
	if !strings.Contains(x.h.Status(testRepo).Problem, "try again") {
		t.Errorf("status = %+v", x.h.Status(testRepo))
	}
	x.eng.startErr = nil
	x.now = x.now.Add(x.h.Config.MaxBackoff)
	x.tick()
	if r, _ := x.h.Store.Runs(context.Background(), 1); r[0].State != store.StateWaiting {
		t.Fatalf("should recover once the engine works: %+v", r[0])
	}
}

func TestAJobThatFinishedBeforeARestartKeepsItsResult(t *testing.T) {
	for _, c := range []struct {
		result, state string
		kept          bool
	}{
		{"Succeeded", store.StatePassed, false},
		{"SucceededWithIssues", store.StatePassed, false},
		{"Failed", store.StateFailed, true},
	} {
		x := newHarness(t)
		r := x.startJob()
		b := x.eng.boxes[r.Box]
		b.running = false // The machine rebooted before the host saw the job end.
		b.status += "exit=0\nresult=" + c.result + "\n"
		x.tick()
		got := x.onlyRun()
		if got.State != c.state || got.BoxKept != c.kept {
			t.Errorf("%s: run = %+v", c.result, got)
		}
	}
}

func TestARunnerCutOffByARestartIsLost(t *testing.T) {
	x := newHarness(t)
	r := x.startJob()
	b := x.eng.boxes[r.Box]
	b.running = false
	b.status += "exit=137\n"
	x.tick()
	if got := x.onlyRun(); got.State != store.StateLost || got.BoxKept {
		t.Fatalf("run = %+v", got)
	}
}

func TestSweepRemovesOnlyThisHostsLeftoverRunners(t *testing.T) {
	x := newHarness(t)
	r := x.startJob()
	x.gh.runners = []github.Runner{
		{ID: 100, Name: r.Box},
		{ID: 101, Name: x.h.boxPrefix() + "leftover"},
		{ID: 102, Name: x.h.boxPrefix() + "stillbusy", Busy: true},
		{ID: 103, Name: "pluto-otherhost-abc"},
		{ID: 104, Name: "someones-own-runner"},
	}
	x.gh.deleted = nil
	x.now = x.now.Add(x.h.Config.SweepEvery)
	x.tick()
	if len(x.gh.deleted) != 1 || x.gh.deleted[0] != 101 {
		t.Fatalf("deleted = %v, want only 101", x.gh.deleted)
	}
}

func TestAMissingImageDownloadsWithoutBlockingTheLoop(t *testing.T) {
	x := newHarness(t)
	x.eng.noImage.Store(true)
	x.queueJob()
	x.tick()
	if len(x.gh.created) != 0 {
		t.Fatal("started a box with no image")
	}
	if st := x.h.Status(testRepo); !strings.Contains(st.Problem, "downloading") {
		t.Fatalf("status = %+v", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for x.eng.noImage.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	x.tick()
	if len(x.gh.created) != 1 {
		t.Fatalf("created = %d after the download", len(x.gh.created))
	}
}

func TestEnterWontRestartARunThatsStillActive(t *testing.T) {
	x := newHarness(t)
	r := x.startJob()
	x.eng.boxes[r.Box].running = false
	if rec := x.call(http.MethodPost, "/v1/runs/"+r.ID+"/enter"); rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if x.eng.boxes[r.Box].running {
		t.Fatal("enter restarted an active box")
	}
}

func TestEntryScriptRunsTheRunnerOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	script := strings.ReplaceAll(entryScript, stateDir, dir)
	script = strings.ReplaceAll(script, "exec sleep infinity", "exit 0")
	script = strings.ReplaceAll(script, "./run.sh", "echo ran >> "+dir+"/ran; true")
	for range 2 {
		if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "ran"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "ran") != 1 {
		t.Fatalf("the runner ran %d times", strings.Count(string(data), "ran"))
	}
}

func TestEntryScriptMarksAnInterruptedRunner(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "started"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(strings.ReplaceAll(entryScript, stateDir, dir), "exec sleep infinity", "exit 0")
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "runner-exit"))
	if strings.TrimSpace(string(data)) != "137" {
		t.Fatalf("runner-exit = %q", data)
	}
}

func TestLockStopsASecondHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.lock")
	first, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path); err == nil {
		t.Fatal("a second host got the lock")
	}
	first.Close()
	again, err := Lock(path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again.Close()
}
