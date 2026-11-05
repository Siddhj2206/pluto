# Tailcat is the zero-setup route, pending a real network test

**Status:** Accepted provisionally. The cross-network test is postponed until a second machine is available, and it must pass before M4 ships on this route.

A device should be able to reach a host behind NAT with no router changes, no tailnet and no Pluto account. We'll use Tailcat, embedded as a Go library, as one route alongside direct ones such as LAN, tailnet and public addresses. Same-machine tests were good: it embeds without cgo, a host keeps its address with a saved key and fixed relay region, an unknown device key can't connect, and resuming output by offset lost no lines across a host restart or 45-second freezes. Auth stays Pluto's own and doesn't depend on the route.

## Considered options

- **Tailnet as the default.** Reliable, but it needs an account and an install on every device.
- **A relay run by Pluto.** Zero setup, but it makes Pluto a service someone has to keep running.
- **A self-hosted relay as the only option.** Requires a public machine, which the home-server user may not have. It remains the fallback, because Tailcat supports custom relays.

## Consequences

- Pluto must keep Tailcat behind a route interface and pin its version. It offers no API, CLI or wire-format stability.
- Pluto needs its own heartbeat, because a dead path stalls instead of returning an error, and must recreate the Tailcat client to recover from a host restart.
- The default relays are run by Tailscale, are rate-limited, have no uptime promise and can be revoked. They suit logs and control traffic, not large uploads.
- Tailcat adds about 25 MB to the binary. Its security notes call it early and experimental, so Pluto uses only the tunnel, not its SSH or file features.

**Revisit when:** the cross-network test fails on common home or mobile networks, Tailscale restricts its relays, a Tailcat wire-format change breaks a release, or an audit of how we use it finds a problem.
