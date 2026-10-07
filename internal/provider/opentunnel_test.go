package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type openTunnelCommands struct {
	path   string
	status string
	err    error
	calls  [][]string
}

func (c *openTunnelCommands) LookPath(name string) (string, error) {
	if c.path != "" && (name == "opentunnel" || name == "bun") {
		return c.path, nil
	}
	return "", errors.New("missing")
}
func (c *openTunnelCommands) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c.calls = append(c.calls, append([]string{name}, args...))
	if c.err != nil {
		return []byte("secret-token private-key.pem"), c.err
	}
	if len(args) == 2 && args[0] == "service" && args[1] == "start" {
		c.status = "running"
	}
	if len(args) == 2 && args[0] == "service" && args[1] == "stop" {
		c.status = "stopped"
	}
	if len(args) == 2 && args[0] == "service" && args[1] == "status" {
		return []byte(c.status), nil
	}
	return nil, nil
}

type ingressProvider interface {
	Provider
	AddRoute(context.Context, ServiceRoute) error
	RemoveRoute(context.Context, string) error
}

func runIngressLifecycleContract(t *testing.T, newProvider func() ingressProvider) {
	t.Helper()
	p := newProvider()
	ctx := context.Background()
	if !contains(p.Info().Capabilities, PublicServiceIngress) {
		t.Fatalf("capabilities = %#v, want public service ingress", p.Info().Capabilities)
	}
	if err := p.Install(ctx, false); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("install without approval = %v", err)
	}
	if err := p.Install(ctx, true); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := p.Enable(ctx, false); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("enable without approval = %v", err)
	}
	if err := p.Enable(ctx, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	status, err := p.Status(ctx)
	if err != nil || !status.Installed || !status.Enabled {
		t.Fatalf("status after enable = %#v, %v", status, err)
	}
	closed := false
	route := ServiceRoute{ID: "box-web", BoxID: "box", Service: "web", Port: 3000, Cleanup: func() { closed = true }}
	if err := p.AddRoute(ctx, route); err != nil {
		t.Fatalf("add selected route: %v", err)
	}
	if err := p.Disable(ctx); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !closed {
		t.Fatal("disable did not close the selected service forward")
	}
	status, err = p.Status(ctx)
	if err != nil || !status.Installed || status.Enabled {
		t.Fatalf("status after disable = %#v, %v", status, err)
	}
	if err := p.RemoveRoute(ctx, route.ID); err != nil {
		t.Fatalf("remove route after disable: %v", err)
	}
	if err := p.Remove(ctx); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func contains(values []Capability, want Capability) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestOpenTunnelIngressLifecycleContract(t *testing.T) {
	runIngressLifecycleContract(t, func() ingressProvider {
		return NewOpenTunnel(&openTunnelCommands{path: "/usr/bin/opentunnel"})
	})
}

func TestOpenTunnelRoutesOnlyAnExplicitLoopbackServicePort(t *testing.T) {
	runner := &openTunnelCommands{path: "/usr/bin/opentunnel"}
	p := NewOpenTunnel(runner)
	if err := p.Enable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	runner.calls = nil
	if err := p.AddRoute(context.Background(), ServiceRoute{ID: "box-web", Service: "web", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	want := []string{"opentunnel", "route", "add", "box-web", "127.0.0.1:3000"}
	if !reflect.DeepEqual(runner.calls, [][]string{want}) {
		t.Fatalf("commands = %#v, want explicit loopback route %#v", runner.calls, want)
	}
	for _, invalid := range []ServiceRoute{
		{ID: "*", Service: "web", Port: 3000},
		{ID: "box-web", Service: "web", Port: 0},
		{ID: "box-web", Service: "web", Port: 65536},
	} {
		if err := p.AddRoute(context.Background(), invalid); err == nil {
			t.Fatalf("AddRoute(%#v) succeeded", invalid)
		}
	}
	if got := len(runner.calls); got != 1 {
		t.Fatalf("invalid routes invoked provider: got %d commands", got)
	}
}

func TestOpenTunnelDisableAndRemoveCleanRoutes(t *testing.T) {
	runner := &openTunnelCommands{path: "/usr/bin/opentunnel"}
	p := NewOpenTunnel(runner)
	if err := p.Enable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := p.AddRoute(context.Background(), ServiceRoute{ID: "box-web", Service: "web", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	if err := p.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"opentunnel", "service", "start"},
		{"opentunnel", "route", "add", "box-web", "127.0.0.1:3000"},
		{"opentunnel", "route", "remove", "box-web"},
		{"opentunnel", "service", "stop"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %#v, want %#v", runner.calls, want)
	}
}

func TestOpenTunnelStatusUsesSafeSummary(t *testing.T) {
	runner := &openTunnelCommands{path: "/usr/bin/opentunnel", status: "secret-token is running"}
	status, err := NewOpenTunnel(runner).Status(context.Background())
	if err != nil || !status.Installed || status.Enabled || strings.Contains(status.Detail, "secret-token") {
		t.Fatalf("status = %#v, %v", status, err)
	}
	runner.status = "running"
	status, err = NewOpenTunnel(runner).Status(context.Background())
	if err != nil || !status.Enabled || status.Detail != "OpenTunnel service is running; selected services remain responsible for authentication" {
		t.Fatalf("safe running status = %#v, %v", status, err)
	}
}

func TestOpenTunnelRouteRemovalClosesItsLoopbackForward(t *testing.T) {
	runner := &openTunnelCommands{path: "/usr/bin/opentunnel"}
	p := NewOpenTunnel(runner)
	if err := p.Enable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	closed := false
	if err := p.AddRoute(context.Background(), ServiceRoute{ID: "box-web", Service: "web", Port: 30123, Cleanup: func() { closed = true }}); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveRoute(context.Background(), "box-web"); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("route removal left the selected service forward running")
	}
}

func TestOpenTunnelDisableCleansPersistedRoutesAfterRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "opentunnel-routes.json")
	firstCommands := &openTunnelCommands{path: "/usr/bin/opentunnel"}
	first := NewOpenTunnelWithState(firstCommands, statePath)
	if err := first.Enable(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := first.AddRoute(context.Background(), ServiceRoute{ID: "box-web", BoxID: "box", Service: "web", Port: 31234}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("route state permissions = %o, want 600", info.Mode().Perm())
	}
	secondCommands := &openTunnelCommands{path: "/usr/bin/opentunnel"}
	second := NewOpenTunnelWithState(secondCommands, statePath)
	if err := second.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"opentunnel", "route", "remove", "box-web"},
		{"opentunnel", "service", "stop"},
	}
	if !reflect.DeepEqual(secondCommands.calls, want) {
		t.Fatalf("restart cleanup commands = %#v, want %#v", secondCommands.calls, want)
	}
}

func TestOpenTunnelFailuresDoNotLeakProviderOutput(t *testing.T) {
	runner := &openTunnelCommands{path: "/usr/bin/opentunnel", err: errors.New("secret-token private-key.pem")}
	p := NewOpenTunnel(runner)
	for name, call := range map[string]func() error{
		"install": func() error { return p.Install(context.Background(), true) },
		"enable":  func() error { return p.Enable(context.Background(), true) },
		"add route": func() error {
			return p.AddRoute(context.Background(), ServiceRoute{ID: "box-web", Service: "web", Port: 3000})
		},
		"disable": func() error { return p.Disable(context.Background()) },
		"remove":  func() error { return p.Remove(context.Background()) },
	} {
		err := call()
		if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "private-key") {
			t.Errorf("%s error = %v; want redacted failure", name, err)
		}
	}
}
