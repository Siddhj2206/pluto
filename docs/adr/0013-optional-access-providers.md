# Optional access providers and capability boundaries

M5 extends the SSH-first access model with optional providers, because existing clients need both private host connectivity and a way to expose a selected in-box service. Keep private host/control connectivity, public service ingress, event sources, and client adapters as distinct provider capabilities under one host-owner-controlled lifecycle. Enabling a provider is separate from trusting a project contract; each provider must disclose external accounts, relays, and other dependencies.

Tailscale may provide private host connectivity using the owner's tailnet identity and ACL policy; Pluto does not manage that policy. OpenTunnel may provide public ingress only to an explicitly selected in-box service. Its tunnel is transport, not client authentication: the service keeps its own authentication enabled. Neither provider may expose Pluto's task/control API. Providers remain optional, and the existing SSH path continues to work without them.

## Considered options

- Keep SSH as the only Pluto-managed access path: rejected for M5 because it cannot provide the specified provider lifecycle or selected-service public ingress.
- Treat private connectivity and public ingress as one generic tunnel capability: rejected because their audience, authentication, exposure, and failure boundaries differ.
- Embed every provider into the daemon: deferred. Prefer managing installed host tools behind an interface where feasible; do not silently accept provider telemetry or credential storage on Pluto's behalf.

## Consequences

Host owners opt in to provider installation and enablement and remain responsible for provider accounts and policies. Tailscale's hosted coordination, identity provider, and possible relays are external dependencies; its logging/telemetry behavior must be controlled and disclosed. OpenTunnel currently depends on Cloudflare, ZeroSSL, Bun, and a hosted tunnel service, with an additional relay dependency during its beta. OpenTunnel supplies no client authentication, so public exposure is safe only when the chosen in-box service has its own authentication enabled. Provider status and removal must not print or expose credentials.

This supersedes the SSH-only boundary in [ADR 0006](0006-access-is-ssh.md) for optional M5 capabilities. SSH remains the baseline transport and fallback.
