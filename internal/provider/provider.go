// Package provider defines Pluto's host-owner-controlled provider lifecycle.
// Capabilities describe what a provider can do; they do not imply that Pluto
// exposes its control API over that provider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

type Capability string

const (
	PrivateHostConnectivity Capability = "private_host_connectivity"
	PublicServiceIngress    Capability = "public_service_ingress"
	EventSource             Capability = "event_source"
	ClientAdapter           Capability = "client_adapter"
)

// Info is the disclosure a host owner sees before approving a provider.
type Info struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Capabilities []Capability `json:"capabilities"`
	Dependencies []string     `json:"dependencies"`
}

// Status contains safe lifecycle facts only. Implementations must never put
// credentials, tokens, private keys, or raw provider output in Detail.
type Status struct {
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Detail    string `json:"detail,omitempty"`
}

var (
	ErrApprovalRequired = errors.New("explicit host-owner approval is required")
	ErrNotInstalled     = errors.New("provider is not installed")
	ErrUnknown          = errors.New("unknown provider")
)

// Provider is the stable internal seam for providers managed by the host.
// Install and Enable must reject operations that do not carry explicit owner
// approval. Disable and Remove are local lifecycle actions.
type Provider interface {
	Info() Info
	Install(context.Context, bool) error
	Enable(context.Context, bool) error
	Status(context.Context) (Status, error)
	Disable(context.Context) error
	Remove(context.Context) error
}

// ServiceAccessRequest identifies one declared service for an ingress-capable
// provider. Implementations receive no task/control endpoint and must route
// only to this selected service.
type ServiceAccessRequest struct {
	BoxID   string
	Service string
	Port    int
}

// ServiceRoute is the client-facing route returned by an approved service
// ingress provider. The service remains responsible for client auth/pairing.
type ServiceRoute struct {
	Endpoint     string
	Instructions []string
}

// ServiceIngress is an optional extension implemented only by providers that
// can expose a selected in-box service. It deliberately does not expose Pluto
// control or task APIs.
type ServiceIngress interface {
	ConnectService(context.Context, ServiceAccessRequest) (ServiceRoute, error)
}

// Registry exposes the known optional providers without conflating their
// capabilities. Provider metadata is sorted for stable CLI and API output.
type Registry struct{ providers map[string]Provider }

func NewRegistry(providers ...Provider) (*Registry, error) {
	r := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		if p == nil || p.Info().ID == "" {
			return nil, errors.New("provider must have an id")
		}
		id := p.Info().ID
		if _, exists := r.providers[id]; exists {
			return nil, fmt.Errorf("duplicate provider %q", id)
		}
		r.providers[id] = p
	}
	return r, nil
}

func (r *Registry) List() []Provider {
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	providers := make([]Provider, 0, len(ids))
	for _, id := range ids {
		providers = append(providers, r.providers[id])
	}
	return providers
}

func (r *Registry) Get(id string) (Provider, error) {
	p, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknown, id)
	}
	return p, nil
}
