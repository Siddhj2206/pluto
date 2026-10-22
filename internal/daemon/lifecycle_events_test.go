package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/envcache"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// byKind returns the events with a kind, oldest first.
func byKind(list []events.Event, kind string) []events.Event {
	var out []events.Event
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Kind == kind {
			out = append(out, list[i])
		}
	}
	return out
}

// eventWithKind returns the newest event of a kind.
func eventWithKind(list []events.Event, kind string) *events.Event {
	for i := range list {
		if list[i].Kind == kind {
			return &list[i]
		}
	}
	return nil
}

func assertPlace(t *testing.T, e events.Event, box boxJSON) {
	t.Helper()
	if e.Where.BoxID != box.ID || e.Where.Project == "" || e.Where.Branch == "" || e.Where.Worktree != box.Worktree {
		t.Fatalf("%s where = %+v, want box %s with project, branch, and worktree", e.Kind, e.Where, box.ID)
	}
	if e.Where.Host == "" {
		t.Fatalf("%s has no host", e.Kind)
	}
}

// TestBoxLifecycleEventsCarryPlace drives create, wake, pause, and destroy
// through the daemon API and asserts one durable event per decision, each
// located at the box.
func TestBoxLifecycleEventsCarryPlace(t *testing.T) {
	socket, _ := startWith(t, fakeRunner{record: true})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n")
	box := createBoxAt(t, c, worktree)

	if resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/up", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("up status = %d, body %s", resp.StatusCode, data)
	}
	if resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/pause", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d, body %s", resp.StatusCode, data)
	}
	if resp, data := do(t, c, "DELETE", "/v1/boxes/"+box.ID, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("destroy status = %d, body %s", resp.StatusCode, data)
	}

	list := listEvents(t, c, "?box="+box.ID)
	for _, kind := range []string{
		events.KindBoxCreated,
		events.KindBoxWakeStarted,
		events.KindBoxWakeDone,
		events.KindBoxPauseStarted,
		events.KindBoxPauseDone,
		events.KindBoxDestroyed,
	} {
		e := eventWithKind(list, kind)
		if e == nil {
			t.Fatalf("missing %s in %+v", kind, list)
		}
		assertPlace(t, *e, box)
	}

	// The wake pair reports a duration; a started event never does.
	if started := eventWithKind(list, events.KindBoxWakeStarted); started.DurationMS != 0 {
		t.Fatalf("wake.started duration = %d, want 0", started.DurationMS)
	}
	if done := eventWithKind(list, events.KindBoxWakeDone); done.DurationMS < 0 {
		t.Fatalf("wake.done duration = %d, want a measured duration", done.DurationMS)
	}
}

// TestBoxWakeFailureLeavesAFailureEvent proves a failed wake is attributable
// to the box, with the reason and a duration.
func TestBoxWakeFailureLeavesAFailureEvent(t *testing.T) {
	socket, _ := startWith(t, fakeRunner{upErr: errors.New("boot failed")})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n")
	box := createBoxAt(t, c, worktree)

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/up", nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("up status = %d, body %s", resp.StatusCode, data)
	}
	list := listEvents(t, c, "?box="+box.ID)
	failed := eventWithKind(list, events.KindBoxFailed)
	if failed == nil {
		t.Fatalf("missing box.failed in %+v", list)
	}
	if failed.Level != events.LevelError || failed.Reason != "boot failed" {
		t.Fatalf("box.failed = %+v, want an error with the boot reason", failed)
	}
	assertPlace(t, *failed, box)
}

// TestPhaseFactsRecordExitCodeDurationAndError proves the daemon turns a
// runner's phase, service, and session observations into located events.
func TestPhaseFactsRecordExitCodeDurationAndError(t *testing.T) {
	fr := fakeRunner{record: true, facts: []events.Fact{
		{Kind: events.KindPhaseProvisionStarted, Summary: "provision running"},
		{Kind: events.KindPhaseProvisionDone, Summary: "provision done", ExitCode: 0, ExitCodeKnown: true, Duration: 1500 * 1e6},
		{Kind: events.KindPhaseWakeFailed, Level: events.LevelError, Summary: "wake failed", Reason: "wake command exited 2", ExitCode: 2, ExitCodeKnown: true, Duration: 250 * 1e6},
		{Kind: events.KindPhaseServiceChanged, Summary: "service web active", Attrs: map[string]string{"service": "web", "state": "active", "port": "3000"}},
		{Kind: events.KindSessionStarted, Summary: "session agent running", Attrs: map[string]string{"session": "agent"}},
		{Kind: events.KindSessionAttached, Summary: "session agent attached", Attrs: map[string]string{"session": "agent"}},
	}}
	socket, _ := startWith(t, fr)
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n")
	box := createBoxAt(t, c, worktree)

	if resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/up", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("up status = %d, body %s", resp.StatusCode, data)
	}

	list := listEvents(t, c, "?box="+box.ID)
	provisionDone := eventWithKind(list, events.KindPhaseProvisionDone)
	if provisionDone == nil {
		t.Fatalf("missing provision.done in %+v", list)
	}
	assertPlace(t, *provisionDone, box)
	if provisionDone.Attrs["exit_code"] != "0" {
		t.Fatalf("provision.done exit_code = %q, want 0", provisionDone.Attrs["exit_code"])
	}
	if provisionDone.DurationMS != 1500 {
		t.Fatalf("provision.done duration = %d, want 1500", provisionDone.DurationMS)
	}

	wakeFailed := eventWithKind(list, events.KindPhaseWakeFailed)
	if wakeFailed == nil {
		t.Fatalf("missing wake.failed in %+v", list)
	}
	if wakeFailed.Attrs["exit_code"] != "2" || wakeFailed.Reason != "wake command exited 2" || wakeFailed.DurationMS != 250 {
		t.Fatalf("wake.failed = %+v, want exit 2, reason, and duration", wakeFailed)
	}
	if wakeFailed.Level != events.LevelError {
		t.Fatalf("wake.failed level = %q, want error", wakeFailed.Level)
	}

	service := eventWithKind(list, events.KindPhaseServiceChanged)
	if service == nil || service.Attrs["service"] != "web" || service.Attrs["state"] != "active" {
		t.Fatalf("service change = %+v, want the service and state", service)
	}
	if session := eventWithKind(list, events.KindSessionAttached); session == nil || session.Attrs["session"] != "agent" {
		t.Fatalf("session attach = %+v, want the session name", session)
	}
}

// TestQueuedJobLeavesAFullChain proves a queued job leaves acceptance,
// dispatch, start, finish, and terminal outcome in one trace, with the queue
// and job identity on each event.
func TestQueuedJobLeavesAFullChain(t *testing.T) {
	socket, st, srv := startServerWithLedger(t, fakeRunner{record: true}, events.Options{})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n")
	box := createBoxAt(t, c, worktree)

	const trace = "trc_1111111111111111"
	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/queue", api.QueueRequest{Job: "test", TraceID: trace})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("queue status = %d, body %s", resp.StatusCode, data)
	}
	var queued api.QueueResponse
	if err := json.Unmarshal(data, &queued); err != nil {
		t.Fatalf("decode queue: %v", err)
	}

	runScheduler(t, srv, &schedulerClock{now: time.Now()})
	waitFor(t, "the queue to drain", func() bool {
		items, err := st.Queue()
		if err != nil {
			return false
		}
		for _, item := range items {
			if item.State == state.QueuePending || item.State == state.QueueStarting || item.State == state.QueueRunning {
				return false
			}
		}
		return len(items) > 0
	})

	list := listEvents(t, c, "?trace="+trace)
	want := []string{
		events.KindQueueAccepted,
		events.KindQueueDispatched,
		events.KindJobStarted,
		events.KindJobDone,
		events.KindQueueDone,
	}
	if len(list) != len(want) {
		t.Fatalf("trace events = %+v, want %v", list, want)
	}
	// Newest first; compare the causal order oldest first.
	for i, kind := range want {
		got := list[len(list)-1-i]
		if got.Kind != kind {
			t.Fatalf("event %d kind = %s, want %s (all %+v)", i, got.Kind, kind, list)
		}
		if got.Where.BoxID != box.ID || got.Where.QueueID != queued.Item.ID {
			t.Fatalf("%s where = %+v, want box %s and queue %s", kind, got.Where, box.ID, queued.Item.ID)
		}
	}
	if list[len(list)-1].Attrs["source"] != state.QueueExplicit {
		t.Fatalf("queue.accepted source = %q, want explicit", list[len(list)-1].Attrs["source"])
	}
	// Each event points at the one before it.
	for i := 1; i < len(want); i++ {
		child := list[len(list)-1-i]
		parent := list[len(list)-i]
		if child.ParentID != parent.ID {
			t.Fatalf("%s parent = %q, want %s (%q)", child.Kind, child.ParentID, parent.Kind, parent.ID)
		}
	}
	done := list[0]
	if done.Where.JobID == "" || done.DurationMS < 0 {
		t.Fatalf("queue.done = %+v, want the linked job and duration", done)
	}
}

// TestTaskAndRunLeaveOneTracedChain proves a durable task's run is
// attributable through create, queue, start, finish, and the task-level
// outcome, with task, run, queue, and trace identity on each event.
func TestTaskAndRunLeaveOneTracedChain(t *testing.T) {
	socket, st, srv := startServerWithLedger(t, fakeRunner{record: true}, events.Options{})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n")
	box := createBoxAt(t, c, worktree)

	const trace = "trc_2222222222222222"
	resp, data := do(t, c, "POST", "/v1/tasks", api.TaskRequest{
		BoxID: box.ID, Job: "test", Prompt: "run the tests", IdempotencyKey: "task-key-1", TraceID: trace,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("task status = %d, body %s", resp.StatusCode, data)
	}
	var created api.TaskResponse
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if len(created.Task.Runs) != 1 {
		t.Fatalf("task runs = %+v, want one", created.Task.Runs)
	}
	run := created.Task.Runs[0]

	runScheduler(t, srv, &schedulerClock{now: time.Now()})
	waitFor(t, "the task queue to drain", func() bool {
		items, err := st.Queue()
		if err != nil {
			return false
		}
		for _, item := range items {
			if item.State == state.QueuePending || item.State == state.QueueStarting || item.State == state.QueueRunning {
				return false
			}
		}
		return len(items) > 0
	})

	list := listEvents(t, c, "?trace="+trace)
	want := []string{
		events.KindQueueAccepted,
		events.KindTaskCreated,
		events.KindQueueDispatched,
		events.KindTaskRunStarted,
		events.KindJobStarted,
		events.KindJobDone,
		events.KindQueueDone,
		events.KindTaskRunDone,
	}
	if len(list) != len(want) {
		t.Fatalf("trace events = %+v, want %v", list, want)
	}
	for i, kind := range want {
		got := list[len(list)-1-i]
		if got.Kind != kind {
			t.Fatalf("event %d kind = %s, want %s (all %+v)", i, got.Kind, kind, list)
		}
		if got.TraceID != trace {
			t.Fatalf("%s trace = %q, want %q", kind, got.TraceID, trace)
		}
		if got.Where.TaskID != created.Task.ID || got.Where.RunID != run.ID || got.Where.QueueID != run.QueueID {
			t.Fatalf("%s where = %+v, want task %s run %s queue %s", kind, got.Where, created.Task.ID, run.ID, run.QueueID)
		}
	}
	for i := 1; i < len(want); i++ {
		child := list[len(list)-1-i]
		parent := list[len(list)-i]
		if child.ParentID != parent.ID {
			t.Fatalf("%s parent = %q, want %s (%q)", child.Kind, child.ParentID, parent.Kind, parent.ID)
		}
	}

	// The run's stored record agrees with the chain.
	stored := fetchTask(t, c, created.Task.ID)
	if stored.TraceID != trace || len(stored.Runs) != 1 || stored.Runs[0].TraceID != trace || stored.Runs[0].State != state.TaskRunCompleted {
		t.Fatalf("stored task = %+v, want a completed traced run", stored)
	}
}

// TestTaskRunBlockLeavesAReason proves a task run that cannot proceed records
// its block with the task, run, queue, and trace identity.
func TestTaskRunBlockLeavesAReason(t *testing.T) {
	socket, _, srv := startServerWithLedger(t, fakeRunner{run: func(*state.Box, contract.Exec, func([]byte)) (*state.Job, error) {
		return nil, envcache.ErrBuilding
	}}, events.Options{})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n")
	box := createBoxAt(t, c, worktree)

	const trace = "trc_4444444444444444"
	resp, data := do(t, c, "POST", "/v1/tasks", api.TaskRequest{
		BoxID: box.ID, Job: "test", Prompt: "run the tests", IdempotencyKey: "task-key-block", TraceID: trace,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("task status = %d, body %s", resp.StatusCode, data)
	}
	var created api.TaskResponse
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	run := created.Task.Runs[0]

	stop := startScheduler(t, srv)
	waitFor(t, "the task run block", func() bool {
		return eventWithKind(listEvents(t, c, "?trace="+trace), events.KindTaskRunBlocked) != nil
	})
	stop()

	blocked := eventWithKind(listEvents(t, c, "?trace="+trace), events.KindTaskRunBlocked)
	if blocked == nil {
		t.Fatal("missing task.run.blocked")
	}
	if blocked.Where.TaskID != created.Task.ID || blocked.Where.RunID != run.ID || blocked.Where.QueueID != run.QueueID || blocked.Where.BoxID != box.ID {
		t.Fatalf("task.run.blocked where = %+v, want task, run, queue, and box", blocked.Where)
	}
	if !strings.Contains(blocked.Reason, "environment build") || blocked.TraceID != trace {
		t.Fatalf("task.run.blocked = %+v, want the wait reason and the trace", blocked)
	}
}

// TestQueueRejectionBlockAndRecoverySurviveRestart proves a rejected or
// blocked queue decision leaves a reason in both the queue record and the
// ledger, and that a restart neither forgets the reason nor loses the item.
func TestQueueRejectionBlockAndRecoverySurviveRestart(t *testing.T) {
	dir := t.TempDir()
	const trace = "trc_3333333333333333"

	st, ledger, srv := openDaemon(t, dir, 1, fakeRunner{record: true})
	c := client(socketFor(dir))
	worktree := writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n")
	box := createBoxAt(t, c, worktree)
	if resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/queue", api.QueueRequest{Job: "test", TraceID: trace}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first queue status = %d, body %s", resp.StatusCode, data)
	}
	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/queue", api.QueueRequest{Job: "test", TraceID: trace})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second queue status = %d, want 503, body %s", resp.StatusCode, data)
	}
	rejected := eventWithKind(listEvents(t, c, "?kind=queue.rejected"), events.KindQueueRejected)
	if rejected == nil || !strings.Contains(rejected.Reason, "full") || rejected.Where.BoxID != box.ID {
		t.Fatalf("queue.rejected = %+v, want the box and the full-queue reason", rejected)
	}
	rejectedID := rejected.ID
	closeDaemon(t, srv, ledger, st)

	// Restart: the rejection reason is still readable from the ledger.
	st, ledger, srv = openDaemon(t, dir, 1, fakeRunner{run: func(*state.Box, contract.Exec, func([]byte)) (*state.Job, error) {
		return nil, envcache.ErrBuilding
	}})
	c = client(socketFor(dir))
	rejected = eventWithKind(listEvents(t, c, "?kind=queue.rejected"), events.KindQueueRejected)
	if rejected == nil || rejected.ID != rejectedID || !strings.Contains(rejected.Reason, "full") {
		t.Fatalf("after restart queue.rejected = %+v, want the same durable decision %s", rejected, rejectedID)
	}
	items := fetchQueue(t, c)
	if len(items) != 1 || items[0].State != state.QueuePending {
		t.Fatalf("after restart queue = %+v, want the accepted item still pending", items)
	}
	itemID := items[0].ID

	// The environment build makes the accepted item wait with a visible reason.
	stop := startScheduler(t, srv)
	waitFor(t, "the queue block", func() bool {
		return eventWithKind(listEvents(t, c, "?kind=queue.blocked"), events.KindQueueBlocked) != nil
	})
	stop()
	blocked := eventWithKind(listEvents(t, c, "?kind=queue.blocked"), events.KindQueueBlocked)
	if blocked == nil || blocked.Where.QueueID != itemID || !strings.Contains(blocked.Reason, "environment build") {
		t.Fatalf("queue.blocked = %+v, want queue %s waiting for an environment build", blocked, itemID)
	}
	closeDaemon(t, srv, ledger, st)

	// Restart again: the blocked reason survives in the queue record and ledger.
	st, ledger, srv = openDaemon(t, dir, 1, fakeRunner{record: true})
	c = client(socketFor(dir))
	items = fetchQueue(t, c)
	if len(items) != 1 || items[0].State != state.QueuePending || !strings.Contains(items[0].Reason, "environment build") {
		t.Fatalf("after second restart queue = %+v, want the blocked reason", items)
	}
	blocked = eventWithKind(listEvents(t, c, "?kind=queue.blocked"), events.KindQueueBlocked)
	if blocked == nil || !strings.Contains(blocked.Reason, "environment build") {
		t.Fatalf("after second restart queue.blocked = %+v, want the durable reason", blocked)
	}

	// A request caught running by a restart is failed with an unknown-outcome
	// reason, and the decision is durable.
	if _, err := st.UpdateQueueItem(itemID, state.QueueRunning, "", "", time.Now()); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	stop = startScheduler(t, srv)
	waitFor(t, "queue recovery", func() bool {
		return eventWithKind(listEvents(t, c, "?kind=queue.failed"), events.KindQueueFailed) != nil
	})
	stop()
	failed := eventWithKind(listEvents(t, c, "?kind=queue.failed"), events.KindQueueFailed)
	if failed == nil || failed.Where.QueueID != itemID || !strings.Contains(failed.Reason, "unknown") {
		t.Fatalf("queue.failed = %+v, want the running item's unknown outcome", failed)
	}
	items = fetchQueue(t, c)
	if len(items) != 1 || items[0].State != state.QueueFailed || !strings.Contains(items[0].Reason, "unknown") {
		t.Fatalf("after recovery queue = %+v, want the failed item and reason", items)
	}

	// A request caught starting is requeued with its own recover decision.
	resp, data = do(t, c, "POST", "/v1/boxes/"+box.ID+"/queue", api.QueueRequest{Job: "test", TraceID: trace})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("third queue status = %d, body %s", resp.StatusCode, data)
	}
	items = fetchQueue(t, c)
	var requeueID string
	for _, item := range items {
		if item.State == state.QueuePending {
			requeueID = item.ID
		}
	}
	if requeueID == "" {
		t.Fatalf("queue = %+v, want a pending item to recover", items)
	}
	if _, err := st.UpdateQueueItem(requeueID, state.QueueStarting, "", "", time.Now()); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	stop = startScheduler(t, srv)
	waitFor(t, "queue requeue recovery", func() bool {
		for _, e := range listEvents(t, c, "?kind=queue.recovered") {
			if e.Where.QueueID == requeueID {
				return true
			}
		}
		return false
	})
	stop()
	waitFor(t, "the requeued item to finish", func() bool {
		for _, item := range fetchQueue(t, c) {
			if item.ID == requeueID {
				return item.State != state.QueuePending && item.State != state.QueueStarting && item.State != state.QueueRunning
			}
		}
		return false
	})
	var recovered *events.Event
	for _, e := range listEvents(t, c, "?kind=queue.recovered") {
		if e.Where.QueueID == requeueID {
			found := e
			recovered = &found
			break
		}
	}
	if recovered == nil || recovered.Where.BoxID != box.ID || recovered.Attrs["source"] != state.QueueExplicit {
		t.Fatalf("queue.recovered = %+v, want box %s and the source", recovered, box.ID)
	}
	closeDaemon(t, srv, ledger, st)
}

func socketFor(dir string) string { return filepath.Join(dir, "pluto.sock") }

func fetchQueue(t *testing.T, c *http.Client) []state.QueueItem {
	t.Helper()
	resp, data := do(t, c, "GET", "/v1/queue", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/queue status = %d, body %s", resp.StatusCode, data)
	}
	var out api.QueueListResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode queue: %v (%s)", err, data)
	}
	return out.Items
}

func fetchTask(t *testing.T, c *http.Client, id string) state.Task {
	t.Helper()
	resp, data := do(t, c, "GET", "/v1/tasks/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/tasks/%s status = %d, body %s", id, resp.StatusCode, data)
	}
	var out api.TaskResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode task: %v (%s)", err, data)
	}
	return out.Task
}

// openDaemon opens a daemon over a fixed directory so a test can restart it
// and observe what survived.
func openDaemon(t *testing.T, dir string, capacity int, fr fakeRunner) (*state.Store, *events.Store, *daemon.Server) {
	t.Helper()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	ledger, err := events.OpenWithOptions(filepath.Join(dir, "events"), events.Options{})
	if err != nil {
		t.Fatalf("events.Open: %v", err)
	}
	fr.st = st
	fr.obs = &fakeObservers{}
	srv := daemon.New(st, fr, "test")
	srv.Events = ledger
	srv.QueueCapacity = capacity
	if err := srv.Listen(socketFor(dir)); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve()
	return st, ledger, srv
}

func closeDaemon(t *testing.T, srv *daemon.Server, ledger *events.Store, st *state.Store) {
	t.Helper()
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	ledger.Close()
	st.Close()
}

// startScheduler runs the daemon's scheduler and dispatcher until the returned
// function is called.
func startScheduler(t *testing.T, srv *daemon.Server) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.SchedulerLoop(ctx, 20*time.Millisecond)
	}()
	return func() {
		cancel()
		<-done
	}
}
