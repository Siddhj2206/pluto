package cli_test

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// cliResult is one runCLI call's outcome, collected from a goroutine so two
// CLI runs can be in flight at once.
type cliResult struct {
	code int
	out  string
	err  string
}

// runCLIAsync runs the CLI on its own goroutine. Concurrency tests start one of
// these per run and order them through the fake engine's channels.
func runCLIAsync(t *testing.T, args ...string) <-chan cliResult {
	t.Helper()
	ch := make(chan cliResult, 1)
	go func() {
		code, out, errOut := runCLI(t, args...)
		ch <- cliResult{code: code, out: out, err: errOut}
	}()
	return ch
}

// awaitEngine blocks until a run reaches the engine, failing the test after a
// short bound. The channel is the run's own entry signal, so ordering is
// observed rather than slept for.
func awaitEngine(t *testing.T, entered <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s to reach the engine", what)
	}
}

// requireNotEntered proves a run has not reached the engine at this instant. It
// is only meaningful when another holder is known to own the box.
func requireNotEntered(t *testing.T, entered <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-entered:
		t.Fatalf("%s reached the engine while the lock policy should have held it", what)
	default:
	}
}

// awaitRun records that a task's run has been written; BeginRun happens before
// the lock policy is consulted, so it is the barrier that a request reached the
// daemon.
func awaitRun(t *testing.T, st *state.Store, task string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := st.Runs()
		if err != nil {
			t.Fatalf("Runs: %v", err)
		}
		for _, run := range runs {
			if run.Task == task {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for a %s run to be recorded", task)
}

// TestNamedTasksShareOneBoxAndVolumes proves several named tasks for one
// workspace resolve to the same durable box and the same volumes.
func TestNamedTasksShareOneBoxAndVolumes(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: [\"true\"], lock: shared }\n  build: { run: [\"true\"], lock: exclusive }\n")
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	for _, task := range []string{"dev", "build"} {
		if code, _, errOut := runCLI(t, "--socket", socket, "run", task); code != 0 {
			t.Fatalf("run %s exit = %d, stderr: %s", task, code, errOut)
		}
	}

	runs, err := st.Runs()
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %v (%v), want two", runs, err)
	}
	if runs[0].BoxID != runs[1].BoxID {
		t.Fatalf("named tasks used different boxes: %s vs %s", runs[0].BoxID, runs[1].BoxID)
	}
	record := boxRecord(t, st, repo)
	if record.ID != runs[0].BoxID {
		t.Fatalf("box record %s != run box %s", record.ID, runs[0].BoxID)
	}
	if len(record.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want the workspace and home volumes", record.Volumes)
	}
	creates := 0
	for _, call := range engine.Calls() {
		if len(call) >= 7 && call[:7] == "create:" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("engine create calls = %d, want one shared box (%v)", creates, engine.Calls())
	}
}

// TestSharedRunsRunConcurrently proves a `shared` task runs alongside another
// shared run of the same box.
func TestSharedRunsRunConcurrently(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: [\"hold\"], lock: shared }\n")
	engine := container.NewFake()
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	engine.ExecFunc = func(_ string, _ container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		entered <- struct{}{}
		<-release
		return container.ExecResult{}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, _ := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	first := runCLIAsync(t, "--socket", socket, "run", "dev")
	awaitEngine(t, entered, "the first shared run")
	second := runCLIAsync(t, "--socket", socket, "run", "dev")
	awaitEngine(t, entered, "the second shared run")

	close(release)
	for i, ch := range []<-chan cliResult{first, second} {
		res := <-ch
		if res.code != 0 {
			t.Fatalf("shared run #%d exit = %d, stderr: %s", i+1, res.code, res.err)
		}
	}
}

// TestExclusiveRunWaitsForSharedWork proves an `exclusive` task runs alone: it
// waits for shared work to drain before starting, and shared work arriving
// while it runs waits.
func TestExclusiveRunWaitsForSharedWork(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  one: { run: [\"hold\", \"one\"], lock: shared }\n  batch: { run: [\"hold\", \"batch\"], lock: exclusive }\n  two: { run: [\"hold\", \"two\"], lock: shared }\n")
	engine := container.NewFake()
	entered := map[string]chan struct{}{"one": make(chan struct{}), "batch": make(chan struct{}), "two": make(chan struct{})}
	release := map[string]chan struct{}{"one": make(chan struct{}), "batch": make(chan struct{}), "two": make(chan struct{})}
	var mu sync.Mutex
	closed := map[string]bool{}
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		name := spec.Argv[1]
		mu.Lock()
		if !closed[name] {
			close(entered[name])
			closed[name] = true
		}
		mu.Unlock()
		<-release[name]
		return container.ExecResult{}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	shared := runCLIAsync(t, "--socket", socket, "run", "one")
	awaitEngine(t, entered["one"], "the shared run")

	exclusive := runCLIAsync(t, "--socket", socket, "run", "batch")
	awaitRun(t, st, "batch")
	requireNotEntered(t, entered["batch"], "the exclusive run while shared work held the box")

	close(release["one"])
	awaitEngine(t, entered["batch"], "the exclusive run after shared work drained")

	joining := runCLIAsync(t, "--socket", socket, "run", "two")
	awaitRun(t, st, "two")
	requireNotEntered(t, entered["two"], "the shared run while an exclusive run held the box")

	close(release["batch"])
	awaitEngine(t, entered["two"], "the shared run after the exclusive run released")
	close(release["two"])

	for _, ch := range []<-chan cliResult{shared, exclusive, joining} {
		if res := <-ch; res.code != 0 {
			t.Fatalf("run exit = %d, stderr: %s", res.code, res.err)
		}
	}
}

// TestServiceTaskRunsForTheBoxLifeAndRestartsOnStart proves a `service` task
// keeps running alongside runs and is restarted on every box start.
func TestServiceTaskRunsForTheBoxLifeAndRestartsOnStart(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  db: { service: true, run: [\"db\"] }\n  dev: { run: [\"dev\"], lock: shared }\n")
	engine := container.NewFake()
	dbRelease := make(chan struct{})
	dbStarted := make(chan struct{}, 8)
	var mu sync.Mutex
	dbStarts := 0
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		if spec.Argv[0] == "db" {
			mu.Lock()
			dbStarts++
			mu.Unlock()
			dbStarted <- struct{}{}
			<-dbRelease
		}
		return container.ExecResult{}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Cleanup(func() { close(dbRelease) })
	t.Chdir(repo)

	// Creating the box starts the service, and it stays running.
	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", repo); code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	awaitEngine(t, dbStarted, "the service task")

	// A shared run proceeds while the service runs.
	if code, _, errOut := runCLI(t, "--socket", socket, "run", "dev"); code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	if got := startCount(&mu, &dbStarts); got != 1 {
		t.Fatalf("service starts = %d, want the service to keep running without restarting", got)
	}

	// Running a service task is refused: it starts with the box.
	if code, _, errOut := runCLI(t, "--socket", socket, "run", "db"); code == 0 {
		t.Fatal("run db succeeded, want a service task to be refused")
	} else if !strings.Contains(errOut, "service") {
		t.Fatalf("run db stderr = %q, want a service note", errOut)
	}

	// Stopping then starting the box restarts the service.
	record := boxRecord(t, st, repo)
	if _, err := client.New(socket).StopContainerBox(record.ID); err != nil {
		t.Fatalf("stop box: %v", err)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "run", "dev"); code != 0 {
		t.Fatalf("run dev after restart exit = %d, stderr: %s", code, errOut)
	}
	awaitEngine(t, dbStarted, "the service task after a restart")
	if got := startCount(&mu, &dbStarts); got != 2 {
		t.Fatalf("service starts = %d, want the service restarted on the second start", got)
	}
}

func startCount(mu *sync.Mutex, n *int) int {
	mu.Lock()
	defer mu.Unlock()
	return *n
}
