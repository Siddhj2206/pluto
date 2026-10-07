package daemon_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
)

// gitWorktreeWithContract creates a committed git worktree that declares one
// job, so change inspection has a HEAD to diff against.
func gitWorktreeWithContract(t *testing.T, job string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "branch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "init", "-b", "main")
	gitTest(t, dir, "config", "user.name", "Test")
	gitTest(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, ".pluto.toml"), []byte("[jobs."+job+"]\ncommand = ['true']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", ".pluto.toml")
	gitTest(t, dir, "commit", "-m", "contract")
	return dir
}

func TestTaskChangesReportSharedBoxStateWithoutTaskAttribution(t *testing.T) {
	worktree := gitWorktreeWithContract(t, "agent")
	socket, st, _ := startServer(t, fakeRunner{})
	c := client(socket)
	box, _, err := st.CreateBox("project", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	approveContractRevision(t, c, box)
	resp, data := do(t, c, http.MethodPost, "/v1/tasks", api.TaskRequest{BoxID: box.ID, Job: "agent", Prompt: "do work", IdempotencyKey: "shared-task"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create task status=%d body=%s", resp.StatusCode, data)
	}
	var created api.TaskResponse
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatal(err)
	}

	// The shared branch box accumulates changes from anyone working in it.
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[jobs.agent]\ncommand = ['true']\n# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "notes.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, data = do(t, c, http.MethodGet, "/v1/tasks/"+created.Task.ID+"/changes", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("changes status=%d body=%s", resp.StatusCode, data)
	}
	var changes api.TaskChangesResponse
	if err := json.Unmarshal(data, &changes); err != nil {
		t.Fatal(err)
	}
	if changes.TaskID != created.Task.ID || changes.BoxID != box.ID {
		t.Fatalf("changes identity = %+v", changes)
	}
	if changes.Attribution != api.TaskChangesSharedBox {
		t.Fatalf("attribution = %q, want %q", changes.Attribution, api.TaskChangesSharedBox)
	}
	if !strings.Contains(changes.AttributionNote, "cannot be attributed") {
		t.Fatalf("shared box attribution note = %q", changes.AttributionNote)
	}
	if !slices.Contains(changes.ChangedFiles, "notes.txt") || !slices.Contains(changes.ChangedFiles, ".pluto.toml") {
		t.Fatalf("changed files = %v", changes.ChangedFiles)
	}
	if !strings.Contains(changes.Diff, "# edited") {
		t.Fatalf("diff missing the tracked edit: %q", changes.Diff)
	}
	if changes.UnavailableReason != "" {
		t.Fatalf("unexpected unavailable reason: %q", changes.UnavailableReason)
	}
}

func TestTaskChangesReportIsolatedTaskBoxChanges(t *testing.T) {
	worktree := gitWorktreeWithContract(t, "agent")
	socket, st, _ := startServer(t, fakeRunner{})
	c := client(socket)
	branchBox, _, err := st.CreateBox("project", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	approveContractRevision(t, c, branchBox)
	resp, data := do(t, c, http.MethodPost, "/v1/tasks", api.TaskRequest{
		BoxID: branchBox.ID, Job: "agent", Prompt: "do work", IdempotencyKey: "isolated-task", Isolate: true,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create task status=%d body=%s", resp.StatusCode, data)
	}
	var created api.TaskResponse
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatal(err)
	}
	isolated, err := st.Box(created.Task.BoxID)
	if err != nil {
		t.Fatal(err)
	}
	if isolated.Worktree == branchBox.Worktree {
		t.Fatalf("isolated task reused branch worktree %q", isolated.Worktree)
	}
	// The task's own worktree changes are the task's; the shared branch box
	// accumulates separate changes that must not leak into the task.
	if err := os.WriteFile(filepath.Join(isolated.Worktree, ".pluto.toml"), []byte("[jobs.agent]\ncommand = ['true']\n# task edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(isolated.Worktree, "task-note.txt"), []byte("task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "shared-note.txt"), []byte("shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, data = do(t, c, http.MethodGet, "/v1/tasks/"+created.Task.ID+"/changes", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("changes status=%d body=%s", resp.StatusCode, data)
	}
	var changes api.TaskChangesResponse
	if err := json.Unmarshal(data, &changes); err != nil {
		t.Fatal(err)
	}
	if changes.Attribution != api.TaskChangesTaskBox {
		t.Fatalf("attribution = %q, want %q", changes.Attribution, api.TaskChangesTaskBox)
	}
	if !slices.Contains(changes.ChangedFiles, "task-note.txt") || !strings.Contains(changes.Diff, "# task edit") {
		t.Fatalf("isolated changes = %+v", changes)
	}
	if slices.Contains(changes.ChangedFiles, "shared-note.txt") {
		t.Fatalf("isolated task attributed the shared box's change: %v", changes.ChangedFiles)
	}
}
