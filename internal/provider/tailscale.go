package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// CommandRunner is the host-tool boundary used by the Tailscale adapter.
type CommandRunner interface {
	LookPath(string) (string, error)
	Run(context.Context, string, ...string) ([]byte, error)
}

type OSCommandRunner struct{}

func (OSCommandRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }
func (OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Tailscale manages private host reachability through the installed host
// client. It never starts Tailscale Serve/Funnel and never binds Pluto's API.
type Tailscale struct{ commands CommandRunner }

func NewTailscale(commands CommandRunner) *Tailscale {
	if commands == nil {
		commands = OSCommandRunner{}
	}
	return &Tailscale{commands: commands}
}

func (p *Tailscale) Info() Info {
	return Info{
		ID: "tailscale", Name: "Tailscale",
		Capabilities: []Capability{PrivateHostConnectivity},
		Dependencies: []string{
			"Tailscale host client",
			"Tailscale account and hosted coordination service",
			"host owner sign-in on this device",
			"tailnet identity and owner-managed ACL policy",
			"Tailscale DERP relays when direct connectivity is unavailable",
		},
	}
}

func (p *Tailscale) Install(ctx context.Context, approved bool) error {
	if !approved {
		return ErrApprovalRequired
	}
	if _, err := p.commands.LookPath("tailscale"); err != nil {
		return errors.New("Tailscale host client is not installed; install it with your operating system's package manager")
	}
	if _, err := p.commands.Run(ctx, "tailscale", "version"); err != nil {
		return safeCommandError("check installed client", err)
	}
	return nil
}

func (p *Tailscale) Enable(ctx context.Context, approved bool) error {
	if !approved {
		return ErrApprovalRequired
	}
	if _, err := p.commands.LookPath("tailscale"); err != nil {
		return ErrNotInstalled
	}
	// Pluto uses the owner's private tailnet only. Tailscale SSH, subnet route
	// acceptance, and DNS changes are outside Pluto's provider contract.
	if _, err := p.commands.Run(ctx, "tailscale", "up", "--ssh=false", "--accept-routes=false", "--accept-dns=false"); err != nil {
		return safeCommandError("enable private connectivity", err)
	}
	return nil
}

func (p *Tailscale) Status(ctx context.Context) (Status, error) {
	if _, err := p.commands.LookPath("tailscale"); err != nil {
		return Status{}, nil
	}
	data, err := p.commands.Run(ctx, "tailscale", "status", "--json")
	if err != nil {
		return Status{Installed: true, Detail: "Tailscale service is unavailable"}, nil
	}
	var state struct {
		BackendState string `json:"BackendState"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return Status{}, errors.New("Tailscale returned an invalid status response")
	}
	enabled := strings.EqualFold(state.BackendState, "Running")
	detail := "not connected to the tailnet"
	if enabled {
		detail = "connected to the private tailnet"
	}
	return Status{Installed: true, Enabled: enabled, Detail: detail}, nil
}

func (p *Tailscale) Disable(ctx context.Context) error {
	if _, err := p.commands.LookPath("tailscale"); err != nil {
		return nil
	}
	if _, err := p.commands.Run(ctx, "tailscale", "down"); err != nil {
		return safeCommandError("disable private connectivity", err)
	}
	return nil
}

func (p *Tailscale) Remove(ctx context.Context) error {
	// Remove this host's tailnet identity while leaving the owner-managed host
	// client package in place for other uses.
	if _, err := p.commands.LookPath("tailscale"); err != nil {
		return nil
	}
	if _, err := p.commands.Run(ctx, "tailscale", "logout"); err != nil {
		return safeCommandError("remove host from tailnet", err)
	}
	return nil
}

func safeCommandError(stage string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("Tailscale %s failed (exit code %d)", stage, exitErr.ExitCode())
	}
	return fmt.Errorf("Tailscale %s failed", stage)
}
