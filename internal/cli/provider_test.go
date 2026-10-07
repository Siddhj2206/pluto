package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/cli"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/provider"
	"github.com/Siddhj2206/pluto/internal/state"
)

type cliFakeProvider struct {
	installed, enabled bool
	installs, enables  int
}

func (p *cliFakeProvider) Info() provider.Info {
	return provider.Info{ID: "fake", Name: "Fake", Capabilities: []provider.Capability{provider.PrivateHostConnectivity, provider.EventSource}, Dependencies: []string{"owner account", "hosted relay"}}
}
func (p *cliFakeProvider) Install(_ context.Context, approved bool) error {
	p.installs++
	if !approved {
		return provider.ErrApprovalRequired
	}
	p.installed = true
	return nil
}
func (p *cliFakeProvider) Enable(_ context.Context, approved bool) error {
	p.enables++
	if !approved {
		return provider.ErrApprovalRequired
	}
	if !p.installed {
		return provider.ErrNotInstalled
	}
	p.enabled = true
	return nil
}
func (p *cliFakeProvider) Status(context.Context) (provider.Status, error) {
	return provider.Status{Installed: p.installed, Enabled: p.enabled}, nil
}
func (p *cliFakeProvider) Disable(context.Context) error { p.enabled = false; return nil }
func (p *cliFakeProvider) Remove(context.Context) error {
	p.enabled = false
	p.installed = false
	return nil
}

func providerCLI(t *testing.T, p provider.Provider, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := daemon.New(st, fakeRunner{st: st}, "test")
	registry, err := provider.NewRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetProviders(registry)
	socket := filepath.Join(dir, "pluto.sock")
	if err := srv.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	var stdout, stderr bytes.Buffer
	args = append([]string{"--socket", socket}, args...)
	code := cli.Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestProviderInstallDisclosesBeforeRequiringApproval(t *testing.T) {
	p := &cliFakeProvider{}
	code, stdout, stderr := providerCLI(t, p, "provider", "install", "fake")
	if code != 1 || p.installs != 0 {
		t.Fatalf("result code=%d installs=%d", code, p.installs)
	}
	for _, want := range []string{"private_host_connectivity", "event_source", "owner account", "hosted relay"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("disclosure missing %q: %s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "--approve") {
		t.Fatalf("approval next step missing: %s", stderr)
	}
}

func TestProviderEnableRequiresExplicitApprovalAndUsesSharedLifecycle(t *testing.T) {
	p := &cliFakeProvider{}
	code, _, _ := providerCLI(t, p, "provider", "install", "fake", "--approve")
	if code != 0 || p.installs != 1 {
		t.Fatalf("install result code=%d installs=%d", code, p.installs)
	}
	code, _, stderr := providerCLI(t, p, "provider", "enable", "fake")
	if code != 1 || !strings.Contains(stderr, "approval") {
		t.Fatalf("unapproved enable: code=%d stderr=%s", code, stderr)
	}
	if p.enabled {
		t.Fatal("unapproved enable changed provider state")
	}
	code, _, _ = providerCLI(t, p, "provider", "enable", "fake", "--approve")
	if code != 0 || !p.enabled || p.enables != 1 {
		t.Fatalf("approved enable result code=%d enabled=%t calls=%d", code, p.enabled, p.enables)
	}
}

func TestProviderRouteRequiresExposureAndServiceAuthConfirmation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"provider", "route", "add", "box-id", "web"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "--approve and --confirm-auth") {
		t.Fatalf("route without approvals: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "next:") {
		t.Fatalf("route failure has no next step (ADR 0009): %q", stderr.String())
	}
}
