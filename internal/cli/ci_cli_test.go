package cli_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// TestBoxPinExemptsARetainedBoxFromEvictionThroughCLI proves pluto box pin and
// unpin exempt a retained failed CI box from eviction, through the real CLI,
// daemon, and store (#148).
func TestBoxPinExemptsARetainedBoxFromEvictionThroughCLI(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  test: { run: \"false\", lock: exclusive }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(_ string, _ container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		return container.ExecResult{ExitCode: 1}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, srv := startBoxDaemonServer(t, engine, adapter)
	t.Chdir(repo)

	for i := 0; i < 2; i++ {
		if code, _, errOut := runCLI(t, "--socket", socket, "ci", "test"); code != 1 {
			t.Fatalf("ci test #%d exit = %d, stderr: %s", i+1, code, errOut)
		}
	}
	runs, err := st.Runs()
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %v (%v), want two retained runs", runs, err)
	}
	keep, evict := runs[0].BoxID, runs[1].BoxID

	code, out, errOut := runCLI(t, "--socket", socket, "box", "pin", keep)
	if code != 0 {
		t.Fatalf("box pin exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "pinned") {
		t.Fatalf("box pin output = %q", out)
	}
	if b, err := st.Box(keep); err != nil || !b.Pinned {
		t.Fatalf("box %s not pinned: %+v (%v)", keep, b, err)
	}

	// Every retained box is past the age bound; only the pinned one survives.
	srv.RetainedMaxAge = time.Nanosecond
	srv.RetainedMaxCount = 0
	srv.BoxRetentionStep(time.Now().UTC())
	if _, err := st.Box(keep); err != nil {
		t.Fatalf("the pinned box was evicted: %v", err)
	}
	if _, err := st.Box(evict); err == nil {
		t.Fatal("the unpinned box survived eviction")
	}

	code, out, errOut = runCLI(t, "--socket", socket, "box", "unpin", keep)
	if code != 0 {
		t.Fatalf("box unpin exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "unpinned") {
		t.Fatalf("box unpin output = %q", out)
	}
	srv.BoxRetentionStep(time.Now().UTC())
	if _, err := st.Box(keep); err == nil {
		t.Fatal("the unpinned box survived eviction")
	}
}

// TestCIEphemeralBoxNeverTouchesDurableVolumes proves ephemeral execution is
// isolated from the workspace's durable box: the CI box mounts no volumes and
// the durable box's volumes are left untouched (#148).
func TestCIEphemeralBoxNeverTouchesDurableVolumes(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  test: { run: \"true\", lock: exclusive }\n")
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo, Remote: "app"}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create"); code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	durable := boxRecord(t, st, repo)
	if len(durable.Volumes) != 2 {
		t.Fatalf("durable volumes = %+v, want the workspace and home volumes", durable.Volumes)
	}
	durableNames := map[string]bool{}
	for _, v := range durable.Volumes {
		durableNames[v.Name] = true
	}

	if code, _, errOut := runCLI(t, "--socket", socket, "ci", "test"); code != 0 {
		t.Fatalf("ci test exit = %d, stderr: %s", code, errOut)
	}

	specs := engine.CreateSpecs()
	if len(specs) != 2 {
		t.Fatalf("create specs = %d, want the durable box and the ephemeral box", len(specs))
	}
	ephemeral := specs[1]
	if len(ephemeral.Volumes) != 0 {
		t.Fatalf("ephemeral create volumes = %+v, want none", ephemeral.Volumes)
	}
	// The durable box's volumes survive, and no ephemeral create reused one.
	held := map[string]bool{}
	for _, v := range engine.Volumes() {
		held[v.Name] = true
	}
	for name := range durableNames {
		if !held[name] {
			t.Fatalf("durable volume %s was removed", name)
		}
	}
	// The ephemeral box mounted no volumes, so it cannot have reused one.
	if ephemeral.BoxID == durable.ID {
		t.Fatalf("the CI run reused the durable box %s", durable.ID)
	}
}

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
