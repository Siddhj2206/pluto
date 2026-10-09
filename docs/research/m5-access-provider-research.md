# M5 access provider decision (#104)

Parent spec: [#101](https://github.com/Siddhj2206/pluto/issues/101). Research notes: `/tmp/pluto-m5-research-104.md` (2026-10-07). The decision is recorded in [ADR 0013](../adr/0013-optional-access-providers.md).

## Recommendation

Treat private connectivity and public service ingress as distinct optional provider capabilities. Use Tailscale for private host reachability and OpenTunnel only for an explicitly selected in-box service. Neither path exposes Pluto's task/control API. Keep SSH available as the baseline and fallback. Require separate host-owner approval to install and enable providers; do not conflate provider approval with project-contract trust.

## Tailscale: private connectivity

Tailscale's tailnet provides private layer-3 access with device identity and ACLs. This fits remote SSH and the private host control path; Tailscale Serve can provide a stable private HTTPS origin for a selected in-box web service. Pluto does not own the tailnet or edit ACL policy. The owner supplies a Tailscale account and remains responsible for tailnet identity, device approval, and access rules. Hosted coordination and DERP relays are dependencies. Embedded `tsnet` adds binary size and has default logtail behavior that must be disabled or explicitly disclosed; favor the installed host client unless implementation requirements justify embedding. Keep services behind localhost/private interfaces and do not trust identity headers from a directly reachable backend.

## OpenTunnel: public service ingress

OpenTunnel maps an explicit subdomain route to a local service and uses blind TLS ingress; it does not authenticate clients. The selected service must retain its own authentication. Do not create wildcard routes, shared routing to Pluto's daemon, or path-based assumptions. Owners need a Cloudflare account and credentials, ZeroSSL ACME credentials, Bun, and the hosted OpenTunnel service. The project is beta and currently documents an AWS relay dependency while Cloudflare's TCP worker support is pending. It has no path routing and its availability depends on the upstream service.

OpenTunnel keeps key material in its own XDG data directory; Pluto should invoke its lifecycle without reading, logging, copying, or returning its tokens or private keys. Removal must stop routes and clean up provider state. Failure output should name the failed lifecycle stage without including credentials.

## Boundaries and lifecycle

| Capability | Provider use | Audience | Pluto control API |
| --- | --- | --- | --- |
| Private host connectivity | Tailscale tailnet | Tailnet members under owner ACLs | Private only |
| Public in-box service ingress | OpenTunnel explicit route | Public internet; service authentication required | Never routed |

The shared provider lifecycle covers install, enable, status, disable, and removal, while capabilities stay explicit. Provider install/enable approval belongs to the host owner; project trust remains a separate, revision-specific decision. The exact operational behaviors and primary sources are in `/tmp/pluto-m5-research-104.md`.

## Material limits

Tailscale requires an external account and hosted control plane. OpenTunnel is early and depends on external Cloudflare/ZeroSSL services, Bun, and currently an AWS relay. These are disclosed opt-in dependencies, not Pluto accounts or mandatory services. Reassess OpenTunnel's upstream architecture before release; preserve the invariant that public routes reach only the selected service.
