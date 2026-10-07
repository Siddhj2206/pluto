package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/cli"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
)

// fakeRunner stands in for the box lifecycle: it moves records through the
// same states the real runner would. run scripts a job and receives the
// resolved spec; without one, runs succeed silently.
type fakeRunner struct {
	st             *state.Store
	run            func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error)
	upErr          error
	jobLog         string
	jobLogID       *string // records the ID JobLog was asked for
	logErr         error
	window         time.Duration
	clients        int
	unknownClients bool
	services       []state.ServiceStatus
	sessions       []state.SessionStatus
	remotes        []state.Remote
	refreshErr     error
	stale          bool
}

func (f fakeRunner) Up(ctx context.Context, box *state.Box) (*state.Box, error) {
	if f.upErr != nil {
		return nil, f.upErr
	}
	if f.remotes != nil {
		if _, err := f.st.SetRemotes(box.ID, f.remotes); err != nil {
			return nil, err
		}
	}
	return f.st.Transition(box.ID, state.StateRunning)
}

func (f fakeRunner) Pause(box *state.Box) (*state.Box, error) {
	return f.st.Transition(box.ID, state.StatePaused)
}

func (f fakeRunner) Attach(ctx context.Context, box *state.Box) (api.AttachInfo, error) {
	return api.AttachInfo{User: "dev", UDS: "/tmp/v.sock", Key: "/tmp/id", Port: 22}, nil
}

func (f fakeRunner) Reconcile(box *state.Box) (*state.Box, error) { return box, nil }

func (f fakeRunner) Refresh(box *state.Box) (*state.Box, error) {
	if f.refreshErr != nil {
		return box, f.refreshErr
	}
	if f.unknownClients {
		return f.st.SetPhases(box.ID, state.Phases{Synced: true})
	}
	n := f.clients
	return f.st.SetPhases(box.ID, state.Phases{Synced: true, Clients: &n, Services: f.services, Sessions: f.sessions})
}

func (f fakeRunner) Logs(box *state.Box, phase, service string, lines int) (string, error) {
	if f.logErr != nil {
		return "", f.logErr
	}
	if box.State != state.StateRunning {
		return "", fmt.Errorf("box %s is not running; start it with 'pluto up'", state.ShortID(box.ID))
	}
	return "log of " + phase + service, nil
}

func (f fakeRunner) RunJob(ctx context.Context, box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Box, *state.Job, error) {
	if f.run != nil {
		job, err := f.run(box, spec, emit)
		if err != nil {
			return nil, nil, err
		}
		return box, job, nil
	}
	job := state.StartJobCommand(state.NewID(), spec.Command.String())
	job.Finish(state.JobDone, 0, "")
	return box, &job, nil
}

func (f fakeRunner) JobLog(box *state.Box, jobID string, lines int) (string, error) {
	if f.jobLogID != nil {
		*f.jobLogID = jobID
	}
	if f.jobLog != "" {
		return f.jobLog, nil
	}
	return "job log of " + jobID, nil
}

func (f fakeRunner) AutoPauseWindow(box *state.Box) time.Duration { return f.window }

func (f fakeRunner) ContractStale(box *state.Box) bool { return f.stale }

func (f fakeRunner) Destroy(id string) error { return f.st.DestroyBox(id) }

func (f fakeRunner) Import(srcDir string) (string, error) { return "ver123", nil }

func (f fakeRunner) Images() ([]api.ImageInfo, error) {
	return []api.ImageInfo{{Version: "ver123"}}, nil
}

func (f fakeRunner) Metrics(box *state.Box) (json.RawMessage, error) {
	return nil, os.ErrNotExist
}

func startDaemon(t *testing.T) (socket string, st *state.Store) {
	return startDaemonWith(t, fakeRunner{})
}

func startDaemonWith(t *testing.T, fr fakeRunner) (socket string, st *state.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	fr.st = st
	srv := daemon.New(st, fr, "test")
	socket = filepath.Join(dir, "pluto.sock")
	if err := srv.Listen(socket); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() {
		srv.Shutdown(context.Background())
		st.Close()
	})
	return socket, st
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("set branch: %v (%s)", err, out)
	}
	return dir
}

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = cli.Run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

// recordJob seeds a finished job straight into the store, so CLI tests can
// exercise history without driving the daemon's runner.
func recordJob(t *testing.T, st *state.Store, boxID, command string, exit int, durationMS int64) state.Job {
	t.Helper()
	job := state.StartJob(state.NewID(), strings.Fields(command))
	if _, err := st.BeginJob(boxID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	outcome := state.JobDone
	if exit != 0 {
		outcome = state.JobFailed
	}
	job.Finish(outcome, exit, "")
	job.DurationMS = durationMS // pin a deterministic duration for the listing
	if _, err := st.SetJob(boxID, job); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	return job
}

func TestUpLsStatusDestroy(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)

	code, out, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo)
	if code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "created box") {
		t.Fatalf("up output = %q, want 'created box'", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "up", "--worktree", repo)
	if code != 0 {
		t.Fatalf("second up exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "already exists") {
		t.Fatalf("second up output = %q, want 'already exists'", out)
	}
	if !strings.Contains(out, "running") {
		t.Fatalf("second up output = %q, want running", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "ls")
	if code != 0 {
		t.Fatalf("ls exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "main") || !strings.Contains(out, "running") {
		t.Fatalf("ls output = %q, want branch and running state", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, repo) || !strings.Contains(out, "running") {
		t.Fatalf("status output = %q, want worktree and running state", out)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "destroy", repo)
	if code == 0 {
		t.Fatal("destroy without --yes in a non-interactive session should fail")
	}
	if !strings.Contains(errOut, "--yes") {
		t.Fatalf("destroy stderr = %q, want a --yes hint", errOut)
	}

	code, out, _ = runCLI(t, "--socket", socket, "ls")
	if !strings.Contains(out, "main") {
		t.Fatal("box should still exist after a refused destroy")
	}

	code, out, errOut = runCLI(t, "--socket", socket, "destroy", repo, "--yes")
	if code != 0 {
		t.Fatalf("destroy --yes exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "destroyed") {
		t.Fatalf("destroy output = %q, want 'destroyed'", out)
	}

	code, out, _ = runCLI(t, "--socket", socket, "ls")
	if strings.Contains(out, "main") {
		t.Fatalf("box still listed after destroy: %q", out)
	}
}

func TestLsWithoutDaemonIsLegible(t *testing.T) {
	code, _, errOut := runCLI(t, "--socket", filepath.Join(t.TempDir(), "missing.sock"), "ls")
	if code == 0 {
		t.Fatal("ls should fail without a daemon")
	}
	if !strings.Contains(errOut, "daemon") {
		t.Fatalf("stderr = %q, want a daemon hint", errOut)
	}
}

func TestUpOutsideGitWorktreeFails(t *testing.T) {
	socket, _ := startDaemon(t)
	code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", t.TempDir())
	if code == 0 {
		t.Fatal("up outside a git worktree should fail")
	}
	if !strings.Contains(errOut, "git") {
		t.Fatalf("stderr = %q, want a git hint", errOut)
	}
}

func TestPauseFlow(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	code, out, errOut := runCLI(t, "--socket", socket, "pause", repo)
	if code != 0 {
		t.Fatalf("pause exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "paused") {
		t.Fatalf("pause output = %q, want paused", out)
	}
	code, out, _ = runCLI(t, "--socket", socket, "ls")
	if code != 0 || !strings.Contains(out, "paused") {
		t.Fatalf("ls after pause = %q, want paused state", out)
	}
}

func TestImageCommands(t *testing.T) {
	socket, _ := startDaemon(t)
	code, out, errOut := runCLI(t, "--socket", socket, "image", "import", t.TempDir())
	if code != 0 {
		t.Fatalf("image import exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "ver123") {
		t.Fatalf("image import output = %q, want the version", out)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "image", "ls")
	if code != 0 {
		t.Fatalf("image ls exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "ver123") {
		t.Fatalf("image ls output = %q, want the version", out)
	}
}

func TestLogsCommand(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "logs", repo)
	if code != 0 {
		t.Fatalf("logs exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "== provision ==") || !strings.Contains(out, "== wake ==") {
		t.Fatalf("logs output = %q, want both phase sections", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "logs", repo, "--service", "web")
	if code != 0 {
		t.Fatalf("service logs exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "== service web ==") {
		t.Fatalf("service logs output = %q", out)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "logs", repo, "--phase", "bogus")
	if code != 2 || !strings.Contains(errOut, "unknown phase") {
		t.Fatalf("bad phase: exit %d, stderr %q", code, errOut)
	}
}

func TestDestroyByIDRepairsCorruptRecord(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	id := boxes[0].ID

	record := filepath.Join(st.Root(), "boxes", id, "box.json")
	if err := os.WriteFile(record, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("corrupt record: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "destroy", id, "--yes")
	if code != 0 {
		t.Fatalf("destroy corrupt record exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "destroyed") {
		t.Fatalf("output = %q, want 'destroyed'", out)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), "boxes", id)); !os.IsNotExist(err) {
		t.Fatalf("box dir still present: %v", err)
	}
}

func TestBoxTargetAcceptsIDPrefix(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	prefix := boxes[0].ID[:8]
	code, out, errOut := runCLI(t, "--socket", socket, "status", prefix)
	if code != 0 {
		t.Fatalf("status by id prefix exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, boxes[0].Worktree) {
		t.Fatalf("status output = %q, want the box's worktree", out)
	}
}

// ADR 0011: an ambiguous box id prefix is a failure that names how to
// disambiguate, not a fall-through to the generic daemon-log hint.
func TestAmbiguousBoxIDPrefixNamesTheNextStep(t *testing.T) {
	socket, st := startDaemon(t)
	for _, repo := range []string{gitRepo(t), gitRepo(t)} {
		if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
			t.Fatalf("up exit %d: %s", code, errOut)
		}
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 2 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	// Pin both records to ids sharing the first eight characters, so `status`
	// cannot pick one.
	for i, box := range boxes {
		oldID := box.ID
		box.ID = fmt.Sprintf("aaaaaaaa-0000-4000-8000-00000000000%d", i)
		data, err := json.Marshal(box)
		if err != nil {
			t.Fatalf("marshal box: %v", err)
		}
		if err := os.WriteFile(filepath.Join(st.Root(), "boxes", oldID, "box.json"), append(data, '\n'), 0o644); err != nil {
			t.Fatalf("rewrite box record: %v", err)
		}
	}

	code, _, errOut := runCLI(t, "--socket", socket, "status", "aaaaaaaa")
	if code != 1 {
		t.Fatalf("ambiguous prefix exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{
		"matches more than one box",
		"next: use a longer prefix, or list boxes with 'pluto ls'",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
	if strings.Contains(errOut, "journalctl") {
		t.Fatalf("stderr = %q, want the specific hint, not the daemon-log fallback", errOut)
	}
}

// `run` must accept the short ids `ls` and `status` print, for both the
// declared-job and ad-hoc spellings, resolving to the same box as the full id.
func TestRunAcceptsShortAndFullBoxIDs(t *testing.T) {
	var ranOn string
	socket, st := startDaemonWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		ranOn = box.ID
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	repo := gitRepo(t)
	writeJobContract(t, repo)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	id := boxes[0].ID
	prefix := id[:8]

	// Run from outside any git worktree, so only id resolution can succeed;
	// a short id falling through to worktree resolution would fail here.
	t.Chdir(t.TempDir())

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"short id, ad-hoc", []string{"run", prefix, "--", "true"}},
		{"short id, named job", []string{"run", prefix, "dev"}},
		{"full id, ad-hoc", []string{"run", id, "--", "true"}},
		{"full id, named job", []string{"run", id, "dev"}},
	} {
		ranOn = ""
		code, _, errOut := runCLI(t, append([]string{"--socket", socket}, tc.args...)...)
		if code != 0 {
			t.Fatalf("%s exit = %d, stderr: %s", tc.name, code, errOut)
		}
		if ranOn != id {
			t.Fatalf("%s ran on box %s, want %s", tc.name, state.ShortID(ranOn), state.ShortID(id))
		}
	}
}

func TestRunCommandStreamsOutputAndReturnsExitCode(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		emit([]byte("building\n"))
		emit([]byte("failed\n"))
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		now := time.Now().UTC()
		job.State = state.JobFailed
		job.ExitCode = 3
		job.FinishedAt = &now
		return &job, nil
	}})
	repo := gitRepo(t)

	code, out, errOut := runCLI(t, "--socket", socket, "run", repo, "--", "make", "test")
	if code != 3 {
		t.Fatalf("run exit = %d, want the command's 3 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "building") || !strings.Contains(out, "failed") {
		t.Fatalf("run output = %q, want the streamed chunks", out)
	}
}

func TestRunListsDeclaredJobs(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "run")
	if code != 0 {
		t.Fatalf("run list exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"dev", "start the dev server", "test", "run the test suite"} {
		if !strings.Contains(out, want) {
			t.Fatalf("run list = %q, want %q", out, want)
		}
	}
}

func TestRunListsDeclaredJobsFromASubdirectory(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)
	sub := filepath.Join(repo, "web", "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(sub)

	code, out, errOut := runCLI(t, "--socket", socket, "run")
	if code != 0 {
		t.Fatalf("run list exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "dev") || !strings.Contains(out, "start the dev server") {
		t.Fatalf("run list = %q, want the worktree root's jobs", out)
	}
}

func TestRunListsNoJobsClearly(t *testing.T) {
	socket, _ := startDaemon(t)
	t.Chdir(t.TempDir())

	code, out, errOut := runCLI(t, "--socket", socket, "run")
	if code != 0 {
		t.Fatalf("run list exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "no jobs declared") || !strings.Contains(out, ".pluto.toml") {
		t.Fatalf("run list = %q, want the no-jobs note", out)
	}
}

func TestRunNamedJob(t *testing.T) {
	var got contract.Exec
	socket, _ := startDaemonWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		got = spec
		emit([]byte("dev up\n"))
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	repo := gitRepo(t)
	writeJobContract(t, repo)

	code, out, errOut := runCLI(t, "--socket", socket, "run", repo, "dev")
	if code != 0 {
		t.Fatalf("run dev exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "dev up") {
		t.Fatalf("run output = %q, want the streamed output", out)
	}
	if strings.Join(got.Command.Argv(), " ") != "pnpm dev" || got.Dir != "web" {
		t.Fatalf("spec = %+v, want the declared job", got)
	}
	if got.Env["NODE_ENV"] != "development" {
		t.Fatalf("spec env = %v, want the top-level env", got.Env)
	}
}

func TestRunNamedJobFromTheCurrentWorktree(t *testing.T) {
	var got contract.Exec
	socket, _ := startDaemonWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		got = spec
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	repo := gitRepo(t)
	writeJobContract(t, repo)
	t.Chdir(repo)

	code, _, errOut := runCLI(t, "--socket", socket, "run", "test")
	if code != 0 {
		t.Fatalf("run test exit = %d, stderr: %s", code, errOut)
	}
	if got.Command.String() != "pnpm test" {
		t.Fatalf("spec = %+v, want the declared test job", got)
	}
}

func TestRunUnknownJobListsJobsAndAdHocSpelling(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)

	code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "web")
	if code == 0 {
		t.Fatal("an unknown job must fail")
	}
	for _, want := range []string{"no such job", "dev (start the dev server)", "next:", "'pluto run'", "-- <command>"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

func TestRunTargetOnlyIsAJobName(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)
	t.Chdir(repo)

	// The old target-only spelling is now a lone job name; the error lists
	// the declared jobs and shows the ad-hoc spelling with the target.
	code, _, errOut := runCLI(t, "--socket", socket, "run", repo)
	if code == 0 {
		t.Fatal("a lone target argument is a job name and must fail")
	}
	for _, want := range []string{"no such job", "dev (start the dev server)", "pluto run " + repo + " -- <command>"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
	// The box for the worktree is still resolved (and created) before the
	// name fails, matching the running path.
	if code, out, _ := runCLI(t, "--socket", socket, "ls"); code != 0 || !strings.Contains(out, "main") {
		t.Fatalf("ls = %q, want the box the run created", out)
	}
}

func TestRunAdHocStillWorksFromTheCurrentWorktree(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)
	t.Chdir(repo)

	code, _, errOut := runCLI(t, "--socket", socket, "run", "--", "true")
	if code != 0 {
		t.Fatalf("ad-hoc run exit = %d, stderr: %s", code, errOut)
	}
}

// '--' fences the work's argv: a literal --async after the separator belongs to
// the command, not to pluto (ADR 0011).
func TestRunFencesAsyncAfterDashDash(t *testing.T) {
	var got contract.Exec
	socket, _ := startDaemonWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		got = spec
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	repo := gitRepo(t)

	code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "--", "tool", "--async")
	if code != 0 {
		t.Fatalf("run exit = %d, stderr: %s", code, errOut)
	}
	if args := got.Command.Argv(); strings.Join(args, " ") != "tool --async" {
		t.Fatalf("argv = %v, want the literal --async passed to the tool", args)
	}

	// A --async before the separator is still pluto's flag and queues the run.
	code, out, errOut := runCLI(t, "--socket", socket, "run", repo, "--async", "--", "tool")
	if code != 0 || !strings.Contains(out, "queued") {
		t.Fatalf("async run: exit=%d out=%q stderr=%q, want a queued request", code, out, errOut)
	}
}

// writeJobContract writes a v2 contract with two declared jobs.
func writeJobContract(t *testing.T, repo string) {
	t.Helper()
	body := `
[env]
NODE_ENV = "development"

[jobs.dev]
description = "start the dev server"
command = ["pnpm", "dev"]
dir = "web"

[jobs.test]
description = "run the test suite"
command = "pnpm test"
`
	if err := os.WriteFile(filepath.Join(repo, ".pluto.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
}

func TestRunCommandCreatesTheBox(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)

	code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "--", "true")
	if code != 0 {
		t.Fatalf("run exit = %d, stderr: %s", code, errOut)
	}
	code, out, _ := runCLI(t, "--socket", socket, "ls")
	if code != 0 || !strings.Contains(out, "main") {
		t.Fatalf("ls = %q, want the box run created", out)
	}
}

func TestRunRefusedWhileJobRuns(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		return nil, fmt.Errorf("%w: %q", state.ErrJobRunning, "make")
	}})
	repo := gitRepo(t)

	code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "--", "make")
	if code == 0 {
		t.Fatal("a refused run must fail")
	}
	if !strings.Contains(errOut, "already running") {
		t.Fatalf("stderr = %q, want the refusal reason", errOut)
	}
}

func TestRunRequiresACommand(t *testing.T) {
	socket, _ := startDaemon(t)
	code, _, errOut := runCLI(t, "--socket", socket, "run", "--")
	if code != 2 || !strings.Contains(errOut, "usage") {
		t.Fatalf("exit = %d, stderr = %q, want usage", code, errOut)
	}
	code, _, errOut = runCLI(t, "--socket", socket, "run", "a", "b", "c")
	if code != 2 || !strings.Contains(errOut, "usage") {
		t.Fatalf("too many args: exit = %d, stderr = %q, want usage", code, errOut)
	}
}

func TestStatusShowsJobOutcome(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	job := state.StartJob(state.NewID(), []string{"make", "test"})
	if _, err := st.BeginJob(boxes[0].ID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	now := time.Now().UTC()
	job.State = state.JobFailed
	job.ExitCode = 2
	job.FinishedAt = &now
	job.DurationMS = 1234
	if _, err := st.SetJob(boxes[0].ID, job); err != nil {
		t.Fatalf("SetJob: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	for _, want := range []string{"make test", "exit 2", "failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output = %q, want %q", out, want)
		}
	}
}

func TestStatusShowsServiceDescriptions(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{
		window:   30 * time.Minute,
		services: []state.ServiceStatus{{Name: "web", State: "active", Port: 3000, Description: "web UI"}},
	})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "service:  web active (port 3000) - web UI") {
		t.Fatalf("status output = %q, want the service description", out)
	}
}

func TestStatusShowsDeclaredSessions(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{
		sessions: []state.SessionStatus{
			{Name: "agent", State: "running", Attached: true, Description: "the coding agent"},
			{Name: "worker", State: "stopped"},
		},
	})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	for _, want := range []string{
		"session:  agent running (attached) - the coding agent",
		"session:  worker stopped",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output = %q, want %q", out, want)
		}
	}
}

func TestStatusShowsLocalOnlyWhenThereAreNoRemotes(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "remotes:  none (local-only)") {
		t.Fatalf("status output = %q, want the local-only remotes line", out)
	}
	if !strings.Contains(out, "push:     unavailable") {
		t.Fatalf("status output = %q, want a push line saying pushing is unavailable", out)
	}
}

func TestStatusShowsEveryRemoteAndTheTrackedOne(t *testing.T) {
	remotes := []state.Remote{
		{Name: "origin", Fetch: "https://example.com/acme/app.git"},
		{Name: "fork", Fetch: "git@example.com:me/app.git"},
	}
	socket, _ := startDaemonWith(t, fakeRunner{remotes: remotes})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	for _, want := range []string{
		"remotes:  origin https://example.com/acme/app.git (tracked)",
		"remotes:  fork git@example.com:me/app.git (SSH; pushing over SSH is unavailable until M4)",
		"push:     origin (current branch)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output = %q, want %q", out, want)
		}
	}
}

func TestStatusShowsAnAmbiguousBranchUntracked(t *testing.T) {
	remotes := []state.Remote{
		{Name: "upstream", Fetch: "https://example.com/org/app.git"},
		{Name: "fork", Fetch: "https://example.com/me/app.git"},
	}
	socket, _ := startDaemonWith(t, fakeRunner{remotes: remotes})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "push:     branch \"main\" is untracked (several remotes, none named origin)") {
		t.Fatalf("status output = %q, want the untracked-branch line", out)
	}
	if strings.Contains(out, "(tracked)") {
		t.Fatalf("status output = %q, want no remote marked tracked", out)
	}
}

func TestUpWarnsOnceWhenTheWorktreeIsLocalOnly(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{})
	repo := gitRepo(t)

	_, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo)
	if !strings.Contains(errOut, "warning:") || !strings.Contains(errOut, "pushing from the box is unavailable") {
		t.Fatalf("up stderr = %q, want a local-only warning", errOut)
	}
	// The warning is a first-boot message: a second up stays quiet.
	_, _, errOut = runCLI(t, "--socket", socket, "up", "--worktree", repo)
	if errOut != "" {
		t.Fatalf("second up stderr = %q, want no warning on an existing box", errOut)
	}
}

func TestUpWarnsAboutAnSSHRemote(t *testing.T) {
	remotes := []state.Remote{
		{Name: "origin", Fetch: "https://example.com/acme/app.git"},
		{Name: "fork", Fetch: "git@example.com:me/app.git"},
	}
	socket, _ := startDaemonWith(t, fakeRunner{remotes: remotes})
	repo := gitRepo(t)

	_, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo)
	if !strings.Contains(errOut, `remote "fork" is SSH`) || !strings.Contains(errOut, "M4") {
		t.Fatalf("up stderr = %q, want the SSH warning for fork", errOut)
	}
	if strings.Contains(errOut, "pushing from the box is unavailable") {
		t.Fatalf("up stderr = %q, want no local-only warning when a remote exists", errOut)
	}
}

func TestUpWarnsAboutAnAmbiguousUntrackedBranch(t *testing.T) {
	remotes := []state.Remote{
		{Name: "upstream", Fetch: "https://example.com/org/app.git"},
		{Name: "fork", Fetch: "https://example.com/me/app.git"},
	}
	socket, _ := startDaemonWith(t, fakeRunner{remotes: remotes})
	repo := gitRepo(t)

	_, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo)
	if !strings.Contains(errOut, "untracked") || !strings.Contains(errOut, "none named origin") {
		t.Fatalf("up stderr = %q, want the ambiguous-branch warning", errOut)
	}
}

func TestStatusFlagsAContractChangedSinceHandoff(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{stale: true})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "contract: changed since this box applied it") {
		t.Fatalf("status output = %q, want the stale-contract line", out)
	}
}

func TestStatusStaysQuietWhenTheContractMatches(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if strings.Contains(out, "contract:") {
		t.Fatalf("status output = %q, want no contract line for a matching contract", out)
	}
}

func TestStatusShowsAutoPauseIdleWindow(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{window: 30 * time.Minute})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	for _, want := range []string{"auto-pause:", "idle", "30m0s", "pauses in"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output = %q, want %q", out, want)
		}
	}
}

func TestStatusShowsAutoPauseBlockedByClient(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{window: 30 * time.Minute, clients: 1})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "auto-pause: blocked (client attached)") {
		t.Fatalf("status output = %q, want the attached-client reason", out)
	}
}

func TestStatusShowsAutoPauseBlockedByJob(t *testing.T) {
	socket, st := startDaemonWith(t, fakeRunner{window: 30 * time.Minute})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	if _, err := st.BeginJob(boxes[0].ID, state.StartJob(state.NewID(), []string{"make"})); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "auto-pause: blocked (job running)") {
		t.Fatalf("status output = %q, want the running-job reason", out)
	}
}

func TestStatusShowsAutoPauseOff(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{window: 0})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "auto-pause: off") {
		t.Fatalf("status output = %q, want auto-pause off", out)
	}
}

func TestStatusShowsAutoPauseUnknownClientState(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{window: 30 * time.Minute, unknownClients: true})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "auto-pause: blocked (client state unknown)") {
		t.Fatalf("status output = %q, want the unknown-client reason", out)
	}
}

func TestStatusShowsAutoPauseNoLiveView(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{window: 30 * time.Minute, refreshErr: fmt.Errorf("agent unreachable")})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "auto-pause: blocked (no live view)") {
		t.Fatalf("status output = %q, want the no-live-view reason", out)
	}
}

func TestStatusOmitsAutoPauseForPausedBoxes(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "pause", repo); code != 0 {
		t.Fatalf("pause exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if strings.Contains(out, "auto-pause:") {
		t.Fatalf("status output = %q, want no auto-pause line for a paused box", out)
	}
}

func TestLogsOnPausedBoxShowsTheRecordedJob(t *testing.T) {
	socket, st := startDaemonWith(t, fakeRunner{jobLog: "job says hi\n"})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	job := state.StartJob(state.NewID(), []string{"make"})
	if _, err := st.BeginJob(boxes[0].ID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	job.Finish(state.JobDone, 0, "")
	if _, err := st.SetJob(boxes[0].ID, job); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "pause", repo); code != 0 {
		t.Fatalf("pause exit %d: %s", code, errOut)
	}

	// Phase logs live in the guest and are unreachable while paused; the job
	// log is on the host and must still be shown.
	code, out, errOut := runCLI(t, "--socket", socket, "logs", repo)
	if code != 0 {
		t.Fatalf("logs exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "job says hi") {
		t.Fatalf("logs output = %q, want the recorded job log", out)
	}
	if !strings.Contains(errOut, "provision") {
		t.Fatalf("logs stderr = %q, want a note about the unavailable phase log", errOut)
	}
}

func TestLogsOnRunningBoxFailsWhenTheAgentCannotAnswer(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{logErr: fmt.Errorf("agent unreachable")})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	// A running box with an unreachable agent is a real failure, not a
	// paused-box degradation.
	code, _, errOut := runCLI(t, "--socket", socket, "logs", repo)
	if code == 0 {
		t.Fatalf("logs exit = 0, want a failure (stderr %q)", errOut)
	}
	if !strings.Contains(errOut, "agent unreachable") {
		t.Fatalf("logs stderr = %q, want the agent error", errOut)
	}
}

func TestLogsShowJobOutput(t *testing.T) {
	socket, st := startDaemonWith(t, fakeRunner{jobLog: "job says hi\n"})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, _ := st.Boxes()
	job := state.StartJob(state.NewID(), []string{"make"})
	if _, err := st.BeginJob(boxes[0].ID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	now := time.Now().UTC()
	job.State = state.JobDone
	job.FinishedAt = &now
	if _, err := st.SetJob(boxes[0].ID, job); err != nil {
		t.Fatalf("SetJob: %v", err)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "logs", repo, "--job", "last")
	if code != 0 {
		t.Fatalf("logs exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "== job") || !strings.Contains(out, "job says hi") {
		t.Fatalf("logs output = %q, want the job section", out)
	}

	// The default view also shows the latest job's output.
	code, out, _ = runCLI(t, "--socket", socket, "logs", repo)
	if code != 0 || !strings.Contains(out, "job says hi") {
		t.Fatalf("default logs = %q, want the job output too", out)
	}
}

func TestJobsCommandListsRecentRuns(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	first := recordJob(t, st, boxes[0].ID, "make test", 0, 1500)
	second := recordJob(t, st, boxes[0].ID, "make lint", 2, 250)

	code, out, errOut := runCLI(t, "--socket", socket, "jobs", repo)
	if code != 0 {
		t.Fatalf("jobs exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{
		"ID", "STATE", "EXIT", "DURATION", "STARTED", "COMMAND",
		state.ShortID(second.ID), "make lint", "failed", "250ms",
		state.ShortID(first.ID), "make test", "done", "1.5s",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("jobs output = %q, want %q", out, want)
		}
	}
	// Newest first: the lint run heads the list.
	if strings.Index(out, "make lint") > strings.Index(out, "make test") {
		t.Fatalf("jobs output = %q, want the newest run first", out)
	}
}

func TestJobsCommandWithoutHistory(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "jobs", repo)
	if code != 0 {
		t.Fatalf("jobs exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "ID") || !strings.Contains(out, "COMMAND") {
		t.Fatalf("jobs output = %q, want the header", out)
	}
}

// TestLogsJobResolvesRetainedHistory pins --job resolution against the whole
// retained history: an old id, an unambiguous prefix, and "last".
func TestLogsJobResolvesRetainedHistory(t *testing.T) {
	var logged string
	socket, st := startDaemonWith(t, fakeRunner{jobLogID: &logged})
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	first := recordJob(t, st, boxes[0].ID, "first job", 0, 0)
	second := recordJob(t, st, boxes[0].ID, "second job", 0, 0)

	code, out, errOut := runCLI(t, "--socket", socket, "logs", repo, "--job", first.ID[:8])
	if code != 0 {
		t.Fatalf("logs by prefix exit = %d, stderr: %s", code, errOut)
	}
	if logged != first.ID {
		t.Fatalf("daemon got job id %s, want the older %s", logged, first.ID)
	}
	if !strings.Contains(out, "job log of "+first.ID) {
		t.Fatalf("logs output = %q, want the older job's output", out)
	}

	if code, _, errOut := runCLI(t, "--socket", socket, "logs", repo, "--job", "last"); code != 0 {
		t.Fatalf("logs last exit = %d, stderr: %s", code, errOut)
	}
	if logged != second.ID {
		t.Fatalf("daemon got job id %s, want the newest %s", logged, second.ID)
	}

	if code, _, errOut := runCLI(t, "--socket", socket, "logs", repo, "--job", first.ID); code != 0 {
		t.Fatalf("logs by exact id exit = %d, stderr: %s", code, errOut)
	}
	if logged != first.ID {
		t.Fatalf("daemon got job id %s, want the exact %s", logged, first.ID)
	}

	code, _, errOut = runCLI(t, "--socket", socket, "logs", repo, "--job", "deadbeef")
	if code == 0 {
		t.Fatal("logs for an unknown job should fail")
	}
	if !strings.Contains(errOut, "no job") {
		t.Fatalf("logs stderr = %q, want a no-job message", errOut)
	}
}

func TestStatusShowsTheLatestJobFromHistory(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	recordJob(t, st, boxes[0].ID, "old run", 0, 0)
	latest := recordJob(t, st, boxes[0].ID, "new run", 6, 0)

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "job:") || !strings.Contains(out, latest.Command) {
		t.Fatalf("status output = %q, want the latest job", out)
	}
	if strings.Contains(out, "old run") {
		t.Fatalf("status output = %q, want only the latest job", out)
	}
}

func TestSetupGitHubGuidesEventToDeclaredJobMappings(t *testing.T) {
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, contract.FileName), []byte("[jobs.test]\ncommand = 'go test ./...'\n[jobs.lint]\ncommand = 'golangci-lint run'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.WriteString("push,pull_request,issues\ntest\nlint\n\ntest\nopened,labeled\n"); err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	oldStdin := os.Stdin
	os.Stdin = read
	t.Cleanup(func() { os.Stdin = oldStdin; _ = read.Close() })
	code, out, errOut := runCLI(t, "setup", "github")
	if code != 0 {
		t.Fatalf("setup github exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	configured, err := contract.Load(repo)
	if err != nil {
		t.Fatalf("generated contract: %v", err)
	}
	if configured.Events.Push == nil || configured.Events.Push.Job != "test" ||
		configured.Events.PullRequest == nil || configured.Events.PullRequest.Job != "lint" || !configured.Events.PullRequest.Allows("synchronize") ||
		configured.Events.Issue == nil || configured.Events.Issue.Job != "test" || !configured.Events.Issue.Allows("labeled") {
		t.Fatalf("generated GitHub event mappings = %+v", configured.Events)
	}
	if !strings.Contains(out, "Declared jobs:") || !strings.Contains(out, "approve this exact contract revision") {
		t.Fatalf("setup guidance = %q", out)
	}
}
