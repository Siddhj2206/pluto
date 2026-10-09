package daemon_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/provider"
	"github.com/Siddhj2206/pluto/internal/state"
)

type routeProvider struct {
	routes  []provider.IngressRoute
	removed []string
}

func (*routeProvider) Info() provider.Info {
	return provider.Info{ID: "opentunnel", Name: "OpenTunnel", Capabilities: []provider.Capability{provider.PublicServiceIngress}, Dependencies: []string{"Cloudflare", "ZeroSSL", "Bun", "AWS relay"}}
}
func (*routeProvider) Install(context.Context, bool) error { return nil }
func (*routeProvider) Enable(context.Context, bool) error  { return nil }
func (*routeProvider) Status(context.Context) (provider.Status, error) {
	return provider.Status{Installed: true, Enabled: true}, nil
}
func (*routeProvider) Disable(context.Context) error { return nil }
func (*routeProvider) Remove(context.Context) error  { return nil }
func (p *routeProvider) AddRoute(_ context.Context, route provider.IngressRoute) error {
	p.routes = append(p.routes, route)
	return nil
}
func (p *routeProvider) RemoveRoute(_ context.Context, id string) error {
	p.removed = append(p.removed, id)
	return nil
}

func TestPublicIngressRoutesOnlyTheSelectedDeclaredService(t *testing.T) {
	var forwardedPort int
	socket, st, srv := startServer(t, fakeRunner{forward: func(_ *state.Box, port int) (int, func(), error) {
		forwardedPort = port
		return 32000, func() {}, nil
	}})
	p := &routeProvider{}
	registry, err := provider.NewRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetProviders(registry)
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[services.web]\ncommand = 'serve'\nport = 3000\n[services.admin]\ncommand = 'serve-admin'\nport = 4000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	box, _, err := st.CreateBox("test", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}

	resp, data := do(t, client(socket), http.MethodPost, "/v1/providers/opentunnel/routes", api.ProviderRouteRequest{BoxID: box.ID, Service: "web", Approved: true, ServiceAuthConfirmed: true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("route status = %d, body=%s", resp.StatusCode, data)
	}
	var out api.ProviderRouteResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(p.routes) != 1 || p.routes[0].Service != "web" || p.routes[0].Port != 32000 || forwardedPort != 3000 || p.routes[0].BoxID != box.ID || out.Route.ID != p.routes[0].ID {
		t.Fatalf("selected route = %#v; response = %#v", p.routes, out)
	}
	if p.routes[0].Port == 4000 || p.routes[0].Port == 22 {
		t.Fatalf("route can reach an unselected service or SSH: %#v", p.routes[0])
	}
	resp, _ = do(t, client(socket), http.MethodDelete, "/v1/providers/opentunnel/routes/"+p.routes[0].ID, nil)
	if resp.StatusCode != http.StatusNoContent || len(p.removed) != 1 || p.removed[0] != p.routes[0].ID {
		t.Fatalf("route cleanup status=%d removed=%v", resp.StatusCode, p.removed)
	}
}

func TestPublicIngressRejectsUnselectedAndUnapprovedTargets(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{})
	p := &routeProvider{}
	registry, _ := provider.NewRegistry(p)
	srv.SetProviders(registry)
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[services.web]\ncommand = 'serve'\nport = 3000\n[services.ssh]\ncommand = 'sshd'\nport = 22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	box, _, err := st.CreateBox("test", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []api.ProviderRouteRequest{
		{BoxID: box.ID, Service: "missing", Approved: true, ServiceAuthConfirmed: true},
		{BoxID: box.ID, Service: "ssh", Approved: true, ServiceAuthConfirmed: true},
		{BoxID: box.ID, Service: "web", ServiceAuthConfirmed: true},
		{BoxID: box.ID, Service: "web", Approved: true},
	} {
		resp, _ := do(t, client(socket), http.MethodPost, "/v1/providers/opentunnel/routes", req)
		if resp.StatusCode < 400 {
			t.Errorf("request %#v succeeded with %d", req, resp.StatusCode)
		}
	}
	if len(p.routes) != 0 {
		t.Fatalf("rejected requests created routes: %#v", p.routes)
	}
}

// namedRouteProvider carries the shared route lifecycle under a chosen id, so
// a test can prove the daemon routes to the provider the caller selected
// rather than to a hardcoded one (#112).
type namedRouteProvider struct {
	routeProvider
	id string
}

func (p *namedRouteProvider) Info() provider.Info {
	return provider.Info{ID: p.id, Name: p.id, Capabilities: []provider.Capability{provider.PublicServiceIngress}}
}

func TestPublicIngressRouteUsesTheSelectedProvider(t *testing.T) {
	socket, st, srv := startServer(t, fakeRunner{forward: func(_ *state.Box, port int) (int, func(), error) {
		return 32000, func() {}, nil
	}})
	first := &namedRouteProvider{id: "opentunnel"}
	second := &namedRouteProvider{id: "edge"}
	registry, err := provider.NewRegistry(first, second)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetProviders(registry)
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[services.web]\ncommand = 'serve'\nport = 3000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	box, _, err := st.CreateBox("test", "main", worktree)
	if err != nil {
		t.Fatal(err)
	}
	resp, data := do(t, client(socket), http.MethodPost, "/v1/providers/edge/routes", api.ProviderRouteRequest{BoxID: box.ID, Service: "web", Approved: true, ServiceAuthConfirmed: true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("selected provider route status=%d body=%s", resp.StatusCode, data)
	}
	if len(second.routes) != 1 || len(first.routes) != 0 {
		t.Fatalf("routes landed on the wrong provider: first=%v second=%v", first.routes, second.routes)
	}
}
