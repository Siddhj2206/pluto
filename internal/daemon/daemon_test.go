package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
)

// fakeRunner stands in for the VM lifecycle in daemon tests: it moves
// records through the same states the real runner would.
type fakeRunner struct{ st *state.Store }

func (f fakeRunner) Up(ctx context.Context, box *state.Box) (*state.Box, error) {
	return f.st.Transition(box.ID, state.StateRunning)
}

func (f fakeRunner) Pause(ctx context.Context, box *state.Box) (*state.Box, error) {
	return f.st.Transition(box.ID, state.StatePaused)
}

func (f fakeRunner) Attach(ctx context.Context, box *state.Box) (api.AttachInfo, error) {
	return api.AttachInfo{User: "dev", UDS: "/tmp/v.sock", Key: "/tmp/id", Port: 22}, nil
}

func (f fakeRunner) Reconcile(box *state.Box) (*state.Box, error) { return box, nil }

func (f fakeRunner) Destroy(ctx context.Context, id string) error { return f.st.DestroyBox(id) }

func (f fakeRunner) Import(srcDir string) (string, error) { return "ver123", nil }

func (f fakeRunner) Images() ([]api.ImageInfo, error) {
	return []api.ImageInfo{{Version: "ver123"}}, nil
}

type boxJSON struct {
	Schema   int    `json:"schema"`
	ID       string `json:"id"`
	Project  string `json:"project"`
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"`
	State    string `json:"state"`
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
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	socket = filepath.Join(dir, "pluto.sock")
	srv := daemon.New(st, fakeRunner{st}, "test")
	if err := srv.Listen(socket); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	return socket, st
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

	srv := daemon.New(other, fakeRunner{other}, "test")
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
