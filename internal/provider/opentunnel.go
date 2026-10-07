package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var routeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
var serviceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// ServiceRoute identifies one declared box service. Target is intentionally
// absent: the adapter constructs an explicit loopback destination from Port.
type ServiceRoute struct {
	ID      string `json:"id"`
	BoxID   string `json:"box_id"`
	Service string `json:"service"`
	Port    int    `json:"port"`
	Cleanup func() `json:"-"`
}

// Ingress exposes explicit selected-service route lifecycle in addition to
// the shared provider lifecycle.
type Ingress interface {
	Provider
	AddRoute(context.Context, ServiceRoute) error
	RemoveRoute(context.Context, string) error
}

// OpenTunnel manages explicit local service routes through the OpenTunnel CLI.
// A caller must resolve and approve a declared service before constructing a
// ServiceRoute; this adapter cannot accept arbitrary addresses or path routes.
type OpenTunnel struct {
	commands  CommandRunner
	mu        sync.Mutex
	routes    map[string]ServiceRoute
	enabled   bool
	statePath string
	stateErr  error
}

func NewOpenTunnel(commands CommandRunner) *OpenTunnel {
	return NewOpenTunnelWithState(commands, "")
}

// NewOpenTunnelWithState persists route IDs and targets so disable/removal can
// clean OpenTunnel configuration after a Pluto daemon restart. The file holds
// no OpenTunnel credentials or private key material.
func NewOpenTunnelWithState(commands CommandRunner, statePath string) *OpenTunnel {
	if commands == nil {
		commands = OSCommandRunner{}
	}
	p := &OpenTunnel{commands: commands, routes: make(map[string]ServiceRoute), statePath: statePath}
	if statePath != "" {
		data, err := os.ReadFile(statePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			p.stateErr = errors.New("could not read OpenTunnel route state")
		} else if err == nil {
			var routes []ServiceRoute
			if json.Unmarshal(data, &routes) != nil {
				p.stateErr = errors.New("OpenTunnel route state is invalid")
			} else {
				for _, route := range routes {
					if !validServiceRoute(route) {
						p.stateErr = errors.New("OpenTunnel route state is invalid")
						p.routes = make(map[string]ServiceRoute)
						break
					}
					p.routes[route.ID] = route
				}
			}
		}
	}
	return p
}

func (p *OpenTunnel) Info() Info {
	return Info{
		ID: "opentunnel", Name: "OpenTunnel",
		Capabilities: []Capability{PublicServiceIngress},
		Dependencies: []string{
			"OpenTunnel CLI and Bun runtime",
			"OpenTunnel tunnel identity created with `opentunnel create`",
			"Cloudflare account and DNS API credentials",
			"ZeroSSL ACME account credentials",
			"hosted OpenTunnel service",
			"AWS relay service while OpenTunnel's Cloudflare TCP worker support is pending",
			"selected in-box service must keep its own authentication enabled",
		},
	}
}

func (p *OpenTunnel) Install(ctx context.Context, approved bool) error {
	if !approved {
		return ErrApprovalRequired
	}
	if _, err := p.commands.LookPath("bun"); err != nil {
		return errors.New("Bun is required to install OpenTunnel")
	}
	if _, err := p.commands.Run(ctx, "bun", "install", "-g", "opentunnel"); err != nil {
		return safeProviderCommandError("OpenTunnel", "install client", err)
	}
	if _, err := p.commands.LookPath("opentunnel"); err != nil {
		return errors.New("OpenTunnel CLI was not found after installation")
	}
	if _, err := p.commands.Run(ctx, "opentunnel", "--version"); err != nil {
		return safeProviderCommandError("OpenTunnel", "check installed client", err)
	}
	return nil
}

func (p *OpenTunnel) Enable(ctx context.Context, approved bool) error {
	if !approved {
		return ErrApprovalRequired
	}
	if _, err := p.commands.LookPath("opentunnel"); err != nil {
		return ErrNotInstalled
	}
	p.mu.Lock()
	if !p.enabled {
		if err := p.cleanRoutesLocked(ctx); err != nil {
			p.closeForwardsLocked()
			p.mu.Unlock()
			return err
		}
	}
	p.mu.Unlock()
	if _, err := p.commands.Run(ctx, "opentunnel", "service", "start"); err != nil {
		return safeProviderCommandError("OpenTunnel", "enable public ingress", err)
	}
	p.mu.Lock()
	p.enabled = true
	p.mu.Unlock()
	return nil
}

func (p *OpenTunnel) Status(ctx context.Context) (Status, error) {
	if _, err := p.commands.LookPath("opentunnel"); err != nil {
		return Status{}, nil
	}
	output, err := p.commands.Run(ctx, "opentunnel", "service", "status")
	if err != nil {
		return Status{Installed: true, Detail: "OpenTunnel service is unavailable"}, nil
	}
	enabled := p.serviceRunning(output)
	detail := "public ingress is disabled"
	if enabled {
		detail = "OpenTunnel service is running; selected services remain responsible for authentication"
	} else if strings.TrimSpace(string(output)) != "stopped" {
		detail = "OpenTunnel returned an unrecognized service status"
	}
	return Status{Installed: true, Enabled: enabled, Detail: detail}, nil
}

func (p *OpenTunnel) AddRoute(ctx context.Context, route ServiceRoute) error {
	if !validServiceRoute(route) {
		return errors.New("invalid selected service route")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return ErrIngressDisabled
	}
	if _, exists := p.routes[route.ID]; exists {
		return fmt.Errorf("OpenTunnel route %q already exists", route.ID)
	}
	if _, err := p.commands.Run(ctx, "opentunnel", "route", "add", route.ID, "127.0.0.1:"+strconv.Itoa(route.Port)); err != nil {
		return safeProviderCommandError("OpenTunnel", "add selected service route", err)
	}
	p.routes[route.ID] = route
	if err := p.saveRoutesLocked(); err != nil {
		_, _ = p.commands.Run(ctx, "opentunnel", "route", "remove", route.ID)
		delete(p.routes, route.ID)
		if route.Cleanup != nil {
			route.Cleanup()
		}
		return errors.New("OpenTunnel selected service route could not be recorded")
	}
	return nil
}

func (p *OpenTunnel) RemoveRoute(ctx context.Context, id string) error {
	if !routeIDPattern.MatchString(id) {
		return errors.New("invalid selected service route id")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.routes[id]; !exists {
		return nil
	}
	if _, err := p.commands.Run(ctx, "opentunnel", "route", "remove", id); err != nil {
		route := p.routes[id]
		if route.Cleanup != nil {
			route.Cleanup()
			route.Cleanup = nil
			p.routes[id] = route
		}
		return safeProviderCommandError("OpenTunnel", "remove selected service route", err)
	}
	if cleanup := p.routes[id].Cleanup; cleanup != nil {
		cleanup()
	}
	delete(p.routes, id)
	if err := p.saveRoutesLocked(); err != nil {
		return errors.New("OpenTunnel route state could not be updated")
	}
	return nil
}

func (p *OpenTunnel) Disable(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stateErr != nil {
		p.closeForwardsLocked()
		return p.stateErr
	}
	if _, err := p.commands.LookPath("opentunnel"); err != nil {
		if len(p.routes) == 0 {
			p.enabled = false
			return nil
		}
		p.closeForwardsLocked()
		p.enabled = false
		return errors.New("OpenTunnel client is unavailable; selected route cleanup could not be completed")
	}
	if err := p.cleanRoutesLocked(ctx); err != nil {
		p.closeForwardsLocked()
		_, _ = p.commands.Run(ctx, "opentunnel", "service", "stop")
		p.enabled = false
		return err
	}
	if _, err := p.commands.Run(ctx, "opentunnel", "service", "stop"); err != nil {
		return safeProviderCommandError("OpenTunnel", "disable public ingress", err)
	}
	p.enabled = false
	return nil
}

func (p *OpenTunnel) Remove(ctx context.Context) error {
	if err := p.Disable(ctx); err != nil {
		return err
	}
	if _, err := p.commands.LookPath("opentunnel"); err != nil {
		return nil
	}
	if _, err := p.commands.Run(ctx, "opentunnel", "tunnel", "remove"); err != nil {
		return safeProviderCommandError("OpenTunnel", "remove tunnel identity", err)
	}
	return nil
}

func (p *OpenTunnel) cleanRoutesLocked(ctx context.Context) error {
	if p.stateErr != nil {
		return p.stateErr
	}
	for id := range p.routes {
		if _, err := p.commands.Run(ctx, "opentunnel", "route", "remove", id); err != nil {
			return safeProviderCommandError("OpenTunnel", "remove selected service route", err)
		}
		if cleanup := p.routes[id].Cleanup; cleanup != nil {
			cleanup()
		}
		delete(p.routes, id)
	}
	return p.saveRoutesLocked()
}

func (p *OpenTunnel) closeForwardsLocked() {
	for id, route := range p.routes {
		if route.Cleanup != nil {
			route.Cleanup()
			route.Cleanup = nil
			p.routes[id] = route
		}
	}
}

func (p *OpenTunnel) saveRoutesLocked() error {
	if p.statePath == "" {
		return nil
	}
	routes := make([]ServiceRoute, 0, len(p.routes))
	for _, route := range p.routes {
		route.Cleanup = nil
		routes = append(routes, route)
	}
	data, err := json.Marshal(routes)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.statePath), 0o700); err != nil {
		return err
	}
	tmp := p.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p.statePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func validServiceRoute(route ServiceRoute) bool {
	return routeIDPattern.MatchString(route.ID) && serviceNamePattern.MatchString(route.Service) && route.Port > 0 && route.Port <= 65535
}

func safeProviderCommandError(name, stage string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s %s failed (exit code %d)", name, stage, exitErr.ExitCode())
	}
	return fmt.Errorf("%s %s failed", name, stage)
}

// ParseStatus is intentionally narrow and safe. It accepts only a known
// status word; unrecognized provider output is never surfaced to callers.
func (p *OpenTunnel) serviceRunning(output []byte) bool {
	return strings.TrimSpace(string(output)) == "running"
}
