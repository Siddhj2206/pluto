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

	box := boxRecord(t, st, repo)
	if box.ID != run.BoxID {
		t.Fatalf("run box %s != workspace box %s", run.BoxID, box.ID)
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
