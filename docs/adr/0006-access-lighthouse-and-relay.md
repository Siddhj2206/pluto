# Access: a transport-agnostic relay, with a self-hosted control plane on one lighthouse

Boxes are reached through a relay inside the host daemon — per-box TCP listeners on the host node, forwarded to the guest over vsock — and the transport that reaches the host is deliberately pluggable. The product default is **headscale**, the independent BSD-3 control-plane implementation, with `tsnet` embedded in the host agent and an **embedded DERP on one small always-on VPS (the "lighthouse")** serving 443/TCP + 3478/UDP; Tailscale's public DERP fleet is documented as bootstrap/fallback only, so no Tailscale service is in the loop. The lighthouse exists because a host behind CGNAT has no reachable endpoint — a property of the network, not the protocol: every leaner alternative (a WireGuard hub, an SSH reverse-tunnel relay, tailcat) still needs the same public machine, so the choice is what runs on it, not whether it exists. M0 uses none of this: access is loopback through the host relay. Phone onboarding and auth are deferred; when picked up, headscale pre-auth keys with persistent nodes (no OIDC) are the default.

## Considered options

- **Tailscale-hosted control** (the pre-existing personal tailnet): zero setup, but not self-hosted — usable only as a personal stopgap.
- **WireGuard hub / SSH reverse-tunnel relay**: leaner software and no control plane; the SSH relay is the sanctioned personal staging path until the phone story, and swapping transports does not change the relay design.
- **tailcat**: control-plane-free, but no mobile client — it cannot be the product path.

## Consequences

- One rented always-on machine is a deliberate, documented requirement of remote (non-loopback) access; users with a public endpoint or an existing tailnet can skip it.
- Taildrop/Taildrive are not adopted — git stays the only sync layer.
- Per-box tailnet identities remain fog; v1 exposes per-box ports on the single host node.
