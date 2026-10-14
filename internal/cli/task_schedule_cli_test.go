package cli_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// testClock is a scheduler clock the test advances by hand, so occurrences are
// exact and tests never wait on the wall clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// runSchedulerLoop runs the daemon's scheduler on the test's clock until the
// returned stop is called (and at test end). Tests that mutate schedule state
// between phases stop the loop first, so reconciliation cannot race the change.
func runSchedulerLoop(t *testing.T, srv *daemon.Server, clock *testClock) func() {
	t.Helper()
	srv.Now = clock.Now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.SchedulerLoop(ctx, 5*time.Millisecond)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	return stop
}

// armTaskSchedule stores a pluto.yml schedule on a box, the way the daemon's
// reconciliation will preserve it: armed at armed, nothing consumed yet.
func armTaskSchedule(t *testing.T, st *state.Store, boxID, name, cron, task string, armed time.Time) {
	t.Helper()
	if _, err := st.SetSchedules(boxID, []state.Schedule{{Name: name, Cron: cron, Task: task}}, armed); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
}

// TestScheduleFiresRunOnTheNamedTask is the acceptance slice: a schedule that
// names a declared task fires a run for that task on the workspace's box, with
// the schedule occurrence in its trigger context.
func TestScheduleFiresRunOnTheNamedTask(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  build: { run: [\"make\", \"test\"], lock: exclusive }\nschedules:\n  nightly: { task: build, cron: \"* * * * *\" }\n")
	engine := container.NewFake()
	entered := make(chan container.ExecSpec, 4)
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		entered <- spec
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, srv := startBoxDaemonServer(t, engine, adapter)

	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", repo); code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	box := boxRecord(t, st, repo)
	armed := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	armTaskSchedule(t, st, box.ID, "nightly", "* * * * *", "build", armed)
	runSchedulerLoop(t, srv, &testClock{now: armed.Add(70 * time.Second)})

	select {
	case spec := <-entered:
		if got := strings.Join(spec.Argv, " "); got != "make test" {
			t.Fatalf("engine exec = %q, want the declared task command", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the schedule to run the task")
	}

	waitForCondition(t, "the scheduled run recorded", func() bool {
		runs, err := st.Runs()
		return err == nil && len(runs) == 1 && runs[0].State == state.RunDone
	})
	runs, _ := st.Runs()
	run := runs[0]
	if run.Task != "build" || run.BoxID != box.ID || run.Trigger != state.TriggerSchedule {
		t.Fatalf("run = %+v, want a schedule-triggered build on the box", run)
	}
	occurrence := armed.Add(time.Minute)
	if run.TriggerContext == nil || run.TriggerContext.Schedule != "nightly" || run.TriggerContext.Occurrence == nil || !run.TriggerContext.Occurrence.Equal(occurrence) {
		t.Fatalf("trigger context = %+v, want the nightly occurrence at %v", run.TriggerContext, occurrence)
	}
	if len(run.Steps) != 1 || run.Steps[0].Command != "make test" {
		t.Fatalf("steps = %+v, want the task's implicit step", run.Steps)
	}
}

// TestScheduleCoalescesMissedOccurrences proves a task has at most one pending
// run: several due occurrences coalesce into one, the latest trigger time wins,
// and the skipped occurrences are noted in the run's trigger context.
func TestScheduleCoalescesMissedOccurrences(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  build: { run: [\"true\"] }\nschedules:\n  nightly: { task: build, cron: \"* * * * *\" }\n")
	engine := container.NewFake()
	execs := make(chan struct{}, 8)
	engine.ExecFunc = func(_ string, _ container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		execs <- struct{}{}
		return container.ExecResult{}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, srv := startBoxDaemonServer(t, engine, adapter)

	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", repo); code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	box := boxRecord(t, st, repo)
	armed := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	armTaskSchedule(t, st, box.ID, "nightly", "* * * * *", "build", armed)
	// Three occurrences are due at once: 03:01, 03:02, and 03:03.
	runSchedulerLoop(t, srv, &testClock{now: armed.Add(3*time.Minute + 10*time.Second)})

	waitForCondition(t, "the coalesced run recorded", func() bool {
		runs, err := st.Runs()
		return err == nil && len(runs) == 1 && runs[0].State == state.RunDone
	})
	runs, _ := st.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want one coalesced run", len(runs))
	}
	run := runs[0]
	latest := armed.Add(3 * time.Minute)
	skipped := run.TriggerContext.Skipped
	if run.TriggerContext.Occurrence == nil || !run.TriggerContext.Occurrence.Equal(latest) {
		t.Fatalf("occurrence = %v, want the latest %v", run.TriggerContext.Occurrence, latest)
	}
	if len(skipped) != 2 || !skipped[0].At.Equal(armed.Add(time.Minute)) || !skipped[1].At.Equal(armed.Add(2*time.Minute)) {
		t.Fatalf("skipped = %+v, want the two displaced occurrences", skipped)
	}
	if got := len(execs); got != 1 {
		t.Fatalf("engine execs = %d, want the one coalesced run", got)
	}
}

// TestScheduleRedeliveryReusesTheRun proves redelivering the same occurrence
// reuses the accepted run identity rather than creating duplicate history.
func TestScheduleRedeliveryReusesTheRun(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  build: { run: [\"true\"] }\nschedules:\n  nightly: { task: build, cron: \"* * * * *\" }\n")
	engine := container.NewFake()
	execs := make(chan struct{}, 8)
	engine.ExecFunc = func(_ string, _ container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		execs <- struct{}{}
		return container.ExecResult{}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, srv := startBoxDaemonServer(t, engine, adapter)

	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", repo); code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	box := boxRecord(t, st, repo)
	armed := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	armTaskSchedule(t, st, box.ID, "nightly", "* * * * *", "build", armed)
	clock := &testClock{now: armed.Add(70 * time.Second)}
	stop := runSchedulerLoop(t, srv, clock)

	waitForCondition(t, "the first scheduled run recorded", func() bool {
		runs, err := st.Runs()
		return err == nil && len(runs) == 1 && runs[0].State == state.RunDone
	})

	// Redeliver the same occurrence: stop the loop, drop the consumed cursor so
	// the occurrence is due again, and restart. The store must reuse the run.
	stop()
	if _, err := st.SetSchedules(box.ID, nil, clock.Now()); err != nil {
		t.Fatalf("clear schedules: %v", err)
	}
	armTaskSchedule(t, st, box.ID, "nightly", "* * * * *", "build", armed)
	runSchedulerLoop(t, srv, clock)
	occurrence := armed.Add(time.Minute)
	waitForCondition(t, "the redelivery consumed", func() bool {
		got, err := st.Box(box.ID)
		return err == nil && len(got.Schedules) == 1 && got.Schedules[0].LastFired != nil && got.Schedules[0].LastFired.Equal(occurrence)
	})

	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one after a redelivery", runs, err)
	}
	if got := len(execs); got != 1 {
		t.Fatalf("engine execs = %d, want the redelivery not to re-run the task", got)
	}
}

// TestScheduledRunMasksDeclaredSecrets proves a scheduled run crosses the same
// host-side masking boundary as a manual run: a declared secret never reaches
// the run's retained log.
func TestScheduledRunMasksDeclaredSecrets(t *testing.T) {
	const secret = "scheduled-secret-value"
	t.Setenv("SCHED_TOKEN", secret)
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\nsecrets:\n  SCHED_TOKEN: { env: SCHED_TOKEN }\ntasks:\n  build: { run: deploy }\nschedules:\n  nightly: { task: build, cron: \"* * * * *\" }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(_ string, _ container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		_, _ = sink.Write([]byte("push token=" + secret + " done\n"))
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, srv := startBoxDaemonServer(t, engine, adapter)

	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", repo); code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	box := boxRecord(t, st, repo)
	armed := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	armTaskSchedule(t, st, box.ID, "nightly", "* * * * *", "build", armed)
	runSchedulerLoop(t, srv, &testClock{now: armed.Add(70 * time.Second)})

	waitForCondition(t, "the scheduled run recorded", func() bool {
		runs, err := st.Runs()
		return err == nil && len(runs) == 1 && runs[0].State == state.RunDone
	})
	runs, _ := st.Runs()
	retained, err := st.ReadRunLog(runs[0].Steps[0].Log)
	if err != nil {
		t.Fatalf("ReadRunLog: %v", err)
	}
	if strings.Contains(retained, secret) {
		t.Fatalf("retained log leaked the secret: %q", retained)
	}
	if !strings.Contains(retained, "[REDACTED]") {
		t.Fatalf("retained log = %q, want a redaction", retained)
	}
}

// TestScheduledRunWaitsForABusyBox proves a due run on a busy box waits for the
// active work per its lock policy: it goes through the box's gate and only
// reaches the engine once the exclusive holder releases.
func TestScheduledRunWaitsForABusyBox(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  hold: { run: [\"hold\"], lock: exclusive }\n  nightly: { run: [\"nightly\"], lock: exclusive }\nschedules:\n  nightly: { task: nightly, cron: \"* * * * *\" }\n")
	engine := container.NewFake()
	release := make(chan struct{})
	holdEntered := make(chan struct{})
	scheduledEntered := make(chan struct{})
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		switch spec.Argv[0] {
		case "hold":
			close(holdEntered)
			<-release
		case "nightly":
			close(scheduledEntered)
		}
		return container.ExecResult{}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, srv := startBoxDaemonServer(t, engine, adapter)
	t.Chdir(repo)
	clock := &testClock{now: time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)}
	runSchedulerLoop(t, srv, clock)

	// A manual exclusive run holds the box.
	holding := runCLIAsync(t, "--socket", socket, "run", "hold")
	awaitEngine(t, holdEntered, "the exclusive manual run")

	// The schedule fires while the box is busy; the run is accepted but does
	// not reach the engine until the holder releases.
	box := boxRecord(t, st, repo)
	armTaskSchedule(t, st, box.ID, "nightly", "* * * * *", "nightly", clock.Now())
	clock.set(clock.Now().Add(70 * time.Second))
	awaitRun(t, st, "nightly")
	requireNotEntered(t, scheduledEntered, "the scheduled run while the box is busy")

	close(release)
	awaitEngine(t, scheduledEntered, "the scheduled run after the box drained")
	if res := <-holding; res.code != 0 {
		t.Fatalf("manual run exit = %d, stderr: %s", res.code, res.err)
	}
}
