package state

import "testing"

// The durable lifecycle has distinct `accepted`, `queued`, and `starting`
// states. A run waiting behind an active sibling has no separate durable
// state, so it stays `queued`.
func TestRefreshTaskStateExposesAcceptedStartingAndQueued(t *testing.T) {
	cases := []struct {
		name string
		runs []TaskRun
		want string
	}{
		{"all runs queued is accepted", []TaskRun{{State: TaskRunQueued}}, TaskAccepted},
		{"several queued runs is accepted", []TaskRun{{State: TaskRunQueued}, {State: TaskRunQueued}}, TaskAccepted},
		{"a claimed run is starting", []TaskRun{{State: TaskRunStarting}}, TaskRunStarting},
		{"starting after a completed run is starting", []TaskRun{{State: TaskRunCompleted}, {State: TaskRunStarting}}, TaskRunStarting},
		{"running wins over starting", []TaskRun{{State: TaskRunStarting}, {State: TaskRunRunning}}, TaskRunRunning},
		{"a queued follow-up after a completed run is queued", []TaskRun{{State: TaskRunCompleted}, {State: TaskRunQueued}}, TaskRunQueued},
	}
	for _, tc := range cases {
		task := &Task{Runs: tc.runs}
		refreshTaskState(task)
		if task.State != tc.want {
			t.Errorf("%s: state=%q, want %q", tc.name, task.State, tc.want)
		}
	}
}

func TestUpdateTaskRunMapsPendingAndStartingDistinctly(t *testing.T) {
	run := TaskRun{}
	updateTaskRun(&run, QueueItem{State: QueuePending})
	if run.State != TaskRunQueued {
		t.Fatalf("pending mapped to %q, want queued", run.State)
	}
	updateTaskRun(&run, QueueItem{State: QueueStarting})
	if run.State != TaskRunStarting {
		t.Fatalf("starting mapped to %q, want starting", run.State)
	}
	run = TaskRun{}
	updateTaskRun(&run, QueueItem{State: QueueRunning})
	if run.State != TaskRunRunning || run.StartedAt == nil {
		t.Fatalf("running mapped to %q started=%v", run.State, run.StartedAt)
	}
}
