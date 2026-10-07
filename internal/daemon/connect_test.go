package daemon_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/provider"
	"github.com/Siddhj2206/pluto/internal/state"
)

type ingressTestProvider struct {
	status provider.Status
	got    provider.ServiceAccessRequest
}

func (p *ingressTestProvider) Info() provider.Info {
	return provider.Info{ID: "fake-ingress", Name: "Fake ingress", Capabilities: []provider.Capability{provider.PublicServiceIngress}}
}
func (p *ingressTestProvider) Install(context.Context, bool) error { return nil }
func (p *ingressTestProvider) Enable(context.Context, bool) error  { return nil }
func (p *ingressTestProvider) Status(context.Context) (provider.Status, error) {
	return p.status, nil
}
func (p *ingressTestProvider) Disable(context.Context) error { return nil }
func (p *ingressTestProvider) Remove(context.Context) error  { return nil }
func (p *ingressTestProvider) ConnectService(_ context.Context, req provider.ServiceAccessRequest) (provider.ServiceRoute, error) {
	p.got = req
	return provider.ServiceRoute{Endpoint: "https://service.example.test", Instructions: []string{"Open this endpoint in your compatible client."}}, nil
}

func TestConnectWakesBoxAndReturnsDeclaredServiceRoute(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[services.opencode]\ncommand = \"opencode serve\"\nport = 4096\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket, st, _ := startServer(t, fakeRunner{services: []state.ServiceStatus{{Name: "opencode", State: "active", Port: 4096}}})
	box, _, err := st.CreateBox("alpha", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	resp, data := do(t, client(socket), http.MethodPost, "/v1/boxes/"+box.ID+"/connect", map[string]string{"service": "opencode", "access_mode": "ssh-tunnel"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connect status = %d, body %s", resp.StatusCode, data)
	}
	var got api.ConnectResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.BoxID != box.ID || got.Service != "opencode" || got.Endpoint != "http://127.0.0.1:4096" || got.Access.Mode != "ssh-tunnel" || got.Access.Provider != "ssh" {
		t.Fatalf("connect response = %+v", got)
	}
	if len(got.Access.Instructions) == 0 || got.Access.Instructions[0] == "" {
		t.Fatalf("missing client instructions: %+v", got.Access)
	}
	current, err := st.Box(box.ID)
	if err != nil || current.State != state.StateRunning {
		t.Fatalf("box after connect = %+v, err %v; want running", current, err)
	}
}

func TestConnectUsesApprovedIngressProviderForSelectedService(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[services.web]\ncommand = \"serve\"\nport = 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket, st, srv := startServer(t, fakeRunner{services: []state.ServiceStatus{{Name: "web", State: "active", Port: 8080}}})
	box, _, err := st.CreateBox("alpha", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	p := &ingressTestProvider{status: provider.Status{Installed: true, Enabled: true}}
	registry, err := provider.NewRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetProviders(registry)
	resp, data := do(t, client(socket), http.MethodPost, "/v1/boxes/"+box.ID+"/connect", map[string]string{"service": "web", "access_mode": "provider", "provider": "fake-ingress"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connect status = %d, body %s", resp.StatusCode, data)
	}
	var got api.ConnectResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != "https://service.example.test" || got.Access.Provider != "fake-ingress" || p.got.BoxID != box.ID || p.got.Service != "web" || p.got.Port != 8080 {
		t.Fatalf("response = %+v, provider request = %+v", got, p.got)
	}
}

func TestConnectDoesNotWakeForUnapprovedIngressProvider(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[services.web]\ncommand = \"serve\"\nport = 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket, st, srv := startServer(t, fakeRunner{})
	box, _, err := st.CreateBox("alpha", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := provider.NewRegistry(&ingressTestProvider{status: provider.Status{Installed: true}})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetProviders(registry)
	resp, _ := do(t, client(socket), http.MethodPost, "/v1/boxes/"+box.ID+"/connect", map[string]string{"service": "web", "access_mode": "provider", "provider": "fake-ingress"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("connect unapproved provider status = %d, want 409", resp.StatusCode)
	}
	current, err := st.Box(box.ID)
	if err != nil || current.State != state.StateCreated {
		t.Fatalf("box state = %s, err %v; want created", current.State, err)
	}
}

func TestConnectRejectsUndeclaredServiceBeforeWake(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[services.web]\ncommand = \"serve\"\nport = 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket, st, _ := startServer(t, fakeRunner{})
	box, _, err := st.CreateBox("alpha", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := do(t, client(socket), http.MethodPost, "/v1/boxes/"+box.ID+"/connect", map[string]string{"service": "missing", "access_mode": "ssh-tunnel"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("connect undeclared service status = %d, want 400", resp.StatusCode)
	}
	current, err := st.Box(box.ID)
	if err != nil || current.State != state.StateCreated {
		t.Fatalf("box state after rejected connect = %s, err %v; want created", current.State, err)
	}
}

func TestConnectReportsServiceStartupFailure(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[services.web]\ncommand = \"serve\"\nport = 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket, st, _ := startServer(t, fakeRunner{services: []state.ServiceStatus{{Name: "web", State: "failed", Port: 8080}}})
	box, _, err := st.CreateBox("alpha", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	resp, data := do(t, client(socket), http.MethodPost, "/v1/boxes/"+box.ID+"/connect", map[string]string{"service": "web"})
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(data), "pluto logs") {
		t.Fatalf("failed service response = %d %s", resp.StatusCode, data)
	}
}
