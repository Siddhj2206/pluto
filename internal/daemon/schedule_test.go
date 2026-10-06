package daemon_test

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
)

// schedulerClock is a clock for scheduler tests: the scheduler reads the
// test's time instead of the wall clock, so occurrences are exact. The loop
// goroutine and the test both touch it, so reads and writes are guarded.
type schedulerClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *schedulerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// set advances the test clock; safe while a loop is running on it.
func (c *schedulerClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// armSchedule stores a declared schedule on a box the way handoff does: the
// entry exists from armed onwards, with no firing consumed yet.
func armSchedule(t *testing.T, st *state.Store, boxID string, armed time.Time, sched state.Schedule) {
	t.Helper()
	if _, err := st.SetSchedules(boxID, []state.Schedule{sched}, armed); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
}

// runScheduler starts the daemon's scheduler loop on the test's clock and
// stops it before the test's temp dirs are cleaned up, so a fire goroutine
// cannot race RemoveAll.
func runScheduler(t *testing.T, srv *daemon.Server, clock *schedulerClock) {
	t.Helper()
	srv.Now = clock.Now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.SchedulerLoop(ctx, 5*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestSchedulerFiresDueScheduleAndRecordsTheJob(t *testing.T) {
	fired := make(chan contract.Exec, 4)
	socket, st, srv := startServer(t, fakeRunner{record: true, fired: fired})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), `
[jobs.work]
command = ["echo", "scheduled"]
`)
	box := createBoxAt(t, c, worktree)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "every-minute", Cron: "* * * * *", Job: "work"})
	clock := &schedulerClock{now: armed.Add(70 * time.Second)}
	runScheduler(t, srv, clock)

	select {
	case spec := <-fired:
		if got := strings.Join(spec.Command.Argv(), " "); got != "echo scheduled" {
			t.Fatalf("fired %q, want the declared job", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the schedule to fire")
	}

	waitFor(t, "the firing recorded", func() bool {
		got, err := st.Box(box.ID)
		items, qerr := st.Queue()
		return err == nil && qerr == nil && len(got.Jobs) == 1 && got.Jobs[0].State == state.JobDone &&
			got.Schedules[0].LastFired != nil && len(items) == 1 && items[0].State == state.QueueDone
	})
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want the box woken for its schedule", got.State)
	}
	if got.Jobs[0].Command != "echo scheduled" {
		t.Fatalf("job = %+v, want the scheduled command recorded", got.Jobs[0])
	}

	// The same occurrence must not fire twice, however many ticks pass.
	time.Sleep(50 * time.Millisecond)
	if len(fired) != 0 {
		t.Fatalf("scheduler fired %d extra runs for one occurrence", len(fired))
	}
}

func TestSchedulerWarmUpWakesTheBoxWithoutRunningAJob(t *testing.T) {
	fired := make(chan contract.Exec, 4)
	socket, st, srv := startServer(t, fakeRunner{fired: fired})
	c := client(socket)
	box := createBox(t, c)

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "warm", Cron: "* * * * *"})
	clock := &schedulerClock{now: armed.Add(70 * time.Second)}
	runScheduler(t, srv, clock)

	waitFor(t, "the warm-up box running", func() bool {
		got, err := st.Box(box.ID)
		return err == nil && got.State == state.StateRunning
	})
	time.Sleep(50 * time.Millisecond)
	if len(fired) != 0 {
		t.Fatalf("warm-up ran %d jobs, want none", len(fired))
	}
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if len(got.Jobs) != 0 {
		t.Fatalf("job history = %+v, want a warm-up to leave none", got.Jobs)
	}
}

func TestWarmUpWaitsForHostRunningBoxCapacity(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	c := client(socket)
	running := runningBox(t, c)
	warm := createBoxAt(t, c, writeContract(t, t.TempDir(), ""))
	srv.MaxRunningBoxes = 1
	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, warm.ID, armed, state.Schedule{Name: "warm", Cron: "* * * * *"})
	clock := &schedulerClock{now: armed.Add(70 * time.Second)}
	runScheduler(t, srv, clock)
	waitFor(t, "warm-up queued at host capacity", func() bool {
		items, err := st.Queue()
		return err == nil && len(items) == 1 && items[0].State == state.QueuePending
	})
	got, err := st.Box(warm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != state.StateCreated {
		t.Fatalf("warm-up state = %q while host is full", got.State)
	}
	if _, err := st.Transition(running.ID, state.StatePaused); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "warm-up to run after host capacity opens", func() bool {
		got, err := st.Box(warm.ID)
		return err == nil && got.State == state.StateRunning
	})
}

func TestSchedulerCoalescesMissedFiringsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	worktree := writeContract(t, t.TempDir(), `
[jobs.work]
command = "true"
`)
	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	st, err := state.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	box, _, err := st.CreateBox("alpha", "main", worktree)
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "every-minute", Cron: "* * * * *", Job: "work"})
	if _, err := st.AdvanceSchedule(box.ID, "every-minute", armed.Add(30*time.Second)); err != nil {
		t.Fatalf("AdvanceSchedule: %v", err)
	}
	// The daemon goes down with four occurrences (12:01..12:04) still ahead.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := state.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	fired := make(chan contract.Exec, 4)
	srv := daemon.New(reopened, fakeRunner{st: reopened, record: true, fired: fired}, "test")
	clock := &schedulerClock{now: armed.Add(4*time.Minute + 30*time.Second)}
	runScheduler(t, srv, clock)

	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the coalesced run")
	}
	time.Sleep(100 * time.Millisecond)
	if len(fired) != 0 {
		t.Fatalf("scheduler replayed %d extra missed runs, want one coalesced run", len(fired))
	}
	got, err := reopened.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("job history = %+v, want exactly one late run", got.Jobs)
	}
	if got.Schedules[0].LastFired == nil || !got.Schedules[0].LastFired.Equal(clock.Now()) {
		t.Fatalf("last_fired = %v, want the coalesced run to consume through %v", got.Schedules[0].LastFired, clock.Now())
	}
}

func TestSchedulerQueuesWhileTheBoxIsBusyAndRunsWhenCapacityOpens(t *testing.T) {
	fired := make(chan contract.Exec, 4)
	socket, st, srv := startServer(t, fakeRunner{fired: fired, record: true})
	var mu sync.Mutex
	var logs []string
	srv.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), `
[jobs.work]
command = "true"
`)
	box := createBoxAt(t, c, worktree)
	if _, err := st.BeginJob(box.ID, state.StartJob(state.NewID(), []string{"make"})); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "every-minute", Cron: "* * * * *", Job: "work"})
	clock := &schedulerClock{now: armed.Add(70 * time.Second)}
	runScheduler(t, srv, clock)

	waitFor(t, "the occurrence queued", func() bool {
		items, err := st.Queue()
		return err == nil && len(items) == 1 && items[0].State == state.QueuePending
	})
	if len(fired) != 0 {
		t.Fatalf("scheduler started %d runs on a busy box", len(fired))
	}
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if len(got.Jobs) != 1 || got.Jobs[0].Command != "make" {
		t.Fatalf("job history = %+v, want the existing run before capacity opens", got.Jobs)
	}
	finished := got.Jobs[0]
	finished.Finish(state.JobDone, 0, "")
	if _, err := st.SetJob(box.ID, finished); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	waitFor(t, "the queued scheduled job to run", func() bool {
		got, err := st.Box(box.ID)
		return err == nil && len(got.Jobs) == 2 && got.Jobs[0].State == state.JobDone
	})
}

func TestSchedulerFiresEachBoxWithoutWaitingOnALongJob(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseJob := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseJob()
	started := make(chan string, 4)
	_, st, srv := startServer(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		started <- box.Project
		if box.Project == "alpha" {
			<-release // one long job must not hold up the other boxes
		}
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	worktrees := map[string]string{
		"alpha": writeContract(t, t.TempDir(), "[jobs.work]\ncommand = \"true\"\n"),
		"beta":  writeContract(t, t.TempDir(), "[jobs.work]\ncommand = \"true\"\n"),
	}
	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	boxes := map[string]*state.Box{}
	for project, worktree := range worktrees {
		box, _, err := st.CreateBox(project, "main", worktree)
		if err != nil {
			t.Fatalf("CreateBox(%s): %v", project, err)
		}
		boxes[project] = box
		armSchedule(t, st, box.ID, armed, state.Schedule{Name: "every-minute", Cron: "* * * * *", Job: "work"})
	}
	clock := &schedulerClock{now: armed.Add(70 * time.Second)}
	runScheduler(t, srv, clock)

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case project := <-started:
			seen[project] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("only %v started; a long job held up the other box", seen)
		}
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Fatalf("started = %v, want both boxes", seen)
	}

	// Let the long job finish and both firings settle before the test's state
	// directory is torn down.
	releaseJob()
	waitFor(t, "both queued firings completed", func() bool {
		items, err := st.Queue()
		if err != nil || len(items) != 2 {
			return false
		}
		for _, item := range items {
			if item.State != state.QueueDone {
				return false
			}
		}
		return true
	})
}

func TestSchedulerWakesPausedBoxAndAutoPauseSleepsItAgain(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{window: 30 * time.Millisecond})
	c := client(socket)
	box := runningBox(t, c)
	if resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/pause", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d, body %s", resp.StatusCode, data)
	}

	armed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	armSchedule(t, st, box.ID, armed, state.Schedule{Name: "warm", Cron: "* * * * *"})
	clock := &schedulerClock{now: armed.Add(70 * time.Second)}
	// The scheduler and auto-pause share the one Server.Now seam. Drive the
	// scheduler on the test clock, then stop it before the auto-pause loop
	// takes over on the wall clock.
	srv.Now = clock.Now
	schedCtx, stopScheduler := context.WithCancel(context.Background())
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		srv.SchedulerLoop(schedCtx, 5*time.Millisecond)
	}()

	waitFor(t, "the schedule to wake the box", func() bool {
		got, err := st.Box(box.ID)
		return err == nil && got.State == state.StateRunning
	})
	stopScheduler()
	<-schedDone

	srv.Now = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.AutoPauseLoop(ctx, 5*time.Millisecond)
	waitFor(t, "auto-pause to sleep the box again", func() bool {
		got, err := st.Box(box.ID)
		return err == nil && got.State == state.StatePaused
	})
}
