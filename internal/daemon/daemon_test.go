package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
)

// startServer starts the real daemon over a real unix socket with no box
// lifecycle wired, for the routes that do not need one.
func startServer(t *testing.T) (socket string, st *state.Store, srv *daemon.Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv = daemon.New(st, "test")
	socket = filepath.Join(dir, "pluto.sock")
	if err := srv.Listen(socket); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return socket, st, srv
}

func client(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
}

func do(t *testing.T, c *http.Client, method, path string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, "http://pluto"+path, nil)
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
	socket, _, _ := startServer(t)
	resp, data := do(t, client(socket), "GET", "/v1/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, data)
	}
	var h struct {
		Status       string `json:"status"`
		StateVersion int    `json:"state_version"`
	}
	if err := json.Unmarshal(data, &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h.Status != "ok" || h.StateVersion != state.StateVersion {
		t.Fatalf("health = %+v", h)
	}
}

// A box route with no lifecycle wired reports the lifecycle is not configured
// rather than panicking.
func TestBoxRoutesReportAMissingLifecycle(t *testing.T) {
	socket, _, _ := startServer(t)
	resp, _ := do(t, client(socket), "GET", "/v1/box")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/box status = %d, want 503", resp.StatusCode)
	}
}

// A live daemon's socket is not silently replaced.
func TestListenRefusesWhenSocketIsLive(t *testing.T) {
	socket, _, _ := startServer(t)
	second, err := state.Open(filepath.Join(t.TempDir(), "state2"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { second.Close() })
	if err := daemon.New(second, "test").Listen(socket); err == nil {
		t.Fatal("Listen replaced a live socket")
	}
}
