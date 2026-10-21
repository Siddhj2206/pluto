package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/fsutil"
	"github.com/Siddhj2206/pluto/internal/state"
)

// hostJobRunner executes a resolved job's argv on this machine, exactly where
// the box would have run it. The box lifecycle is the existing fakeRunner, so
// the test drives the daemon, the real contract, and the durable store without
// KVM or a guest agent. Output is streamed to the caller as it is produced and
// also written to the same host-side job log a real box keeps.
type hostJobRunner struct {
	*fakeRunner
}

// writerFunc adapts a function to io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func (r *hostJobRunner) RunJob(ctx context.Context, box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Box, *state.Job, error) {
	if spec.Command.IsZero() {
		return nil, nil, errors.New("no command given")
	}
	job := state.StartJobCommand(state.NewID(), spec.Command.String())
	job.Log = filepath.Join("jobs", job.ID+".log")
	if _, err := r.st.BeginJob(box.ID, job); err != nil {
		return nil, nil, err
	}
	up, err := r.fakeRunner.Up(ctx, box)
	if err != nil {
		return nil, nil, err
	}
	dir := up.Worktree
	if spec.Dir != "" {
		dir = filepath.Join(dir, spec.Dir)
	}
	argv := spec.Command.Argv()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for key, value := range spec.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var captured bytes.Buffer
	cmd.Stdout = writerFunc(func(p []byte) (int, error) {
		captured.Write(p)
		if emit != nil {
			emit(p)
		}
		return len(p), nil
	})
	cmd.Stderr = cmd.Stdout

	exitCode := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			job.Finish(state.JobFailed, 1, err.Error())
			return r.recordJob(box.ID, job, captured.Bytes())
		}
		exitCode = exitErr.ExitCode()
	}
	outcome := state.JobDone
	if exitCode != 0 {
		outcome = state.JobFailed
	}
	job.Finish(outcome, exitCode, "")
	return r.recordJob(box.ID, job, captured.Bytes())
}

// recordJob writes the job's captured output to its host-side log and saves the
// terminal record, mirroring the real runner.
func (r *hostJobRunner) recordJob(boxID string, job state.Job, output []byte) (*state.Box, *state.Job, error) {
	logPath := filepath.Join(r.st.Root(), "boxes", boxID, "jobs", job.ID+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create job log dir: %w", err)
	}
	if err := os.WriteFile(logPath, output, 0o644); err != nil {
		return nil, nil, fmt.Errorf("write job log: %w", err)
	}
	updated, err := r.st.SetJob(boxID, job)
	if err != nil {
		return nil, nil, err
	}
	return updated, &job, nil
}

func (r *hostJobRunner) JobLog(box *state.Box, jobID string, lines int) (string, error) {
	if box.Job(jobID) == nil {
		return "", fmt.Errorf("box %s has no job %s", state.ShortID(box.ID), state.ShortID(jobID))
	}
	return fsutil.TailFile(filepath.Join(r.st.Root(), "boxes", box.ID, "jobs", jobID+".log"), lines)
}

// startJobsDaemon starts a daemon backed by the host job runner.
func startJobsDaemon(t *testing.T) (socket string, st *state.Store, srv *daemon.Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	r := &hostJobRunner{fakeRunner: &fakeRunner{st: st}}
	srv = daemon.New(st, r, "test")
	socket = filepath.Join(dir, "pluto.sock")
	if err := srv.Listen(socket); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() {
		srv.Shutdown(context.Background())
		st.Close()
	})
	return socket, st, srv
}

// durableContract declares a succeeding job with multiline output and a
// failing job with a non-zero exit status.
func durableContract(t *testing.T, repo string) {
	t.Helper()
	body := `
[jobs.greet]
description = "greet the user"
command = 'printf "hello from the task\n"'

[jobs.fail]
description = "a job that fails"
command = 'printf "boom one\nboom two\n"; exit 7'
`
	if err := os.WriteFile(filepath.Join(repo, contract.FileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
}

// A declared job from a worktree contract runs on the host: discovery lists it,
// its output streams back, and its exit status is both returned and recorded.
func TestHostRunsDeclaredJobAndRecordsOutputAndExitStatus(t *testing.T) {
	socket, st, _ := startJobsDaemon(t)
	repo := gitRepo(t)
	durableContract(t, repo)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "job", "ls")
	if code != 0 {
		t.Fatalf("job ls exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"greet", "greet the user", "fail"} {
		if !strings.Contains(out, want) {
			t.Fatalf("job ls = %q, want %q", out, want)
		}
	}

	code, out, errOut = runCLI(t, "--socket", socket, "run", repo, "fail")
	if code != 7 {
		t.Fatalf("run fail exit = %d, want the job's 7 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "boom one") || !strings.Contains(out, "boom two") {
		t.Fatalf("run fail output = %q, want the streamed job output", out)
	}

	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	job := boxes[0].LatestJob()
	if job == nil || job.State != state.JobFailed || job.ExitCode != 7 {
		t.Fatalf("recorded job = %+v, want a failed job with exit 7", job)
	}

	// The recorded output is readable from the host-side log, not only the
	// stream that was already sent.
	code, out, errOut = runCLI(t, "--socket", socket, "logs", boxes[0].ID, "--job", "last")
	if code != 0 {
		t.Fatalf("logs --job last exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"boom one", "boom two"} {
		if !strings.Contains(out, want) {
			t.Fatalf("recorded log = %q, want %q", out, want)
		}
	}
}

// Durable work created from a declared job can be listed, shown, and its
// recorded output read through the task and run views.
func TestHostListsShowsAndReadsDurableTaskAndRunLogs(t *testing.T) {
	socket, st, srv := startJobsDaemon(t)
	repo := gitRepo(t)
	durableContract(t, repo)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "task", "create", "--job", "greet", "--prompt", "greet the user", "--json")
	if code != 0 {
		t.Fatalf("task create exit = %d, stderr: %s", code, errOut)
	}
	var created struct {
		Task state.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("task create output is not JSON: %v (%q)", err, out)
	}
	if created.Task.ID == "" || len(created.Task.Runs) != 1 {
		t.Fatalf("created task = %+v", created.Task)
	}
	taskID, runID := created.Task.ID, created.Task.Runs[0].ID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.SchedulerLoop(ctx, 10*time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := st.Task(taskID)
		if err == nil && task.State == state.TaskRunCompleted && task.Runs[0].JobID != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s did not complete: task=%+v err=%v", taskID, task, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "task", "ls")
	if code != 0 || !strings.Contains(out, taskID[:8]) {
		t.Fatalf("task ls: exit=%d out=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "task", "show", taskID)
	if code != 0 || !strings.Contains(out, "greet the user") || !strings.Contains(out, "completed") {
		t.Fatalf("task show: exit=%d out=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "task", "logs", taskID)
	if code != 0 || !strings.Contains(out, "hello from the task") {
		t.Fatalf("task logs: exit=%d out=%q stderr=%q", code, out, errOut)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "run", "ls")
	if code != 0 || !strings.Contains(out, runID[:8]) {
		t.Fatalf("run ls: exit=%d out=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "run", "show", runID)
	if code != 0 || !strings.Contains(out, "greet the user") || !strings.Contains(out, "completed") {
		t.Fatalf("run show: exit=%d out=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "run", "logs", runID)
	if code != 0 || !strings.Contains(out, "hello from the task") {
		t.Fatalf("run logs: exit=%d out=%q stderr=%q", code, out, errOut)
	}
}
