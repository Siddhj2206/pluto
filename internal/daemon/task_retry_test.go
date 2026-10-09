package daemon_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/state"
)

func TestTaskRetryAppendsRunWithTheSameWork(t *testing.T) {
	worktree := gitWorktreeWithContract(t, "agent")
	socket, st, _ := startServer(t, fakeRunner{})
	c := client(socket)
	box, _, err := st.CreateBox("project", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	approveContractRevision(t, c, box)
	resp, data := do(t, c, http.MethodPost, "/v1/tasks", api.TaskRequest{BoxID: box.ID, Job: "agent", Prompt: "do work", IdempotencyKey: "retry-task"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create task status=%d body=%s", resp.StatusCode, data)
	}
	var created api.TaskResponse
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatal(err)
	}
	firstRun := created.Task.Runs[0].ID

	resp, data = do(t, c, http.MethodPost, "/v1/tasks/"+created.Task.ID+"/runs", api.TaskRunRequest{RetryRunID: firstRun, IdempotencyKey: "retry-1"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", resp.StatusCode, data)
	}
	var retried api.TaskRunResponse
	if err := json.Unmarshal(data, &retried); err != nil {
		t.Fatal(err)
	}
	if retried.Run.ID == "" || retried.Run.ID == firstRun {
		t.Fatalf("retry did not create a new run: %+v", retried.Run)
	}
	if retried.Run.Job != "agent" || retried.Run.Prompt != "do work" || retried.Run.State != state.TaskRunQueued {
		t.Fatalf("retry run = %+v, want the retried job and prompt", retried.Run)
	}

	resp, data = do(t, c, http.MethodGet, "/v1/tasks/"+created.Task.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read task status=%d body=%s", resp.StatusCode, data)
	}
	var got api.TaskResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Task.Runs) != 2 || got.Task.Runs[1].ID != retried.Run.ID {
		t.Fatalf("task runs after retry = %+v", got.Task.Runs)
	}

	resp, data = do(t, c, http.MethodPost, "/v1/tasks/"+created.Task.ID+"/runs", api.TaskRunRequest{RetryRunID: "deadbeef", IdempotencyKey: "retry-2"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("retry unknown run status=%d body=%s, want bad request", resp.StatusCode, data)
	}
}
