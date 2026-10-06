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
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
)

// fakeRunner stands in for the box lifecycle in daemon tests: it moves
// records through the same states the real runner would. run scripts a job;
// without one, runs succeed silently. window is the auto-pause window the
// runner reports; clients and refreshErr shape what Refresh sees. record
// makes RunJob write the job into the store the way the real runner does;
// fired receives every executed spec.
type fakeRunner struct {
	st             *state.Store
	advance        func(*state.Box, string, string) error
	run            func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error)
	upErr          error
	record         bool
	fired          chan contract.Exec
	window         time.Duration
	clients        int
	unknownClients bool
	refreshErr     error
	stale          bool
	// usage, when non-nil, is the session cgroup reading Refresh reports. Its
	// error stands in for an unreadable cgroup (an unknown fact). A nil func
	// reports a known, quiet read.
	usage func() (state.SessionUsage, error)
	// metrics is the latest Firecracker metrics snapshot the runner would read;
	// metricsErr stands in for a box that has not flushed metrics yet.
	metrics    json.RawMessage
	metricsErr error
}

func (f fakeRunner) Up(ctx context.Context, box *state.Box) (*state.Box, error) {
	if f.upErr != nil {
		return nil, f.upErr
	}
	return f.st.Transition(box.ID, state.StateRunning)
}

func (f fakeRunner) AdvancePRRef(ctx context.Context, box *state.Box, bundle, ref string) error {
	if f.advance != nil {
		return f.advance(box, bundle, ref)
	}
	return nil
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
	phases := state.Phases{Synced: true}
	if !f.unknownClients {
		n := f.clients
		phases.Clients = &n
	}
	// A real agent reports the sessions' cumulative cgroup counters on every
	// status; a failed read leaves the fact nil (unknown). A nil func here
	// stands for a known, quiet read.
	if f.usage != nil {
		if usage, err := f.usage(); err == nil {
			phases.SessionUsage = &usage
		}
	} else {
		usage := state.SessionUsage{}
		phases.SessionUsage = &usage
	}
	return f.st.SetPhases(box.ID, phases)
}

func (f fakeRunner) AutoPauseWindow(box *state.Box) time.Duration { return f.window }

func (f fakeRunner) ContractStale(box *state.Box) bool { return f.stale }

func (f fakeRunner) Logs(box *state.Box, phase, service string, lines int) (string, error) {
	return "log of " + phase + service, nil
}

func (f fakeRunner) RunJob(ctx context.Context, box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Box, *state.Job, error) {
	if f.fired != nil {
		f.fired <- spec
	}
	if f.run != nil {
		job, err := f.run(box, spec, emit)
		if err != nil {
			return nil, nil, err
		}
		return box, job, nil
	}
	job := state.StartJobCommand(state.NewID(), spec.Command.String())
	if !f.record {
		job.Finish(state.JobDone, 0, "")
		return box, &job, nil
	}
	// Mirror the real runner: ensure the box is up, record the job as
	// running, then record its outcome.
	if _, err := f.Up(ctx, box); err != nil {
		return nil, nil, err
	}
	if _, err := f.st.BeginJob(box.ID, job); err != nil {
		return nil, nil, err
	}
	job.Finish(state.JobDone, 0, "")
	updated, err := f.st.SetJob(box.ID, job)
	if err != nil {
		return nil, nil, err
	}
	return updated, &job, nil
}

func (f fakeRunner) JobLog(box *state.Box, jobID string, lines int) (string, error) {
	return "job log of " + jobID, nil
}

func (f fakeRunner) Metrics(box *state.Box) (json.RawMessage, error) {
	if f.metricsErr != nil {
		return nil, f.metricsErr
	}
	return f.metrics, nil
}

func (f fakeRunner) Destroy(id string) error { return f.st.DestroyBox(id) }

func (f fakeRunner) Import(srcDir string) (string, error) { return "ver123", nil }

func (f fakeRunner) Images() ([]api.ImageInfo, error) {
	return []api.ImageInfo{{Version: "ver123"}}, nil
}

type boxJSON struct {
	Schema        int        `json:"schema"`
	ID            string     `json:"id"`
	Project       string     `json:"project"`
	Branch        string     `json:"branch"`
	Worktree      string     `json:"worktree"`
	State         string     `json:"state"`
	AutoPause     string     `json:"auto_pause"`
	IdleSince     *time.Time `json:"idle_since"`
	ContractStale bool       `json:"contract_stale"`
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

// runAutoPauseLoop starts the loop and stops it before the test's temp dirs
// are cleaned up. The loop writes to the store; a write racing RemoveAll
// fails cleanup with "directory not empty", so the helper cancels and waits
// for the goroutine to exit (its cleanup runs first, LIFO).
func runAutoPauseLoop(t *testing.T, srv *daemon.Server, interval time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.AutoPauseLoop(ctx, interval)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
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

func TestMetricsEndpointReturnsLatestSnapshot(t *testing.T) {
	socket, st := startWith(t, fakeRunner{metrics: json.RawMessage(`{"utc_timestamp_ms":7,"vmm":{"panic_count":0}}`)})
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	resp, data := do(t, client(socket), "GET", "/v1/boxes/"+box.ID+"/metrics", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, data)
	}
	var out struct {
		Metrics json.RawMessage `json:"metrics"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode metrics: %v (%s)", err, data)
	}
	if !strings.Contains(string(out.Metrics), `"utc_timestamp_ms":7`) {
		t.Fatalf("metrics = %s, want the latest snapshot", out.Metrics)
	}
}

func TestMetricsEndpointNotFoundBeforeFirstFlush(t *testing.T) {
	socket, st := startWith(t, fakeRunner{metricsErr: os.ErrNotExist})
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	resp, _ := do(t, client(socket), "GET", "/v1/boxes/"+box.ID+"/metrics", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 before Firecracker flushes metrics", resp.StatusCode)
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

func TestCreateBoxFromRemoteURLWithoutLocalWorktree(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=trunk", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if out, err := exec.Command("git", "clone", remote, seed).CombinedOutput(); err != nil {
		t.Fatalf("clone seed: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(seed, ".pluto.toml"), []byte("[jobs.check]\ncommand = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", seed, "add", ".pluto.toml").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "contract").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", seed, "push", "origin", "trunk").CombinedOutput(); err != nil {
		t.Fatalf("push: %v: %s", err, out)
	}
	socket, st := start(t)
	resp, data := do(t, client(socket), "POST", "/v1/boxes", api.CreateBoxRequest{RepoURL: remote})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, data)
	}
	var box state.Box
	if err := json.Unmarshal(data, &box); err != nil {
		t.Fatal(err)
	}
	if box.Branch != "trunk" || box.PrimaryRepoURL != remote {
		t.Fatalf("box branch/primary = %q/%q", box.Branch, box.PrimaryRepoURL)
	}
	if _, err := os.Stat(filepath.Join(box.Worktree, ".pluto.toml")); err != nil {
		t.Fatalf("default branch contract unavailable in checkout: %v", err)
	}
	remotes, err := exec.Command("git", "-C", box.Worktree, "remote", "get-url", "origin").Output()
	if err != nil || strings.TrimSpace(string(remotes)) != remote {
		t.Fatalf("initialized remote = %q, err=%v", remotes, err)
	}
	if _, err := st.Box(box.ID); err != nil {
		t.Fatal(err)
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

// Attach validates a named session against the box's worktree contract at
// request time (ADR 0007): a declared name connects, an unknown one is
// rejected before the box is woken, with a fact the CLI turns into a hint.
func TestAttachValidatesSessionAgainstTheWorktreeContract(t *testing.T) {
	socket, st := start(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, contract.FileName), []byte("[sessions.agent]\ncommand = \"sleep 1\"\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	box, _, err := st.CreateBox("app", "main", dir)
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	c := client(socket)

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/attach", api.AttachRequest{Session: "agent"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("declared session attach status = %d, body %s", resp.StatusCode, data)
	}

	resp, data = do(t, c, "POST", "/v1/boxes/"+box.ID+"/attach", api.AttachRequest{Session: "ghost"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown session attach status = %d, want 400 (body %s)", resp.StatusCode, data)
	}
	var apiErr api.Error
	if err := json.Unmarshal(data, &apiErr); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !apiErr.Session || !strings.Contains(apiErr.Error, "ghost") {
		t.Fatalf("error = %+v, want the session fact and the name", apiErr)
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
	var gotSpec contract.Exec
	socket, _ := startWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		gotSpec = spec
		emit([]byte("first\n"))
		emit([]byte("second\n"))
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
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
	if got := gotSpec.Command.Argv(); len(got) != 2 || got[0] != "echo" || got[1] != "hi" {
		t.Fatalf("argv = %v, want the request's argv", got)
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

func TestQueueRequestReturnsDurableIDAndRunsWhenCapacityIsAvailable(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	c := client(socket)
	box := createBox(t, c)
	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/queue", api.QueueRequest{Up: true})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("queue status=%d body=%s", resp.StatusCode, data)
	}
	var accepted api.QueueResponse
	if err := json.Unmarshal(data, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Item.ID == "" || accepted.Item.State != state.QueuePending {
		t.Fatalf("accepted item=%+v", accepted.Item)
	}
	items, err := st.Queue()
	if err != nil || len(items) != 1 || items[0].ID != accepted.Item.ID {
		t.Fatalf("durable queue=%+v err=%v", items, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.SchedulerLoop(ctx, 5*time.Millisecond)
	waitFor(t, "queued up request", func() bool { q, e := st.Queue(); return e == nil && q[0].State == state.QueueDone })
	updated, err := st.Box(box.ID)
	if err != nil || updated.State != state.StateRunning {
		t.Fatalf("box=%+v err=%v", updated, err)
	}
}

func TestQueueRequestShowsQueueFull(t *testing.T) {
	socket, _, srv := startServer(t, fakeRunner{})
	srv.QueueCapacity = 1
	c := client(socket)
	box := createBox(t, c)
	for n := 0; n < 2; n++ {
		resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/queue", api.QueueRequest{Up: true})
		want := http.StatusAccepted
		if n == 1 {
			want = http.StatusServiceUnavailable
		}
		if resp.StatusCode != want {
			t.Fatalf("request %d status=%d want=%d body=%s", n, resp.StatusCode, want, data)
		}
	}
}

func TestQueueWaitsForRunningBoxCapacityThenStarts(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	srv.MaxRunningBoxes = 1
	c := client(socket)
	first := createBoxAt(t, c, t.TempDir())
	second := createBoxAt(t, c, t.TempDir())
	if _, err := st.Transition(first.ID, state.StateRunning); err != nil {
		t.Fatal(err)
	}
	resp, data := do(t, c, "POST", "/v1/boxes/"+second.ID+"/queue", api.QueueRequest{Up: true})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("queue status=%d body=%s", resp.StatusCode, data)
	}
	var accepted api.QueueResponse
	if err := json.Unmarshal(data, &accepted); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.SchedulerLoop(ctx, 5*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	items, err := st.Queue()
	if err != nil || items[0].State != state.QueuePending {
		t.Fatalf("at capacity queue=%+v err=%v", items, err)
	}
	if _, err := st.Transition(first.ID, state.StatePaused); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "queued box capacity", func() bool { items, e := st.Queue(); return e == nil && items[0].State == state.QueueDone })
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
	socket, _ := startWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		emit([]byte("first\n"))
		<-release
		emit([]byte("second\n"))
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
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
	socket, _ := startWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
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

func TestRunEndpointRequiresArgvOrJob(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)
	box := createBox(t, c)

	resp, _ := do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", api.RunRequest{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("run without argv or job status = %d, want 400", resp.StatusCode)
	}
	resp, _ = do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", api.RunRequest{Job: "dev", Argv: []string{"make"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("run with both argv and job status = %d, want 400", resp.StatusCode)
	}
}

// createBoxAt registers a box whose worktree is a real directory, so the
// daemon can read its .pluto.toml at run time.
func createBoxAt(t *testing.T, c *http.Client, worktree string) boxJSON {
	t.Helper()
	resp, data := do(t, c, "POST", "/v1/boxes", map[string]string{
		"worktree": worktree, "project": filepath.Base(worktree), "branch": "main",
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

func writeContract(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, contract.FileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	return dir
}

func TestRunEndpointResolvesNamedJobFromTheWorktreeContract(t *testing.T) {
	var gotSpec contract.Exec
	socket, _ := startWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		gotSpec = spec
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), `
[env]
NODE_ENV = "test"

[jobs.dev]
description = "start the dev server"
command = ["pnpm", "dev"]
dir = "web"
env = { PORT = "3000" }
timeout = "30m"
`)
	box := createBoxAt(t, c, worktree)

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", api.RunRequest{Job: "dev"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run status = %d, body %s", resp.StatusCode, data)
	}
	if got := strings.Join(gotSpec.Command.Argv(), " "); got != "pnpm dev" {
		t.Fatalf("command = %q, want the declared job", got)
	}
	if gotSpec.Dir != "web" || gotSpec.Env["NODE_ENV"] != "test" || gotSpec.Env["PORT"] != "3000" || gotSpec.Timeout != 30*time.Minute {
		t.Fatalf("spec = %+v, want the job's dir/env/timeout", gotSpec)
	}
	events := decodeEvents(t, data)
	if last := events[len(events)-1]; last.Type != api.RunExit || last.Job.Command != "pnpm dev" {
		t.Fatalf("exit event = %+v, want the declared display", last)
	}
}

func TestRunEndpointAdHocAppliesTopLevelEnv(t *testing.T) {
	var gotSpec contract.Exec
	socket, _ := startWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		gotSpec = spec
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		job.Finish(state.JobDone, 0, "")
		return &job, nil
	}})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[env]\nFOO = \"bar\"\n")
	box := createBoxAt(t, c, worktree)

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", api.RunRequest{Argv: []string{"make", "test"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run status = %d, body %s", resp.StatusCode, data)
	}
	if gotSpec.Env["FOO"] != "bar" || gotSpec.Timeout != 0 {
		t.Fatalf("spec = %+v, want the top-level env", gotSpec)
	}
}

func TestRunEndpointUnknownJobListsDeclaredJobs(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), `
[jobs.dev]
description = "start the dev server"
command = "true"
`)
	box := createBoxAt(t, c, worktree)

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", api.RunRequest{Job: "web"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown job status = %d, want 400 (body %s)", resp.StatusCode, data)
	}
	for _, want := range []string{"no such job", "web", "dev (start the dev server)"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("body = %s, want %q", data, want)
		}
	}
}

func TestRunEndpointBrokenContractFailsClearly(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.dev]\ncommand = 5\n")
	box := createBoxAt(t, c, worktree)

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", api.RunRequest{Job: "dev"})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("broken contract status = %d, want 500 (body %s)", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), contract.FileName) {
		t.Fatalf("body = %s, want the contract path", data)
	}
	// The wire carries the contract fact, not the hint: the CLI turns it
	// into the edit step (ADR 0009).
	var apiErr api.Error
	if err := json.Unmarshal(data, &apiErr); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, data)
	}
	if !apiErr.Contract {
		t.Fatalf("body = %s, want contract:true", data)
	}
}

// A contract failure anywhere in the runner keeps the fact on the wire, so
// up and attach get the edit hint too.
func TestContractFailureIsMarkedOnTheWire(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, contract.FileName), []byte("[provision]\ncommand = [\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	_, loadErr := contract.Load(dir)
	if loadErr == nil {
		t.Fatal("the contract should fail to load")
	}

	socket, _, _ := startServer(t, fakeRunner{upErr: loadErr})
	c := client(socket)
	box := createBox(t, c)
	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/up", nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("up status = %d, want 500 (body %s)", resp.StatusCode, data)
	}
	var apiErr api.Error
	if err := json.Unmarshal(data, &apiErr); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, data)
	}
	if !apiErr.Contract || !strings.Contains(apiErr.Error, contract.FileName) {
		t.Fatalf("body = %s, want the contract fact and path", data)
	}
}

// recordJob seeds a finished job straight into the store, so daemon tests
// can exercise job history without driving the runner.
func recordJob(t *testing.T, st *state.Store, boxID, command string) state.Job {
	t.Helper()
	job := state.StartJob(state.NewID(), strings.Fields(command))
	if _, err := st.BeginJob(boxID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	job.Finish(state.JobDone, 0, "")
	if _, err := st.SetJob(boxID, job); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	return job
}

func TestJobLogsEndpoint(t *testing.T) {
	socket, st := start(t)
	c := client(socket)
	box := createBox(t, c)
	job := recordJob(t, st, box.ID, "make test")

	resp, data := do(t, c, "GET", "/v1/boxes/"+box.ID+"/logs?job="+job.ID+"&lines=5", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job logs status = %d, body %s", resp.StatusCode, data)
	}
	var logs api.LogsResponse
	if err := json.Unmarshal(data, &logs); err != nil {
		t.Fatalf("decode job logs: %v", err)
	}
	if logs.Log != "job log of "+job.ID {
		t.Fatalf("job log = %q, want the runner's log", logs.Log)
	}
}

// TestGetBoxCarriesJobHistory pins the wire shape `pluto jobs` reads: the
// box record's retained history, newest first.
func TestGetBoxCarriesJobHistory(t *testing.T) {
	socket, st := start(t)
	c := client(socket)
	box := createBox(t, c)
	first := recordJob(t, st, box.ID, "first")
	second := recordJob(t, st, box.ID, "second")

	resp, data := do(t, c, "GET", "/v1/boxes/"+box.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body %s", resp.StatusCode, data)
	}
	var got struct {
		Jobs []state.Job `json:"jobs"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode box: %v (%s)", err, data)
	}
	if len(got.Jobs) != 2 || got.Jobs[0].ID != second.ID || got.Jobs[1].ID != first.ID {
		t.Fatalf("jobs = %+v, want newest first", got.Jobs)
	}
}

// TestJobLogsResolvesHistory pins the daemon's --job resolution: last, an
// unambiguous prefix, and a 404 for a job the box does not retain.
func TestJobLogsResolvesHistory(t *testing.T) {
	socket, st := start(t)
	c := client(socket)
	box := createBox(t, c)
	first := recordJob(t, st, box.ID, "first")
	second := recordJob(t, st, box.ID, "second")

	decode := func(data []byte) api.LogsResponse {
		t.Helper()
		var logs api.LogsResponse
		if err := json.Unmarshal(data, &logs); err != nil {
			t.Fatalf("decode logs: %v (%s)", err, data)
		}
		return logs
	}

	resp, data := do(t, c, "GET", "/v1/boxes/"+box.ID+"/logs?job=last", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("last status = %d, body %s", resp.StatusCode, data)
	}
	if logs := decode(data); logs.Log != "job log of "+second.ID {
		t.Fatalf("last log = %q, want the newest job %s", logs.Log, second.ID)
	}

	resp, data = do(t, c, "GET", "/v1/boxes/"+box.ID+"/logs?job="+first.ID[:8], nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prefix status = %d, body %s", resp.StatusCode, data)
	}
	if logs := decode(data); logs.Log != "job log of "+first.ID {
		t.Fatalf("prefix log = %q, want the older job %s", logs.Log, first.ID)
	}

	resp, _ = do(t, c, "GET", "/v1/boxes/"+box.ID+"/logs?job=deadbeef", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job status = %d, want 404", resp.StatusCode)
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

	runAutoPauseLoop(t, srv, 5*time.Millisecond)

	waitFor(t, "box paused", func() bool {
		b, err := st.Box(box.ID)
		return err == nil && b.State == state.StatePaused
	})
}

func TestAutoPauseLoopKeepsAttachedBoxRunning(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{window: 30 * time.Millisecond, clients: 1})
	c := client(socket)
	box := runningBox(t, c)

	runAutoPauseLoop(t, srv, 5*time.Millisecond)

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

	runAutoPauseLoop(t, srv, 5*time.Millisecond)

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

	runAutoPauseLoop(t, srv, 5*time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running with auto-pause off", got.State)
	}
	if got.AutoPauseSetting != "off" {
		t.Fatalf("auto_pause = %q, want off recorded", got.AutoPauseSetting)
	}
}

func TestAutoPauseLoopNeverPausesWithoutALiveView(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{window: 30 * time.Millisecond, refreshErr: errors.New("agent unreachable")})
	c := client(socket)
	box := runningBox(t, c)

	runAutoPauseLoop(t, srv, 5*time.Millisecond)

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

func TestGetReportsAStaleContract(t *testing.T) {
	socket, _, _ := startServer(t, fakeRunner{stale: true})
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
	if !got.ContractStale {
		t.Fatal("contract_stale = false, want the response to flag the divergence")
	}
}

func TestGetStaysSilentForACurrentContract(t *testing.T) {
	socket, _, _ := startServer(t, fakeRunner{})
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
	if got.ContractStale {
		t.Fatal("contract_stale = true, want equal contracts silent")
	}
	if bytes.Contains(data, []byte("contract_stale")) {
		t.Fatalf("response carries contract_stale for a current contract: %s", data)
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

func TestAutoPauseLoopNeverPausesWhenClientsAreUnknown(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{window: 30 * time.Millisecond, unknownClients: true})
	c := client(socket)
	box := runningBox(t, c)

	runAutoPauseLoop(t, srv, 5*time.Millisecond)

	time.Sleep(150 * time.Millisecond)
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running when the client count is unknown", got.State)
	}
	if got.IdleSince != nil {
		t.Fatalf("idle_since = %v, want no clock without a client count", got.IdleSince)
	}
}

// runAutoPauseLoopOnClock starts the auto-pause loop on the test's clock, so
// a test can let a sample land and then advance time exactly.
func runAutoPauseLoopOnClock(t *testing.T, srv *daemon.Server, clock *schedulerClock) {
	t.Helper()
	srv.Now = clock.Now
	runAutoPauseLoop(t, srv, 5*time.Millisecond)
}

// A detached session that keeps burning CPU/IO must hold the box awake past
// its idle window: the session cgroup counters grow on every look.
func TestAutoPauseLoopKeepsWorkingSessionAwake(t *testing.T) {
	var calls atomic.Int64
	socket, st, srv := startServer(t, fakeRunner{
		window: time.Hour,
		usage: func() (state.SessionUsage, error) {
			return state.SessionUsage{CPUUsec: calls.Add(1) * 1_000_000}, nil
		},
	})
	c := client(socket)
	box := runningBox(t, c)

	clock := &schedulerClock{now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	runAutoPauseLoopOnClock(t, srv, clock)
	// Let a baseline and at least one growing sample land.
	waitFor(t, "the daemon to sample the working session", func() bool { return calls.Load() >= 3 })

	// Two windows later, the burning session is still working.
	clock.set(clock.Now().Add(2 * time.Hour))
	time.Sleep(50 * time.Millisecond)

	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running while a session burns CPU", got.State)
	}
	if got.IdleSince != nil {
		t.Fatalf("idle_since = %v, want no idle clock while a session is working", got.IdleSince)
	}
}

// A quiet session with the same windows must let the box sleep once the idle
// window elapses: the counters never move.
func TestAutoPauseLoopPausesIdleSession(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{
		window: time.Hour,
		usage:  func() (state.SessionUsage, error) { return state.SessionUsage{CPUUsec: 7, IOBytes: 9}, nil },
	})
	c := client(socket)
	box := runningBox(t, c)

	clock := &schedulerClock{now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	runAutoPauseLoopOnClock(t, srv, clock)
	waitFor(t, "the idle clock to start", func() bool {
		b, err := st.Box(box.ID)
		return err == nil && b.IdleSince != nil
	})

	clock.set(clock.Now().Add(time.Hour + time.Minute))
	waitFor(t, "box paused", func() bool {
		b, err := st.Box(box.ID)
		return err == nil && b.State == state.StatePaused
	})
}

// A session whose CPU grows below the noise floor is idle: idle guests keep
// burning a little (journald, sshd keepalives) and observing a session
// perturbs its own cgroup, so a floor keeps the box sleepable.
func TestAutoPauseLoopIgnoresCPUBelowTheNoiseFloor(t *testing.T) {
	var calls atomic.Int64
	socket, st, srv := startServer(t, fakeRunner{
		window: time.Hour,
		usage: func() (state.SessionUsage, error) {
			// 1 ms per look, well under the 50 ms floor.
			return state.SessionUsage{CPUUsec: calls.Add(1) * 1_000}, nil
		},
	})
	c := client(socket)
	box := runningBox(t, c)

	clock := &schedulerClock{now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	runAutoPauseLoopOnClock(t, srv, clock)
	waitFor(t, "the idle clock to start", func() bool {
		b, err := st.Box(box.ID)
		return err == nil && b.IdleSince != nil
	})

	clock.set(clock.Now().Add(time.Hour + time.Minute))
	waitFor(t, "the box to pause below the noise floor", func() bool {
		b, err := st.Box(box.ID)
		return err == nil && b.State == state.StatePaused
	})
}

// A session delta above the noise floor is work and holds the box awake, even
// when the idle window has long elapsed.
func TestAutoPauseLoopStaysAwakeAboveTheNoiseFloor(t *testing.T) {
	var calls atomic.Int64
	socket, st, srv := startServer(t, fakeRunner{
		window: time.Hour,
		usage: func() (state.SessionUsage, error) {
			// 100 ms per look, just over the 50 ms floor.
			return state.SessionUsage{CPUUsec: calls.Add(1) * 100_000}, nil
		},
	})
	c := client(socket)
	box := runningBox(t, c)

	clock := &schedulerClock{now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	runAutoPauseLoopOnClock(t, srv, clock)
	waitFor(t, "the daemon to sample the working session", func() bool { return calls.Load() >= 3 })

	clock.set(clock.Now().Add(2 * time.Hour))
	time.Sleep(50 * time.Millisecond)

	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running above the noise floor", got.State)
	}
	if got.IdleSince != nil {
		t.Fatalf("idle_since = %v, want no idle clock above the noise floor", got.IdleSince)
	}
}

// A session reading the agent cannot produce is unknown, never idle: the box
// stays awake and no idle clock starts, mirroring an unknown client count.
func TestAutoPauseLoopNeverPausesWhenSessionUsageIsUnknown(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{
		window: time.Hour,
		usage:  func() (state.SessionUsage, error) { return state.SessionUsage{}, errors.New("cgroup unreadable") },
	})
	c := client(socket)
	box := runningBox(t, c)

	clock := &schedulerClock{now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	runAutoPauseLoopOnClock(t, srv, clock)
	time.Sleep(30 * time.Millisecond)

	clock.set(clock.Now().Add(2 * time.Hour))
	time.Sleep(50 * time.Millisecond)

	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running when the session reading is unknown", got.State)
	}
	if got.IdleSince != nil {
		t.Fatalf("idle_since = %v, want no clock without a session reading", got.IdleSince)
	}
}

func TestGetMarksNoLiveView(t *testing.T) {
	socket, st, _ := startServer(t, fakeRunner{window: 30 * time.Minute, refreshErr: errors.New("agent unreachable")})
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
	if got.AutoPause != "unknown" {
		t.Fatalf("auto_pause = %q, want unknown without a live view", got.AutoPause)
	}
	// The marker is response-only; the record keeps what the last evaluation
	// wrote.
	record, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if record.AutoPauseSetting == "unknown" {
		t.Fatalf("record = %q, want the marker kept out of the store", record.AutoPauseSetting)
	}
}
