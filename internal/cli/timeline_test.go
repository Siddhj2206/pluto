package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// traceOfRun runs one job and returns the trace its events carry.
func traceOfRun(t *testing.T, socket, repo string) string {
	t.Helper()
	code, out, errOut := runCLI(t, "--socket", socket, "events", "--kind", "job.", "--json")
	if code != 0 {
		t.Fatalf("events exit = %d, stderr: %s", code, errOut)
	}
	var list api.EventListResponse
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decode events: %v (%s)", err, out)
	}
	if len(list.Events) == 0 || list.Events[0].TraceID == "" {
		t.Fatalf("no traced job events: %s", out)
	}
	return list.Events[0].TraceID
}

// TestTimelineShowsOneCausalChain drives the real CLI against a daemon with a
// fake runner: a run's trace reads as one ordered chain, by trace and by box,
// in human and JSON form.
func TestTimelineShowsOneCausalChain(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)

	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "test"); code != 0 {
		t.Fatalf("run exit = %d, stderr: %s", code, errOut)
	}
	trace := traceOfRun(t, socket, repo)

	code, out, errOut := runCLI(t, "--socket", socket, "timeline", trace)
	if code != 0 {
		t.Fatalf("timeline exit = %d, stderr: %s", code, errOut)
	}
	queued := strings.Index(out, events.KindJobQueued)
	started := strings.Index(out, events.KindJobStarted)
	done := strings.Index(out, events.KindJobDone)
	if queued < 0 || started < 0 || done < 0 {
		t.Fatalf("timeline is missing a hop:\n%s", out)
	}
	if !(queued < started && started < done) {
		t.Fatalf("timeline is not in causal order:\n%s", out)
	}
	if !strings.Contains(out, "main") {
		t.Fatalf("timeline has no where:\n%s", out)
	}

	// A worktree resolves to its box and shows the whole box chain.
	code, out, errOut = runCLI(t, "--socket", socket, "timeline", repo)
	if code != 0 {
		t.Fatalf("timeline by box exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, events.KindBoxCreated) || !strings.Contains(out, events.KindJobDone) {
		t.Fatalf("box timeline = %q, want the create and the job", out)
	}

	// JSON is versioned, names the target and trace, and is in sequence order.
	code, out, errOut = runCLI(t, "--socket", socket, "timeline", trace, "--json")
	if code != 0 {
		t.Fatalf("timeline --json exit = %d, stderr: %s", code, errOut)
	}
	var got struct {
		SchemaVersion int            `json:"schema_version"`
		Target        string         `json:"target"`
		TraceID       string         `json:"trace_id"`
		Events        []events.Event `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode timeline: %v (%s)", err, out)
	}
	if got.SchemaVersion != 1 || got.Target != trace || got.TraceID != trace {
		t.Fatalf("timeline JSON = %+v, want schema 1 and the target trace", got)
	}
	if len(got.Events) < 3 {
		t.Fatalf("timeline events = %+v, want the run chain", got.Events)
	}
	for i := 1; i < len(got.Events); i++ {
		if got.Events[i].Seq <= got.Events[i-1].Seq {
			t.Fatalf("timeline JSON is not in sequence order: %d then %d", got.Events[i-1].Seq, got.Events[i].Seq)
		}
	}
}

// TestTimelineResolvesATaskAndRun proves a durable task's id (and its run's
// id) selects the same chain as its trace.
func TestTimelineResolvesATaskAndRun(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	code, out, errOut := runCLI(t, "--socket", socket, "task", "create", "--job", "test", "--prompt", "do it", repo, "--json")
	if code != 0 {
		t.Fatalf("task create exit = %d, stderr: %s", code, errOut)
	}
	var created struct {
		Task state.Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode task: %v (%s)", err, out)
	}
	if created.Task.TraceID == "" || len(created.Task.Runs) != 1 {
		t.Fatalf("task = %+v, want a traced run", created.Task)
	}

	for _, target := range []string{created.Task.ID, created.Task.Runs[0].ID} {
		wantRun := ""
		if target == created.Task.Runs[0].ID {
			wantRun = created.Task.Runs[0].ID
		}
		code, out, errOut = runCLI(t, "--socket", socket, "timeline", target, "--json")
		if code != 0 {
			t.Fatalf("timeline %s exit = %d, stderr: %s", target, code, errOut)
		}
		var got struct {
			TraceID string         `json:"trace_id"`
			TaskID  string         `json:"task_id"`
			RunID   string         `json:"run_id"`
			Events  []events.Event `json:"events"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("decode timeline: %v (%s)", err, out)
		}
		if got.TraceID != created.Task.TraceID || got.TaskID != created.Task.ID || got.RunID != wantRun {
			t.Fatalf("timeline %s identity = %+v, want the task and run %q", target, got, wantRun)
		}
		found := false
		for _, e := range got.Events {
			if e.Kind == events.KindTaskCreated && e.Where.TaskID == created.Task.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("timeline %s has no task.created: %+v", target, got.Events)
		}
	}
}

// TestTimelineRejectsAnUnknownTarget names the next step instead of printing
// an empty chain.
func TestTimelineRejectsAnUnknownTarget(t *testing.T) {
	socket, _ := startDaemon(t)
	code, out, errOut := runCLI(t, "--socket", socket, "timeline", "no-such-target")
	if code == 0 {
		t.Fatalf("timeline exit = 0, want a failure (stdout %q)", out)
	}
	if !strings.Contains(errOut, "box") || !strings.Contains(errOut, "task") || !strings.Contains(errOut, "trace") {
		t.Fatalf("stderr = %q, want the accepted target kinds", errOut)
	}
}

// TestStatusShowsTheLatestEventWithoutChangingState proves status adds the
// latest box event and its trace without altering the lifecycle.
func TestStatusShowsTheLatestEventWithoutChangingState(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "test"); code != 0 {
		t.Fatalf("run exit = %d, stderr: %s", code, errOut)
	}
	trace := traceOfRun(t, socket, repo)

	code, out, errOut := runCLI(t, "--socket", socket, "status", repo)
	if code != 0 {
		t.Fatalf("status exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"id:", "project:", "branch:", "state:", "last:", "trace:", trace, "pluto timeline"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output = %q, want %q", out, want)
		}
	}

	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	if boxes[0].State != state.StateRunning {
		t.Fatalf("status changed the box state to %q, want running", boxes[0].State)
	}
}
