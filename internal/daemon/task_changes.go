package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/state"
)

// isolatedTaskBranchPrefix marks the branch the daemon creates for an
// isolated task worktree and box. It is the durable way to tell a task's own
// box from a shared branch box at inspection time.
const isolatedTaskBranchPrefix = "pluto/task-"

// maxTaskDiffBytes bounds a task diff returned over the API so a large
// working tree cannot produce an unbounded response. The response reports
// truncation.
const maxTaskDiffBytes = 64 * 1024

// handleGetTaskChanges reports the task's execution box and a best-effort
// snapshot of its current working-tree changes. It never claims per-task
// attribution for a shared branch box.
func (s *Server) handleGetTaskChanges(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.Task(r.PathValue("id"))
	if errors.Is(err, state.ErrTaskNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := api.TaskChangesResponse{TaskID: task.ID, ChangedFiles: []string{}}
	box, err := s.store.Box(task.BoxID)
	if err != nil {
		out.Attribution = api.TaskChangesUnavailable
		out.UnavailableReason = "task execution box is no longer available"
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.BoxID, out.BoxState, out.Branch, out.Worktree = box.ID, string(box.State), box.Branch, box.Worktree
	if isIsolatedTaskBox(box) {
		out.Attribution = api.TaskChangesTaskBox
		out.AttributionNote = "changes are from this task's isolated box"
	} else {
		out.Attribution = api.TaskChangesSharedBox
		out.AttributionNote = "these are the shared box's current changes and cannot be attributed to this task"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	status, err := gitOutput(ctx, box.Worktree, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		out.UnavailableReason = "could not inspect the box worktree: " + err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.ChangedFiles = parseGitStatusFiles(status)
	diff, err := gitOutput(ctx, box.Worktree, "diff", "--no-ext-diff", "--no-color", "HEAD", "--")
	if err != nil {
		out.UnavailableReason = "could not read the box diff: " + err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	if len(diff) > maxTaskDiffBytes {
		out.Diff, out.DiffTruncated = diff[:maxTaskDiffBytes], true
	} else {
		out.Diff = diff
	}
	writeJSON(w, http.StatusOK, out)
}

// isIsolatedTaskBox reports whether a box was created for one isolated task.
func isIsolatedTaskBox(box *state.Box) bool {
	return strings.HasPrefix(box.Branch, isolatedTaskBranchPrefix)
}

// gitOutput runs git in a worktree and returns stdout, folding stderr into the
// error so an unavailable reason is actionable.
func gitOutput(ctx context.Context, worktree string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", worktree}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}

// parseGitStatusFiles extracts the changed paths from porcelain v1 status.
func parseGitStatusFiles(status string) []string {
	files := []string{}
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if _, after, ok := strings.Cut(path, " -> "); ok {
			path = after
		}
		files = append(files, path)
	}
	return files
}
