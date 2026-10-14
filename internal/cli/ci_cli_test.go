package cli_test

import (
	"io"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// TestCIFailureRetainsItsBoxAndOffersAttach proves a failed CI run keeps its
// ephemeral box, that the run's details name a working attach path, and that
// the path actually connects to the retained box (#148).
func TestCIFailureRetainsItsBoxAndOffersAttach(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  test: { run: \"go test ./...\", lock: exclusive }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(_ string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		// The box terminal's shell runs with a tty; the task does not.
		if spec.TTY {
			_, _ = io.WriteString(sink, "shell\n")
			return container.ExecResult{ExitCode: 0}, nil
		}
		_, _ = io.WriteString(sink, "FAIL\n")
		return container.ExecResult{ExitCode: 2}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "ci", "test")
	if code != 2 {
		t.Fatalf("ci test exit = %d, want the task's 2 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("ci output = %q, want the streamed step output", out)
	}

	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one run", runs, err)
	}
	run := runs[0]
	if run.State != state.RunFailed || !run.Retained || run.BoxID == "" {
		t.Fatalf("run = %+v, want a retained failed run", run)
	}
	box, err := st.Box(run.BoxID)
	if err != nil {
		t.Fatalf("retained box gone: %v", err)
	}
	if !box.Ephemeral || box.RetainedAt == nil {
		t.Fatalf("box = %+v, want a retained ephemeral box", box)
	}
	if hasCall(engine.Calls(), "destroy") {
		t.Fatalf("engine calls = %v, want the failed box kept", engine.Calls())
	}

	// The failure names the attach path, and the run's details offer it too.
	if !strings.Contains(errOut, "pluto attach "+state.ShortID(run.BoxID)) {
		t.Fatalf("ci stderr = %q, want the attach path", errOut)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "show", run.ID)
	if code != 0 {
		t.Fatalf("show exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "pluto attach "+state.ShortID(run.BoxID)) {
		t.Fatalf("show output = %q, want the attach path", out)
	}

	// The attach path works against the retained box.
	code, out, errOut = runCLI(t, "--socket", socket, "attach", run.BoxID, "--watch")
	if code != 0 {
		t.Fatalf("attach exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "shell") {
		t.Fatalf("attach output = %q, want the retained box's terminal", out)
	}
}

// TestCIRunSucceedsAndDiscardsItsBox is the acceptance slice for #148: a manual
// CI trigger runs one bounded execution in a fresh ephemeral box seeded from the
// workspace's provisioned package layer, records the run, and discards the box
// when the run succeeds.
func TestCIRunSucceedsAndDiscardsItsBox(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\nprovision:\n  base: ubuntu:24.04\n  packages: [jq]\n  watch: [go.sum]\ntasks:\n  test: { run: \"go test ./...\", lock: exclusive }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		_, _ = sink.Write([]byte("ok\n"))
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "ci", "test")
	if code != 0 {
		t.Fatalf("ci test exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("ci test output = %q, want the streamed step output", out)
	}

	runs, err := st.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v), want one run", runs, err)
	}
	run := runs[0]
	if run.Trigger != state.TriggerCI || run.State != state.RunDone {
		t.Fatalf("run = %+v, want a done ci run", run)
	}
	if len(run.Steps) != 1 || run.Steps[0].State != state.RunDone {
		t.Fatalf("steps = %+v, want one done step", run.Steps)
	}
	if _, err := st.Box(run.BoxID); err == nil {
		t.Fatalf("box %s still recorded after a successful CI run", run.BoxID)
	}
	if !hasCall(engine.Calls(), "destroy") {
		t.Fatalf("engine calls = %v, want the ephemeral container destroyed", engine.Calls())
	}

	// The fresh box was seeded from the provisioned package layer.
	spec, ok := engine.LastCreateSpec()
	if !ok {
		t.Fatal("no create spec recorded")
	}
	if len(spec.Layers) != 2 || spec.Layers[1].Kind != container.LayerPackage {
		t.Fatalf("create layers = %+v, want base then package", spec.Layers)
	}
}
