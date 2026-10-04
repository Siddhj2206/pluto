# Access is SSH; the network is yours

The CLI is the control surface and SSH is the only transport. The CLI talks to the host daemon over a unix socket; from another machine it ssh-execs itself (`pluto --host user@host …`). Boxes are reached through their guest sshd over vsock, mediated by the host. Web UIs running inside a box (opencode, T3 Code) are reached by port-forward — a documented `ssh -L`, with a `pluto forward` convenience later — which gives them the stable `localhost` origin they require. Authentication is SSH keys plus unix-socket permissions; pluto has no account system.

Reachability is the user's choice: a plain port-forward, WireGuard, or an existing tailnet. pluto ships no relay, no control plane, and no DERP; the embedded `tsnet`/headscale/lighthouse design is deferred with revive triggers in `docs/DEFERRED.md`. `attach` implies `up`, so nothing needs to reach a sleeping box, and wake-on-connection is deferred.

## Considered options

- **headscale + embedded DERP on a lighthouse VPS** (the previous decision): solves CGNAT reachability properly, but adds a rented machine, a control plane, and a phone-pairing story to v1 for desktop needs SSH already covers.
- **`tsnet` in the daemon**: 22 MB and a default logtail phone-home that contradicts the no-telemetry stance, for reachability we do not yet need.
- **A relay now**: gives browsers stable per-box origins; deferred until browsers are a real client.

## Consequences

- The phone/browser story waits for the deferred relay; the sanctioned path today is tailnet/WireGuard plus SSH.
- No new runtime dependencies; the daemon opens no ports.
- Supersedes the 2026-10-04 headscale/lighthouse/relay decision (issue #15).
