package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/cli"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// lockedBuffer is a bytes.Buffer safe to write from a goroutine and read from
// the test, so a follow can be observed while it is still running.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// waitForCondition polls until cond is true or the deadline passes.
func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A finished run's step output is captured to disk and `pluto logs <run>` shows
// the retained output.
func TestRunRetainsStepOutputAndLogsShowsIt(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"echo hi\" }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		_, _ = sink.Write([]byte("building\n"))
		_, _ = sink.Write([]byte("done\n"))
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
	run := runs[0]
	if len(run.Steps) != 1 || run.Steps[0].Log == "" {
		t.Fatalf("steps = %+v, want a step with a retained log reference", run.Steps)
	}
	retained, err := st.ReadRunLog(run.Steps[0].Log)
	if err != nil {
		t.Fatalf("ReadRunLog: %v", err)
	}
	if retained != "building\ndone\n" {
		t.Fatalf("retained log = %q, want the step output", retained)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "logs", run.ID)
	if code != 0 {
		t.Fatalf("logs exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"building", "done"} {
		if !strings.Contains(out, want) {
			t.Fatalf("logs output = %q, want %q", out, want)
		}
	}
}

// `pluto ls runs --json` and `pluto show <run> --json` emit a versioned schema
// carrying the record's trigger context, steps, and log references.
func TestLsRunsAndShowJSONAreVersioned(t *testing.T) {
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

	_, out, errOut := runCLI(t, "--socket", socket, "ls", "runs", "--json")
	if errOut != "" {
		t.Fatalf("ls runs --json stderr = %q", errOut)
	}
	var listing struct {
		SchemaVersion int          `json:"schema_version"`
		Runs          []*state.Run `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &listing); err != nil {
		t.Fatalf("ls runs --json output = %q: %v", out, err)
	}
	if listing.SchemaVersion != state.RunSchema {
		t.Fatalf("ls runs schema_version = %d, want %d", listing.SchemaVersion, state.RunSchema)
	}
	if len(listing.Runs) != 1 || listing.Runs[0].ID != runs[0].ID {
		t.Fatalf("ls runs --json runs = %+v, want the recorded run", listing.Runs)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "show", runs[0].ID[:8], "--json")
	if code != 0 {
		t.Fatalf("show --json exit = %d, stderr: %s", code, errOut)
	}
	var shown struct {
		SchemaVersion int        `json:"schema_version"`
		Run           *state.Run `json:"run"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("show --json output = %q: %v", out, err)
	}
	if shown.SchemaVersion != state.RunSchema || shown.Run == nil {
		t.Fatalf("show --json = %+v, want version %d and a run", shown, state.RunSchema)
	}
	if shown.Run.TriggerContext == nil || shown.Run.TriggerContext.FiredAt.IsZero() {
		t.Fatalf("show --json trigger context = %+v, want the fire time", shown.Run.TriggerContext)
	}
	if len(shown.Run.Steps) != 1 || shown.Run.Steps[0].Log == "" {
		t.Fatalf("show --json steps = %+v, want the retained log reference", shown.Run.Steps)
	}
}

// `pluto logs <run> -f` follows a live run: it shows output before the run has
// finished and drains the rest as it arrives.
func TestLogsFollowsALiveRun(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"echo hi\" }\n")
	engine := container.NewFake()
	release := make(chan struct{})
	started := make(chan struct{})
	engine.ExecFunc = func(id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		_, _ = sink.Write([]byte("starting\n"))
		close(started)
		<-release
		_, _ = sink.Write([]byte("finished\n"))
		return container.ExecResult{ExitCode: 0}, nil
	}
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)

	runDone := make(chan int, 1)
	go func() { runDone <- cli.Run([]string{"--socket", socket, "run", "dev"}, io.Discard, io.Discard) }()

	<-started
	var run *state.Run
	waitForCondition(t, "the live run record", func() bool {
		runs, err := st.Runs()
		if err != nil || len(runs) != 1 {
			return false
		}
		run = runs[0]
		return run.State == state.RunRunning
	})

	out := &lockedBuffer{}
	followDone := make(chan int, 1)
	go func() {
		followDone <- cli.Run([]string{"--socket", socket, "logs", run.ID, "-f"}, out, io.Discard)
	}()

	waitForCondition(t, "the live output before the run ends", func() bool {
		return strings.Contains(out.String(), "starting")
	})
	close(release)

	if code := <-followDone; code != 0 {
		t.Fatalf("logs -f exit = %d (%s)", code, out.String())
	}
	if code := <-runDone; code != 0 {
		t.Fatalf("run dev exit = %d", code)
	}
	got := out.String()
	for _, want := range []string{"starting", "finished", "done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("logs -f output = %q, want %q", got, want)
		}
	}
}

// Retention is one configurable window shared by records and logs: a run past
// the window is pruned (record and logs together) and disappears from the CLI,
// while a run inside the window survives.
func TestRetentionPrunesExpiredRunsAndLogs(t *testing.T) {
	repo := gitRepo(t)
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st, srv := startBoxDaemonServer(t, engine, adapter)
	t.Chdir(repo)

	now := time.Now().UTC()
	seedRun := func(finished time.Time) *state.Run {
		run := state.StartRun(state.NewID(), "dev", "b1", repo, state.TriggerManual)
		run.State = state.RunDone
		run.FinishedAt = &finished
		run.Steps = []state.Step{{Name: "run", State: state.RunDone, Log: st.RunLogRef(run.ID, 0)}}
		if _, err := st.SetRun(run); err != nil {
			t.Fatalf("SetRun: %v", err)
		}
		log, err := st.CreateRunLog(run.ID, 0)
		if err != nil {
			t.Fatalf("CreateRunLog: %v", err)
		}
		_, _ = log.Write([]byte("retained\n"))
		_ = log.Close()
		return &run
	}
	old := seedRun(now.Add(-2 * time.Hour))
	fresh := seedRun(now.Add(-10 * time.Minute))

	srv.RunRetention = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.RetentionLoop(ctx, 5*time.Millisecond)

	waitForCondition(t, "the expired run to be pruned", func() bool {
		_, err := st.Run(old.ID)
		return err != nil
	})
	if _, err := st.Run(fresh.ID); err != nil {
		t.Fatalf("fresh run was pruned: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "ls", "runs")
	if code != 0 {
		t.Fatalf("ls runs exit = %d, stderr: %s", code, errOut)
	}
	if strings.Contains(out, state.ShortID(old.ID)) {
		t.Fatalf("ls runs output = %q, want the expired run gone", out)
	}
	if !strings.Contains(out, state.ShortID(fresh.ID)) {
		t.Fatalf("ls runs output = %q, want the fresh run listed", out)
	}
}
