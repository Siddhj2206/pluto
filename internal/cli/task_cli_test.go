package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

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
	if response.SchemaVersion != 1 || response.Task.ID == "" || response.Task.State != state.TaskRunQueued || len(response.Task.Runs) != 1 {
		t.Fatalf("task create response = %+v, want schema 1 with one queued run", response)
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
