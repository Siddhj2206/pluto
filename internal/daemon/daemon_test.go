package daemon_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
)

// fakeRunner stands in for the box lifecycle in daemon tests: it moves
// records through the same states the real runner would. run scripts a job;
// without one, runs succeed silently. window is the auto-pause window the
// runner reports; clients and refreshErr shape what Refresh sees.
type fakeRunner struct {
	st         *state.Store
	run        func(box *state.Box, argv []string, emit func([]byte)) (*state.Job, error)
	window     time.Duration
	clients    int
	refreshErr error
}

func (f fakeRunner) Up(ctx context.Context, box *state.Box) (*state.Box, error) {
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
	if f.clients > 0 {
		return f.st.SetPhases(box.ID, state.Phases{Synced: true, Clients: f.clients})
	}
	return box, nil
}

func (f fakeRunner) AutoPauseWindow(box *state.Box) time.Duration { return f.window }

func (f fakeRunner) Logs(box *state.Box, phase, service string, lines int) (string, error) {
	return "log of " + phase + service, nil
}

func (f fakeRunner) RunJob(ctx context.Context, box *state.Box, argv []string, emit func([]byte)) (*state.Box, *state.Job, error) {
	if f.run != nil {
		job, err := f.run(box, argv, emit)
		if err != nil {
			return nil, nil, err
		}
		return box, job, nil
	}
	job := state.StartJob(state.NewID(), argv)
	job.Finish(state.JobDone, 0, "")
	return box, &job, nil
}

func (f fakeRunner) JobLog(box *state.Box, jobID string, lines int) (string, error) {
	return "job log of " + jobID, nil
}

func (f fakeRunner) Destroy(id string) error { return f.st.DestroyBox(id) }

func (f fakeRunner) Import(srcDir string) (string, error) { return "ver123", nil }

func (f fakeRunner) Images() ([]api.ImageInfo, error) {
	return []api.ImageInfo{{Version: "ver123"}}, nil
}

type boxJSON struct {
	Schema    int        `json:"schema"`
	ID        string     `json:"id"`
	Project   string     `json:"project"`
	Branch    string     `json:"branch"`
	Worktree  string     `json:"worktree"`
	State     string     `json:"state"`
	AutoPause string     `json:"auto_pause"`
	IdleSince *time.Time `json:"idle_since"`
}

type recordErrorJSON struct {
	Path string `json:"path"`
	Err  string `json:"error"`
}

type listJSON struct {
	Boxes  []boxJSON         `json:"boxes"`
	Errors []recordErrorJSON `json:"errors"`
}

func start(t *testing.T) (socket string, st *state.Store) {
	return startWith(t, fakeRunner{})
}

func startWith(t *testing.T, fr fakeRunner) (socket string, st *state.Store) {
	socket, st, _ = startServer(t, fr)
	return socket, st
}

func startServer(t *testing.T, fr fakeRunner) (socket string, st *state.Store, srv *daemon.Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	fr.st = st

	socket = filepath.Join(dir, "pluto.sock")
	srv = daemon.New(st, fr, "test")
	if err := srv.Listen(socket); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	return socket, st, srv
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func client(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
}

func do(t *testing.T, c *http.Client, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "http://pluto"+path, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, data
}

func TestHealth(t *testing.T) {
	socket, _ := start(t)
	resp, data := do(t, client(socket), "GET", "/v1/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, data)
	}
	var health struct {
		Status string `json:"status"`
		Boxes  int    `json:"boxes"`
	}
	if err := json.Unmarshal(data, &health); err != nil {
		t.Fatalf("decode health: %v (%s)", err, data)
	}
	if health.Status != "ok" || health.Boxes != 0 {
		t.Fatalf("health = %+v, want ok/0", health)
	}
}

func TestBoxCRUDOverSocket(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)

	resp, data := do(t, c, "POST", "/v1/boxes", map[string]string{
		"worktree": "/src/alpha", "project": "alpha", "branch": "main",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", resp.StatusCode, data)
	}
	var box boxJSON
	if err := json.Unmarshal(data, &box); err != nil {
		t.Fatalf("decode box: %v (%s)", err, data)
	}
	if box.ID == "" || box.State != "created" || box.Worktree != "/src/alpha" {
		t.Fatalf("box = %+v", box)
	}

	resp, data = do(t, c, "POST", "/v1/boxes", map[string]string{
		"worktree": "/src/alpha", "project": "alpha", "branch": "main",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second create status = %d, body %s", resp.StatusCode, data)
	}
	var again boxJSON
	if err := json.Unmarshal(data, &again); err != nil {
		t.Fatalf("decode box: %v", err)
	}
	if again.ID != box.ID {
		t.Fatalf("second create id = %s, want %s", again.ID, box.ID)
	}

	resp, data = do(t, c, "GET", "/v1/boxes", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var list listJSON
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, data)
	}
	if len(list.Boxes) != 1 || len(list.Errors) != 0 {
		t.Fatalf("list = %+v", list)
	}

	resp, _ = do(t, c, "GET", "/v1/boxes/"+box.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	resp, _ = do(t, c, "GET", "/v1/boxes/11111111-2222-4333-8444-555555555555", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown get status = %d, want 404", resp.StatusCode)
	}

	resp, _ = do(t, c, "DELETE", "/v1/boxes/"+box.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}
	resp, _ = do(t, c, "GET", "/v1/boxes/"+box.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", resp.StatusCode)
	}
}

func TestCreateRequiresWorktree(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)

	resp, _ := do(t, c, "POST", "/v1/boxes", map[string]string{"project": "alpha"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing worktree status = %d, want 400", resp.StatusCode)
	}

	req, _ := http.NewRequest("POST", "http://pluto/v1/boxes", bytes.NewReader([]byte("{not json")))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("post invalid json: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid json status = %d, want 400", resp.StatusCode)
	}
}

func TestCorruptRecordSurfacesInList(t *testing.T) {
	socket, st := start(t)
	c := client(socket)

	do(t, c, "POST", "/v1/boxes", map[string]string{"worktree": "/src/alpha", "project": "alpha", "branch": "main"})

	badDir := filepath.Join(st.Root(), "boxes", "11111111-2222-4333-8444-555555555555")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "box.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	resp, data := do(t, c, "GET", "/v1/boxes", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var list listJSON
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Boxes) != 1 || len(list.Errors) != 1 {
		t.Fatalf("list boxes=%d errors=%d, want 1/1 (%s)", len(list.Boxes), len(list.Errors), data)
	}
}

func TestListenRefusesWhenSocketIsLive(t *testing.T) {
	socket, _ := start(t)

	other, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer other.Close()

	srv := daemon.New(other, fakeRunner{st: other}, "test")
	if err := srv.Listen(socket); err == nil {
		t.Fatal("Listen should refuse a socket a live daemon owns")
	}
}

func TestBoxLifecycleEndpoints(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)

	resp, data := do(t, c, "POST", "/v1/boxes", map[string]string{
		"worktree": "/src/alpha", "project": "alpha", "branch": "main",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", resp.StatusCode, data)
	}
	var box boxJSON
	if err := json.Unmarshal(data, &box); err != nil {
		t.Fatalf("decode box: %v", err)
	}

	resp, data = do(t, c, "POST", "/v1/boxes/"+box.ID+"/up", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("up status = %d, body %s", resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, &box); err != nil || box.State != "running" {
		t.Fatalf("up box = %+v, err %v", box, err)
	}

	resp, data = do(t, c, "POST", "/v1/boxes/"+box.ID+"/pause", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d, body %s", resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, &box); err != nil || box.State != "paused" {
		t.Fatalf("pause box = %+v, err %v", box, err)
	}

	resp, data = do(t, c, "POST", "/v1/boxes/"+box.ID+"/attach", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attach status = %d, body %s", resp.StatusCode, data)
	}
	var info api.AttachInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("decode attach: %v", err)
	}
	if info.User != "dev" || info.Port != 22 || info.UDS == "" || info.Key == "" {
		t.Fatalf("attach info = %+v", info)
	}

	resp, _ = do(t, c, "POST", "/v1/boxes/11111111-2222-4333-8444-555555555555/up", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("up unknown box status = %d, want 404", resp.StatusCode)
	}
}

func TestImageEndpoints(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)

	resp, data := do(t, c, "POST", "/v1/images", map[string]string{"path": "/artifacts"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d, body %s", resp.StatusCode, data)
	}
	var imported api.ImportImageResponse
	if err := json.Unmarshal(data, &imported); err != nil || imported.Version != "ver123" {
		t.Fatalf("import = %+v, err %v", imported, err)
	}

	resp, _ = do(t, c, "POST", "/v1/images", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("import without path status = %d, want 400", resp.StatusCode)
	}

	resp, data = do(t, c, "GET", "/v1/images", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list images status = %d", resp.StatusCode)
	}
	var list api.ImagesResponse
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("decode images: %v", err)
	}
	if len(list.Images) != 1 || list.Images[0].Version != "ver123" {
		t.Fatalf("images = %+v", list.Images)
	}
}

func TestLogsEndpoint(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)

	resp, data := do(t, c, "POST", "/v1/boxes", map[string]string{
		"worktree": "/src/alpha", "project": "alpha", "branch": "main",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", resp.StatusCode, data)
	}
	var box boxJSON
	if err := json.Unmarshal(data, &box); err != nil {
		t.Fatalf("decode box: %v", err)
	}

	resp, data = do(t, c, "GET", "/v1/boxes/"+box.ID+"/logs?phase=wake&lines=50", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs status = %d, body %s", resp.StatusCode, data)
	}
	var logs api.LogsResponse
	if err := json.Unmarshal(data, &logs); err != nil || logs.Log != "log of wake" {
		t.Fatalf("logs = %+v, err %v", logs, err)
	}

	resp, data = do(t, c, "GET", "/v1/boxes/"+box.ID+"/logs?service=web", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("service logs status = %d", resp.StatusCode)
	}
	if err := json.Unmarshal(data, &logs); err != nil || logs.Log != "log of web" {
		t.Fatalf("service logs = %+v, err %v", logs, err)
	}
}

// createBox registers a box over the API and returns it.
func createBox(t *testing.T, c *http.Client) boxJSON {
	t.Helper()
	resp, data := do(t, c, "POST", "/v1/boxes", map[string]string{
		"worktree": "/src/alpha", "project": "alpha", "branch": "main",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", resp.StatusCode, data)
	}
	var box boxJSON
	if err := json.Unmarshal(data, &box); err != nil {
		t.Fatalf("decode box: %v", err)
	}
	return box
}

func decodeEvents(t *testing.T, data []byte) []api.RunEvent {
	t.Helper()
	var events []api.RunEvent
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var event api.RunEvent
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode run event: %v (%s)", err, data)
		}
		events = append(events, event)
	}
	return events
}

func TestRunEndpointStreamsJobEvents(t *testing.T) {
	var gotArgv []string
	socket, _ := startWith(t, fakeRunner{run: func(box *state.Box, argv []string, emit func([]byte)) (*state.Job, error) {
		gotArgv = argv
		emit([]byte("first\n"))
		emit([]byte("second\n"))
		job := state.StartJob(state.NewID(), argv)
		now := time.Now().UTC()
		job.State = state.JobDone
		job.FinishedAt = &now
		return &job, nil
	}})
	c := client(socket)
	box := createBox(t, c)

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", api.RunRequest{Argv: []string{"echo", "hi"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run status = %d, body %s", resp.StatusCode, data)
	}
	if len(gotArgv) != 2 || gotArgv[0] != "echo" || gotArgv[1] != "hi" {
		t.Fatalf("argv = %v, want the request's argv", gotArgv)
	}

	events := decodeEvents(t, data)
	if len(events) != 3 {
		t.Fatalf("events = %+v, want two outputs and an exit", events)
	}
	if events[0].Type != api.RunOutput || string(events[0].Data) != "first\n" {
		t.Fatalf("event 0 = %+v", events[0])
	}
	if events[1].Type != api.RunOutput || string(events[1].Data) != "second\n" {
		t.Fatalf("event 1 = %+v", events[1])
	}
	if events[2].Type != api.RunExit || events[2].Job == nil || events[2].Job.State != state.JobDone {
		t.Fatalf("event 2 = %+v, want a done job", events[2])
	}
}

// TestRunEndpointStreamsBeforeTheJobEnds pins the flush: output must reach
// the client while the job is still running, not when the handler returns.
func TestRunEndpointStreamsBeforeTheJobEnds(t *testing.T) {
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	socket, _ := startWith(t, fakeRunner{run: func(box *state.Box, argv []string, emit func([]byte)) (*state.Job, error) {
		emit([]byte("first\n"))
		<-release
		emit([]byte("second\n"))
		job := state.StartJob(state.NewID(), argv)
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	c := client(socket)
	c.Timeout = 5 * time.Second
	box := createBox(t, c)

	body, err := json.Marshal(api.RunRequest{Argv: []string{"long"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := c.Post("http://pluto/v1/boxes/"+box.ID+"/run", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post run: %v", err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 4)
	go func() {
		defer close(lines)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				return
			}
		}
	}()
	readEvent := func() api.RunEvent {
		t.Helper()
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("run stream closed early")
			}
			var event api.RunEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("decode event: %v (%q)", err, line)
			}
			return event
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for a run event")
		}
		return api.RunEvent{}
	}

	first := readEvent()
	if first.Type != api.RunOutput || string(first.Data) != "first\n" {
		t.Fatalf("first event = %+v, want the first output before the job ends", first)
	}
	close(release)
	if second := readEvent(); second.Type != api.RunOutput || string(second.Data) != "second\n" {
		t.Fatalf("second event = %+v, want the second output", second)
	}
	if exit := readEvent(); exit.Type != api.RunExit || exit.Job == nil || exit.Job.State != state.JobDone {
		t.Fatalf("exit event = %+v, want a done job", exit)
	}
}

func TestRunEndpointRefusesConcurrentRun(t *testing.T) {
	socket, _ := startWith(t, fakeRunner{run: func(box *state.Box, argv []string, emit func([]byte)) (*state.Job, error) {
		return nil, fmt.Errorf("%w: %q", state.ErrJobRunning, "make")
	}})
	c := client(socket)
	box := createBox(t, c)

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", api.RunRequest{Argv: []string{"make"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("run status = %d, want 409 (body %s)", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "already running") {
		t.Fatalf("body = %s, want the refusal reason", data)
	}
}

func TestRunEndpointRequiresArgv(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)
	box := createBox(t, c)

	resp, _ := do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", api.RunRequest{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("run without argv status = %d, want 400", resp.StatusCode)
	}
}

func TestJobLogsEndpoint(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)
	box := createBox(t, c)
	jobID := state.NewID()

	resp, data := do(t, c, "GET", "/v1/boxes/"+box.ID+"/logs?job="+jobID+"&lines=5", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job logs status = %d, body %s", resp.StatusCode, data)
	}
	var logs api.LogsResponse
	if err := json.Unmarshal(data, &logs); err != nil {
		t.Fatalf("decode job logs: %v", err)
	}
	if logs.Log != "job log of "+jobID {
		t.Fatalf("job log = %q, want the runner's log", logs.Log)
	}
}

// runningBox creates a box and moves it to running over the API.
func runningBox(t *testing.T, c *http.Client) boxJSON {
	t.Helper()
	box := createBox(t, c)
	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/up", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("up status = %d, body %s", resp.StatusCode, data)
	}
	return box
}

func TestAutoPauseLoopPausesIdleBox(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{window: 30 * time.Millisecond})
	c := client(socket)
	box := runningBox(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.AutoPauseLoop(ctx, 5*time.Millisecond)

	waitFor(t, "box paused", func() bool {
		b, err := st.Box(box.ID)
		return err == nil && b.State == state.StatePaused
	})
}

func TestAutoPauseLoopKeepsAttachedBoxRunning(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{window: 30 * time.Millisecond, clients: 1})
	c := client(socket)
	box := runningBox(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.AutoPauseLoop(ctx, 5*time.Millisecond)

	// Several windows pass; the attached client must hold the pause off.
	time.Sleep(150 * time.Millisecond)
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running while a client is attached", got.State)
	}
	if got.IdleSince != nil {
		t.Fatalf("idle_since = %v, want the clock clear while attached", got.IdleSince)
	}
}

func TestAutoPauseLoopKeepsJobRunningBoxRunning(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{window: 30 * time.Millisecond})
	c := client(socket)
	box := runningBox(t, c)
	if _, err := st.BeginJob(box.ID, state.StartJob(state.NewID(), []string{"make"})); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.AutoPauseLoop(ctx, 5*time.Millisecond)

	time.Sleep(150 * time.Millisecond)
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running while a job runs", got.State)
	}
}

func TestAutoPauseLoopLeavesWindowOffBoxesAlone(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{window: 0})
	c := client(socket)
	box := runningBox(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.AutoPauseLoop(ctx, 5*time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running with auto-pause off", got.State)
	}
	if got.AutoPause != "off" {
		t.Fatalf("auto_pause = %q, want off recorded", got.AutoPause)
	}
}

func TestAutoPauseLoopNeverPausesWithoutALiveView(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{window: 30 * time.Millisecond, refreshErr: errors.New("agent unreachable")})
	c := client(socket)
	box := runningBox(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.AutoPauseLoop(ctx, 5*time.Millisecond)

	time.Sleep(150 * time.Millisecond)
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running when the agent cannot be reached", got.State)
	}
	if got.IdleSince != nil {
		t.Fatalf("idle_since = %v, want no clock without a live view", got.IdleSince)
	}
}

func TestGetRecordsTheAutoPauseWindow(t *testing.T) {
	socket, _, _ := startServer(t, fakeRunner{window: 45 * time.Minute})
	c := client(socket)
	box := runningBox(t, c)

	resp, data := do(t, c, "GET", "/v1/boxes/"+box.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body %s", resp.StatusCode, data)
	}
	var got boxJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode box: %v (%s)", err, data)
	}
	if got.AutoPause != "45m0s" {
		t.Fatalf("auto_pause = %q, want the evaluated window", got.AutoPause)
	}
	if got.IdleSince == nil {
		t.Fatalf("idle_since = nil, want the idle clock to start on the first look")
	}
}

func TestGetClearsTheIdleClockWhileAttached(t *testing.T) {
	socket, _, _ := startServer(t, fakeRunner{window: 45 * time.Minute, clients: 1})
	c := client(socket)
	box := runningBox(t, c)

	resp, data := do(t, c, "GET", "/v1/boxes/"+box.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body %s", resp.StatusCode, data)
	}
	var got boxJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode box: %v (%s)", err, data)
	}
	if got.IdleSince != nil {
		t.Fatalf("idle_since = %v, want it cleared while a client is attached", got.IdleSince)
	}
}

func TestGetDoesNotPause(t *testing.T) {
	socket, st, _ := startServer(t, fakeRunner{window: time.Millisecond})
	c := client(socket)
	box := runningBox(t, c)

	// The window is already over, but only the loop pauses.
	time.Sleep(10 * time.Millisecond)
	do(t, c, "GET", "/v1/boxes/"+box.ID, nil)
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want status to report, not pause", got.State)
	}
}
