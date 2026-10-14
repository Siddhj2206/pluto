package cli_test

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// TestIdleLifecycleThroughCLI is the acceptance slice for #146: a running box
// freezes after the short idle window, stops after the long one keeping its
// volumes and record, and the next work wakes it — running the wake phase
// again — through the real CLI, daemon, and store over a real unix socket.
func TestIdleLifecycleThroughCLI(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\nwake:\n  run: [\"wake\"]\ntasks:\n  dev: { run: [\"dev\"], lock: shared }\n")
	engine := container.NewFake()
	var mu sync.Mutex
	execs := map[string]int{}
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		mu.Lock()
		execs[strings.Join(spec.Argv, " ")]++
		mu.Unlock()
		return container.ExecResult{}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, srv := startBoxDaemonServer(t, engine, adapter)

	code, _, errOut := runCLI(t, "--socket", socket, "box", "create", "--workspace", repo)
	if code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	record := boxRecord(t, st, repo)
	if got := wakeCount(&mu, execs); got != 1 {
		t.Fatalf("wake execs after create = %d, want 1", got)
	}

	const freezeAfter, stopAfter = 5 * time.Minute, 30 * time.Minute
	base := time.Now().UTC()

	// The first look starts the idle clock; nothing changes yet.
	srv.IdleStep(base, freezeAfter, stopAfter)
	if got := boxRecord(t, st, repo).State; got != state.StateRunning {
		t.Fatalf("state after first idle step = %q, want running", got)
	}

	// At the short window the box freezes, with its memory preserved.
	srv.IdleStep(base.Add(freezeAfter), freezeAfter, stopAfter)
	if got := boxRecord(t, st, repo).State; got != state.StateFrozen {
		t.Fatalf("state at freeze window = %q, want frozen", got)
	}
	if st, ok := engine.State(record.EngineID); !ok || st != container.StateFrozen {
		t.Fatalf("engine state = %q (ok=%v), want frozen", st, ok)
	}

	// At the long window it stops, keeping its volumes and record.
	srv.IdleStep(base.Add(stopAfter), freezeAfter, stopAfter)
	stopped := boxRecord(t, st, repo)
	if stopped.State != state.StateStopped {
		t.Fatalf("state at stop window = %q, want stopped", stopped.State)
	}
	if len(stopped.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want them kept through the stop", stopped.Volumes)
	}
	if st, ok := engine.State(record.EngineID); !ok || st != container.StateStopped {
		t.Fatalf("engine state = %q (ok=%v), want stopped", st, ok)
	}

	// The next work wakes the box and runs the task.
	t.Chdir(repo)
	code, _, errOut = runCLI(t, "--socket", socket, "run", "dev")
	if code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 || runs[0].Task != "dev" || runs[0].State != state.RunDone {
		t.Fatalf("runs = %v (%v), want one done dev run", runs, err)
	}
	if got := boxRecord(t, st, repo).State; got != state.StateRunning {
		t.Fatalf("state after wake = %q, want running", got)
	}
	if got := wakeCount(&mu, execs); got != 2 {
		t.Fatalf("wake execs after wake = %d, want 2", got)
	}
}

// TestFailedWakeSurfacesStepThroughCLI proves a failed wake enters wake_failed,
// starts no run, retains the box, and surfaces the failing step through
// pluto box status.
func TestFailedWakeSurfacesStepThroughCLI(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\nwake:\n  run: [\"wake\", \"--fail\"]\ntasks:\n  dev: { run: [\"dev\"], lock: shared }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		if spec.Argv[0] == "wake" {
			return container.ExecResult{ExitCode: 3}, nil
		}
		return container.ExecResult{}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, _ := startBoxDaemonServer(t, engine, adapter)
	t.Chdir(repo)

	// Running a task wakes the box; the failed wake starts no run.
	code, _, errOut := runCLI(t, "--socket", socket, "run", "dev")
	if code == 0 {
		t.Fatal("run dev succeeded despite a failing wake phase")
	}
	if !strings.Contains(errOut, "wake") {
		t.Fatalf("run dev stderr = %q, want the failing wake step", errOut)
	}
	if runs, err := st.Runs(); err != nil || len(runs) != 0 {
		t.Fatalf("runs = %v (%v), want none after a failed wake", runs, err)
	}

	// The box is retained and its state and failing step are inspectable.
	code, out, errOut := runCLI(t, "--socket", socket, "box", "status", repo)
	if code != 0 {
		t.Fatalf("box status exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"wake_failed", "wake --fail", "exit 3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("box status output = %q, want %q", out, want)
		}
	}
}

// wakeCount reports how many times the declared wake command ran.
func wakeCount(mu *sync.Mutex, execs map[string]int) int {
	mu.Lock()
	defer mu.Unlock()
	return execs["wake"]
}
