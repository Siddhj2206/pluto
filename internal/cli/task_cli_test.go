package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// committedRepo returns a git worktree with a committed contract so change
// inspection has a HEAD to diff against.
func committedRepo(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".pluto.toml"), []byte("[jobs.agent]\ncommand = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"add", ".pluto.toml"},
		{"commit", "-m", "contract"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	return dir
}

func approveTestContract(t *testing.T, st *state.Store, repo string) {
	t.Helper()
	ct, err := contract.Load(repo)
	if err != nil {
		t.Fatalf("load test contract: %v", err)
	}
	if _, err := st.ApproveContract(filepath.Base(repo), ct.Hash(), time.Now()); err != nil {
		t.Fatalf("approve test contract: %v", err)
	}
}

func TestTaskCreateStartsDurableTaskAndPrintsVersionedJSON(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".pluto.toml"), []byte("[jobs.agent]\ncommand = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	approveTestContract(t, st, repo)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "task", "create", "--job", "agent", "--prompt", "fix the failing test", "--json")
	if code != 0 {
		t.Fatalf("task create exit = %d: %s", code, errOut)
	}
	var response struct {
		SchemaVersion int        `json:"schema_version"`
		Task          state.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("task create output is not one JSON document: %v (%q)", err, out)
	}
	if response.SchemaVersion != 1 || response.Task.ID == "" || response.Task.State != state.TaskAccepted || len(response.Task.Runs) != 1 || response.Task.Runs[0].State != state.TaskRunQueued {
		t.Fatalf("task create response = %+v, want schema 1 with an accepted task and one queued run", response)
	}
	if response.Task.Runs[0].Prompt != "fix the failing test" || response.Task.Runs[0].Job != "agent" {
		t.Fatalf("first run = %+v", response.Task.Runs[0])
	}
	if strings.TrimSpace(errOut) != "" {
		t.Fatalf("task create stderr = %q", errOut)
	}
}

func TestTaskCanBeFoundInspectedAndFollowedThroughCLI(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".pluto.toml"), []byte("[jobs.agent]\ncommand = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	approveTestContract(t, st, repo)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "task", "create", "--job", "agent", "--prompt", "make the first change", "--json")
	if code != 0 {
		t.Fatalf("task create exit = %d: %s", code, errOut)
	}
	var created struct {
		Task state.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	shortID := created.Task.ID[:8]

	code, out, errOut = runCLI(t, "--socket", socket, "task", "ls", "--json")
	if code != 0 || !strings.Contains(out, created.Task.ID) {
		t.Fatalf("task ls: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "task", "show", shortID)
	if code != 0 || !strings.Contains(out, "make the first change") || !strings.Contains(out, "queued") {
		t.Fatalf("task show: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "task", "follow", shortID, "--prompt", "add a regression test", "--json")
	if code != 0 {
		t.Fatalf("task follow exit = %d: %s", code, errOut)
	}
	var followed struct {
		SchemaVersion int           `json:"schema_version"`
		TaskID        string        `json:"task_id"`
		Run           state.TaskRun `json:"run"`
	}
	if err := json.Unmarshal([]byte(out), &followed); err != nil {
		t.Fatalf("task follow output is not JSON: %v (%q)", err, out)
	}
	if followed.SchemaVersion != 1 || followed.TaskID != created.Task.ID || followed.Run.State != state.TaskRunQueued || followed.Run.Job != "agent" {
		t.Fatalf("task follow response = %+v", followed)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "run", "ls", "--json")
	if code != 0 || !strings.Contains(out, followed.Run.ID) {
		t.Fatalf("run ls: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "run", "show", followed.Run.ID[:8], "--json")
	if code != 0 || !strings.Contains(out, "schema_version") || !strings.Contains(out, "add a regression test") {
		t.Fatalf("run show: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestTaskErrorsAndHelpGuideTheNextStep(t *testing.T) {
	code, out, errOut := runCLI(t, "help", "task")
	if code != 0 || !strings.Contains(out, "task create") || !strings.Contains(out, "task follow") {
		t.Fatalf("task help: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}

	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".pluto.toml"), []byte("[jobs.agent]\ncommand = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	approveTestContract(t, st, repo)
	t.Chdir(repo)
	code, _, errOut = runCLI(t, "--socket", socket, "task", "create", "--job", "missing", "--prompt", "try it")
	if code != 1 || !strings.Contains(errOut, "next:") || !strings.Contains(errOut, "pluto job ls") {
		t.Fatalf("unknown job guidance: exit=%d stderr=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "--socket", socket, "task", "show", "deadbeef")
	if code != 1 || !strings.Contains(errOut, "pluto task ls") {
		t.Fatalf("missing task guidance: exit=%d stderr=%q", code, errOut)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "run", "test")
	if code != 1 || !strings.Contains(errOut, "no such job") || strings.Contains(out, "task") {
		t.Fatalf("legacy run command changed: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestDeclaredJobsHaveNounManagerAlias(t *testing.T) {
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".pluto.toml"), []byte("[jobs.test]\ncommand = \"go test ./...\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	code, out, errOut := runCLI(t, "job", "ls")
	if code != 0 || !strings.Contains(out, "test") {
		t.Fatalf("job ls: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestTaskRetryCreatesANewRunUnderTheSameTask(t *testing.T) {
	socket, st := startDaemon(t)
	repo := committedRepo(t)
	approveTestContract(t, st, repo)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "task", "create", "--job", "agent", "--prompt", "fix the bug", "--json")
	if code != 0 {
		t.Fatalf("task create exit=%d: %s", code, errOut)
	}
	var created struct {
		Task state.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	shortID := created.Task.ID[:8]
	firstRun := created.Task.Runs[0].ID

	code, out, errOut = runCLI(t, "--socket", socket, "task", "retry", shortID, "--json")
	if code != 0 {
		t.Fatalf("task retry exit=%d stderr=%q", code, errOut)
	}
	var retried struct {
		SchemaVersion int           `json:"schema_version"`
		TaskID        string        `json:"task_id"`
		Run           state.TaskRun `json:"run"`
	}
	if err := json.Unmarshal([]byte(out), &retried); err != nil {
		t.Fatalf("task retry output is not JSON: %v (%q)", err, out)
	}
	if retried.SchemaVersion != 1 || retried.TaskID != created.Task.ID {
		t.Fatalf("task retry response = %+v", retried)
	}
	if retried.Run.ID == "" || retried.Run.ID == firstRun {
		t.Fatalf("task retry reused run %q", firstRun)
	}
	if retried.Run.Job != "agent" || retried.Run.Prompt != "fix the bug" || retried.Run.State != state.TaskRunQueued {
		t.Fatalf("task retry run = %+v", retried.Run)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "task", "show", shortID, "--json")
	if code != 0 {
		t.Fatalf("task show exit=%d stderr=%q", code, errOut)
	}
	var shown struct {
		Task state.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatal(err)
	}
	if len(shown.Task.Runs) != 2 || shown.Task.Runs[1].ID != retried.Run.ID {
		t.Fatalf("task runs after retry = %+v", shown.Task.Runs)
	}
}

func TestTaskChangesShowsSharedBoxStateWithoutTaskAttribution(t *testing.T) {
	socket, st := startDaemon(t)
	repo := committedRepo(t)
	approveTestContract(t, st, repo)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "task", "create", "--job", "agent", "--prompt", "fix the bug", "--json")
	if code != 0 {
		t.Fatalf("task create exit=%d: %s", code, errOut)
	}
	var created struct {
		Task state.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	shortID := created.Task.ID[:8]

	if err := os.WriteFile(filepath.Join(repo, ".pluto.toml"), []byte("[jobs.agent]\ncommand = \"true\"\n# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "task", "changes", shortID, "--json")
	if code != 0 {
		t.Fatalf("task changes exit=%d stderr=%q", code, errOut)
	}
	var response struct {
		SchemaVersion int                     `json:"schema_version"`
		Changes       api.TaskChangesResponse `json:"changes"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("task changes output is not JSON: %v (%q)", err, out)
	}
	if response.SchemaVersion != 1 || response.Changes.TaskID != created.Task.ID {
		t.Fatalf("task changes response = %+v", response)
	}
	if response.Changes.Attribution != api.TaskChangesSharedBox {
		t.Fatalf("attribution = %q, want %q", response.Changes.Attribution, api.TaskChangesSharedBox)
	}
	if !strings.Contains(response.Changes.AttributionNote, "cannot be attributed") {
		t.Fatalf("attribution note = %q", response.Changes.AttributionNote)
	}
	if !slices.Contains(response.Changes.ChangedFiles, "notes.txt") || !strings.Contains(response.Changes.Diff, "# edited") {
		t.Fatalf("changes = %+v", response.Changes)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "task", "changes", shortID)
	if code != 0 || !strings.Contains(out, "cannot be attributed") || !strings.Contains(out, "notes.txt") {
		t.Fatalf("task changes human output: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestRunShowExplainsTriggerContext(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	box, _, err := st.CreateBox("repo", "main", repo)
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.AcceptTriggeredTask(
		state.Task{Source: "github", Project: "repo", Ref: "refs/heads/main", BoxID: box.ID, IdempotencyKey: "github:repo:branch:main"},
		state.TaskRun{Job: "test", Prompt: "GitHub push: first", IdempotencyKey: "github:source:delivery-1"},
		state.QueueItem{Source: state.QueueEvent, EventSource: "github", EventID: "delivery-1", Repo: "repo", Job: "test", Event: state.EventContext{Kind: "push", Action: "first", Repo: "repo", Ref: "refs/heads/main", URL: "https://example.test/push"}},
		10, time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	runID := task.Runs[0].ID

	code, out, errOut := runCLI(t, "--socket", socket, "run", "show", runID[:8])
	if code != 0 {
		t.Fatalf("run show exit=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{"source: github", "project: repo", "ref: refs/heads/main", "event: push", "https://example.test/push"} {
		if !strings.Contains(out, want) {
			t.Fatalf("run show missing %q:\n%s", want, out)
		}
	}
}

// A declared job literally named ls/show/logs must keep its pre-M5 meaning for
// the documented `pluto run <job>` form (ADR 0012: do not repurpose a
// successful existing invocation).
func TestRunRunsDeclaredJobNamedLikeRunManagerSubcommand(t *testing.T) {
	fired := make(chan contract.Exec, 1)
	socket, _ := startDaemonWith(t, fakeRunner{run: func(_ *state.Box, spec contract.Exec, _ func([]byte)) (*state.Job, error) {
		fired <- spec
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	repo := gitRepo(t)
	body := "[jobs.ls]\ncommand = ['echo', 'ls-job']\n[jobs.show]\ncommand = ['echo', 'show-job']\n[jobs.logs]\ncommand = ['echo', 'logs-job']\n"
	if err := os.WriteFile(filepath.Join(repo, ".pluto.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	for _, name := range []string{"ls", "show", "logs"} {
		code, out, errOut := runCLI(t, "--socket", socket, "run", name)
		if code != 0 {
			t.Fatalf("run %s exit=%d stdout=%q stderr=%q", name, code, out, errOut)
		}
		select {
		case spec := <-fired:
			if !strings.Contains(spec.Command.String(), name+"-job") {
				t.Fatalf("run %s executed %q, want the declared job", name, spec.Command.String())
			}
		case <-time.After(time.Second):
			t.Fatalf("run %s did not reach the runner", name)
		}
	}
}

// Without a colliding declared job, the same spelling stays the run manager.
func TestRunManagerStillSelectsRunsWithoutACollidingJob(t *testing.T) {
	socket, st := startDaemon(t)
	repo := committedRepo(t)
	approveTestContract(t, st, repo)
	t.Chdir(repo)
	code, out, errOut := runCLI(t, "--socket", socket, "run", "ls", "--json")
	if code != 0 || !strings.Contains(out, `"runs"`) {
		t.Fatalf("run ls --json: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "--socket", socket, "run", "ls")
	if code != 0 || !strings.Contains(out, "no runs") {
		t.Fatalf("run ls: exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

// --follow prints a run's output and keeps polling until the run reaches a
// terminal state (story 39). This drives the queue directly because the CLI
// test daemon does not run the scheduler loop.
func TestTaskLogsFollowPollsUntilTerminalState(t *testing.T) {
	socket, st := startDaemon(t)
	repo := committedRepo(t)
	approveTestContract(t, st, repo)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "task", "create", "--job", "agent", "--prompt", "follow me", "--json")
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, errOut)
	}
	var created struct {
		Task state.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("task create output: %v (%q)", err, out)
	}
	runID := created.Task.Runs[0].ID
	job := recordJob(t, st, created.Task.BoxID, "agent", 0, 0)

	go func() {
		time.Sleep(300 * time.Millisecond)
		items, err := st.Queue()
		if err != nil {
			return
		}
		for _, item := range items {
			if item.RunID != runID {
				continue
			}
			_, _ = st.UpdateQueueItem(item.ID, state.QueueRunning, job.ID, "", time.Now())
			time.Sleep(500 * time.Millisecond)
			_, _ = st.UpdateQueueItem(item.ID, state.QueueDone, job.ID, "", time.Now())
		}
	}()

	code, out, errOut = runCLI(t, "--socket", socket, "task", "logs", created.Task.ID, "--follow")
	if code != 0 {
		t.Fatalf("task logs --follow exit=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "job log of "+job.ID) {
		t.Fatalf("follow output=%q, want the recorded job log", out)
	}
	if !strings.Contains(out, state.TaskRunCompleted) {
		t.Fatalf("follow output=%q, want the terminal state", out)
	}
}

// With --json, follow waits for the terminal state and emits one document.
func TestRunLogsFollowEmitsOneJSONDocumentAtTerminalState(t *testing.T) {
	socket, st := startDaemon(t)
	repo := committedRepo(t)
	approveTestContract(t, st, repo)
	t.Chdir(repo)

	code, out, errOut := runCLI(t, "--socket", socket, "task", "create", "--job", "agent", "--prompt", "follow json", "--json")
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, errOut)
	}
	var created struct {
		Task state.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	runID := created.Task.Runs[0].ID
	job := recordJob(t, st, created.Task.BoxID, "agent", 0, 0)
	items, err := st.Queue()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.RunID == runID {
			if _, err := st.UpdateQueueItem(item.ID, state.QueueDone, job.ID, "", time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}

	code, out, errOut = runCLI(t, "--socket", socket, "run", "logs", runID, "--follow", "--json")
	if code != 0 {
		t.Fatalf("run logs --follow --json exit=%d stderr=%q", code, errOut)
	}
	var followed struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		State         string `json:"state"`
		Log           string `json:"log"`
	}
	if err := json.Unmarshal([]byte(out), &followed); err != nil {
		t.Fatalf("follow --json is not one document: %v (%q)", err, out)
	}
	if followed.SchemaVersion != 1 || followed.RunID != runID || followed.State != state.TaskRunCompleted || !strings.Contains(followed.Log, job.ID) {
		t.Fatalf("follow --json = %+v", followed)
	}
}
