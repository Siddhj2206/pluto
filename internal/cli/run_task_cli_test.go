package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// writePlutoYML writes a pluto.yml into a workspace.
func writePlutoYML(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "pluto.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write pluto.yml: %v", err)
	}
}

// TestRunDeclaredTaskRecordsRunOnTheDurableBox is the acceptance slice: a task
// declared in pluto.yml runs in the workspace's durable box through the real
// CLI and daemon, streams its output, and adds one run with a step to its
// history.
func TestRunDeclaredTaskRecordsRunOnTheDurableBox(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"echo hi\", lock: shared }\n")
	engine := container.NewFake()
	var got container.ExecSpec
	engine.ExecFunc = func(id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		got = spec
		_, _ = sink.Write([]byte("hi\n"))
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "run", "dev")
	if code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("run dev output = %q, want the streamed step output", out)
	}
	if strings.Join(got.Argv, " ") != "/bin/sh -c echo hi" {
		t.Fatalf("exec argv = %v, want the declared command", got.Argv)
	}

	runs, err := st.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want one run for the dev task", len(runs))
	}
	run := runs[0]
	if run.Task != "dev" || run.State != state.RunDone || run.ExitCode != 0 {
		t.Fatalf("run = %+v, want dev done exit 0", run)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("steps = %+v, want one implicit step", run.Steps)
	}
	if run.Steps[0].Command != "echo hi" || run.Steps[0].State != state.RunDone || run.Steps[0].ExitCode != 0 {
		t.Fatalf("step = %+v, want the implicit done step", run.Steps[0])
	}
	if run.BoxID == "" || run.WorkspacePath != repo {
		t.Fatalf("run box/workspace = %q/%q, want the workspace's box", run.BoxID, run.WorkspacePath)
	}
	if run.StartedAt == nil || run.FinishedAt == nil || run.Trigger != state.TriggerManual {
		t.Fatalf("run timing/trigger = %+v, want started, finished, and manual", run)
	}
	if run.Steps[0].StartedAt == nil || run.Steps[0].FinishedAt == nil {
		t.Fatalf("step timing = %+v, want started and finished", run.Steps[0])
	}

	box := boxRecord(t, st, repo)
	if box.ID != run.BoxID {
		t.Fatalf("run box %s != workspace box %s", run.BoxID, box.ID)
	}
}

// TestLsRunsShowsTheTaskHistory proves a recorded run appears in `pluto ls
// runs` with its task, state, and exit code.
func TestLsRunsShowsTheTaskHistory(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"echo hi\" }\n")
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	if code, _, errOut := runCLI(t, "--socket", socket, "run", "dev"); code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one run", runs, err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "ls", "runs")
	if code != 0 {
		t.Fatalf("ls runs exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{state.ShortID(runs[0].ID), "dev", "done", "0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ls runs output = %q, want %q", out, want)
		}
	}
}

// A failing task adds a failed run carrying the command's exit code, and the
// CLI exits with that code.
func TestRunDeclaredTaskCarriesTheExitCode(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  build: { run: [\"make\", \"test\"], lock: exclusive }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		return container.ExecResult{ExitCode: 3}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	code, _, errOut := runCLI(t, "--socket", socket, "run", "build")
	if code != 3 {
		t.Fatalf("run build exit = %d, want 3 (stderr: %s)", code, errOut)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one run", runs, err)
	}
	run := runs[0]
	if run.State != state.RunFailed || run.ExitCode != 3 {
		t.Fatalf("run = %+v, want failed exit 3", run)
	}
	if len(run.Steps) != 1 || run.Steps[0].State != state.RunFailed || run.Steps[0].ExitCode != 3 {
		t.Fatalf("steps = %+v, want the failing step", run.Steps)
	}
}

// TestShowRunShowsTheRunDetails proves `pluto show <run>` resolves a run by an
// unambiguous id prefix and prints its task, state, timing, exit code, and
// step.
func TestShowRunShowsTheRunDetails(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"echo hi\" }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		_, _ = sink.Write([]byte("hi\n"))
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	if code, _, errOut := runCLI(t, "--socket", socket, "run", "dev"); code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one run", runs, err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "show", runs[0].ID[:8])
	if code != 0 {
		t.Fatalf("show exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"task:", "dev", "state:", "done", "exit:", "step run", "echo hi"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output = %q, want %q", out, want)
		}
	}
}

// TestEachInvocationAddsARunToTheTaskHistory proves repeated runs of a task
// share one durable box and each adds a run to the task's history.
func TestEachInvocationAddsARunToTheTaskHistory(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"true\" }\n")
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	for i := 0; i < 2; i++ {
		if code, _, errOut := runCLI(t, "--socket", socket, "run", "dev"); code != 0 {
			t.Fatalf("run dev #%d exit = %d, stderr: %s", i+1, code, errOut)
		}
	}

	runs, err := st.Runs()
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %v (%v), want two runs", runs, err)
	}
	if runs[0].BoxID != runs[1].BoxID {
		t.Fatalf("runs used different boxes: %s vs %s", runs[0].BoxID, runs[1].BoxID)
	}
	creates := 0
	for _, call := range engine.Calls() {
		if strings.HasPrefix(call, "create:") {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("engine create calls = %d, want one durable box (%v)", creates, engine.Calls())
	}

	code, out, errOut := runCLI(t, "--socket", socket, "ls", "runs")
	if code != 0 {
		t.Fatalf("ls runs exit = %d, stderr: %s", code, errOut)
	}
	if got := strings.Count(out, "dev"); got < 2 {
		t.Fatalf("ls runs output = %q, want both runs recorded", out)
	}
}

// A task named like a run-manager subcommand still runs: a declared task beats
// the legacy `pluto run ls|show|logs` manager.
func TestRunTaskNamedLikeRunManagerSubcommand(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  logs: { run: \"true\" }\n")
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	code, _, errOut := runCLI(t, "--socket", socket, "run", "logs")
	if code != 0 {
		t.Fatalf("run logs exit = %d, stderr: %s", code, errOut)
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one run for the declared logs task", runs, err)
	}
	if runs[0].Task != "logs" {
		t.Fatalf("run task = %q, want logs", runs[0].Task)
	}
}

// An unknown task in a pluto.yml project fails with the declared tasks, not the
// legacy job path, and touches no box.
func TestRunUnknownTaskFailsWithTheDeclaredTasks(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"true\" }\n  build: { run: \"true\" }\n")
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, _ := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	code, _, errOut := runCLI(t, "--socket", socket, "run", "nope")
	if code == 0 {
		t.Fatal("an unknown task must fail")
	}
	for _, want := range []string{"no such task", "build", "dev"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
	if len(engine.Calls()) != 0 {
		t.Fatalf("an unknown task touched the engine: %v", engine.Calls())
	}
}

// --async does not apply to a declared task; it is a usage error, not a
// silently ignored flag.
func TestRunDeclaredTaskRejectsAsync(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"true\" }\n")
	socket, _ := startBoxDaemon(t, container.NewFake(), &vcs.Fake{Workspace: vcs.Workspace{Path: repo}})
	t.Chdir(repo)

	code, _, errOut := runCLI(t, "--socket", socket, "run", "--async", "dev")
	if code != 2 {
		t.Fatalf("run --async dev exit = %d, want 2 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "--async is not supported") {
		t.Fatalf("stderr = %q, want the --async note", errOut)
	}
}
