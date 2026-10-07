package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// runLifecycleContract is shared by the fake provider and first-party adapters
// so lifecycle behavior stays consistent as capabilities are added.
func runLifecycleContract(t *testing.T, newProvider func() Provider) {
	t.Helper()
	p := newProvider()
	ctx := context.Background()
	info := p.Info()
	if info.ID == "" || len(info.Capabilities) == 0 || len(info.Dependencies) == 0 {
		t.Fatalf("provider info must disclose capabilities and dependencies: %#v", info)
	}
	if err := p.Install(ctx, false); err == nil {
		t.Fatal("install without host-owner approval succeeded")
	}
	if err := p.Install(ctx, true); err != nil {
		t.Fatalf("approved install: %v", err)
	}
	if err := p.Enable(ctx, false); err == nil {
		t.Fatal("enable without host-owner approval succeeded")
	}
	if err := p.Enable(ctx, true); err != nil {
		t.Fatalf("approved enable: %v", err)
	}
	status, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Installed || !status.Enabled {
		t.Fatalf("status after enable = %#v, want installed and enabled", status)
	}
	if err := p.Disable(ctx); err != nil {
		t.Fatalf("disable: %v", err)
	}
	status, err = p.Status(ctx)
	if err != nil || !status.Installed || status.Enabled {
		t.Fatalf("status after disable = %#v, %v; want installed and disabled", status, err)
	}
	if err := p.Remove(ctx); err != nil {
		t.Fatalf("remove: %v", err)
	}
	status, err = p.Status(ctx)
	if err != nil {
		t.Fatalf("status after remove: %v", err)
	}
	if status.Installed || status.Enabled {
		t.Fatalf("status after remove = %#v, want removed", status)
	}
}

type fakeProvider struct {
	installed, enabled bool
	routes             map[string]ServiceRoute
}

func (p *fakeProvider) Info() Info {
	return Info{ID: "fake", Name: "Fake", Capabilities: []Capability{PrivateHostConnectivity, PublicServiceIngress, EventSource, ClientAdapter}, Dependencies: []string{"fake dependency"}}
}
func (p *fakeProvider) Install(_ context.Context, approved bool) error {
	if !approved {
		return ErrApprovalRequired
	}
	p.installed = true
	return nil
}
func (p *fakeProvider) Enable(_ context.Context, approved bool) error {
	if !approved {
		return ErrApprovalRequired
	}
	if !p.installed {
		return ErrNotInstalled
	}
	p.enabled = true
	return nil
}
func (p *fakeProvider) Status(context.Context) (Status, error) {
	return Status{Installed: p.installed, Enabled: p.enabled}, nil
}
func (p *fakeProvider) AddRoute(_ context.Context, route ServiceRoute) error {
	if !p.enabled {
		return ErrIngressDisabled
	}
	if p.routes == nil {
		p.routes = make(map[string]ServiceRoute)
	}
	p.routes[route.ID] = route
	return nil
}
func (p *fakeProvider) RemoveRoute(_ context.Context, id string) error {
	delete(p.routes, id)
	return nil
}
func (p *fakeProvider) Disable(context.Context) error {
	p.enabled = false
	for _, route := range p.routes {
		if route.Cleanup != nil {
			route.Cleanup()
		}
	}
	p.routes = nil
	return nil
}
func (p *fakeProvider) Remove(ctx context.Context) error {
	_ = p.Disable(ctx)
	p.installed = false
	return nil
}

func TestFakeProviderLifecycleContract(t *testing.T) {
	runLifecycleContract(t, func() Provider { return &fakeProvider{} })
}

func TestFakeProviderIngressContract(t *testing.T) {
	runIngressLifecycleContract(t, func() ingressProvider { return &fakeProvider{} })
}

type tailscaleCommands struct {
	path   string
	status string
	err    error
	calls  [][]string
}

func (c *tailscaleCommands) LookPath(name string) (string, error) {
	if name != "tailscale" || c.path == "" {
		return "", errors.New("missing")
	}
	return c.path, nil
}
func (c *tailscaleCommands) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c.calls = append(c.calls, append([]string{name}, args...))
	if c.err != nil {
		return []byte("secret-token"), c.err
	}
	if len(args) == 2 && args[0] == "status" {
		return []byte(c.status), nil
	}
	return nil, nil
}

func TestTailscaleUsesPrivateHostClientWithoutPublicServing(t *testing.T) {
	runner := &tailscaleCommands{path: "/usr/bin/tailscale", status: `{"BackendState":"Running"}`}
	p := NewTailscale(runner)
	if err := p.Install(context.Background(), false); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("install without approval = %v", err)
	}
	if err := p.Install(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := p.Enable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	status, err := p.Status(context.Background())
	if err != nil || !status.Installed || !status.Enabled {
		t.Fatalf("status = %#v, %v", status, err)
	}
	if status.Detail != "connected to the private tailnet" {
		t.Fatalf("detail = %q", status.Detail)
	}
	wantEnable := []string{"tailscale", "up", "--ssh=false", "--accept-routes=false", "--accept-dns=false"}
	if !reflect.DeepEqual(runner.calls[1], wantEnable) {
		t.Fatalf("enable args = %#v", runner.calls[1])
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "serve") || strings.Contains(strings.Join(call, " "), "funnel") {
			t.Fatalf("unexpected public serving command: %#v", call)
		}
	}
}

func TestTailscaleErrorsDoNotLeakCommandOutput(t *testing.T) {
	runner := &tailscaleCommands{path: "/usr/bin/tailscale", err: errors.New("secret-token: failed")}
	p := NewTailscale(runner)
	err := p.Enable(context.Background(), true)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestTailscaleStatusRemainsSafeWhenServiceIsUnavailable(t *testing.T) {
	runner := &tailscaleCommands{path: "/usr/bin/tailscale", err: errors.New("secret-token: daemon unavailable")}
	status, err := NewTailscale(runner).Status(context.Background())
	if err != nil || !status.Installed || status.Enabled || status.Detail != "Tailscale service is unavailable" {
		t.Fatalf("status = %#v, %v", status, err)
	}
}

func TestTailscaleRemoveSignsHostOutWithoutUninstallingHostClient(t *testing.T) {
	runner := &tailscaleCommands{path: "/usr/bin/tailscale", status: `{"BackendState":"NeedsLogin"}`}
	p := NewTailscale(runner)
	if err := p.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.calls, [][]string{{"tailscale", "logout"}}) {
		t.Fatalf("remove calls = %#v", runner.calls)
	}
	status, err := p.Status(context.Background())
	if err != nil || !status.Installed || status.Enabled {
		t.Fatalf("status = %#v, %v", status, err)
	}
}
