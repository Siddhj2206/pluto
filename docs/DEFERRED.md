# Deferred work

Everything here was deliberately cut from v1 during the 2026-10-04 pre-implementation restructure. Each entry names the trigger that revives it. Nothing here is forgotten; it is parked.

| Item | Why deferred | Revive trigger | Reference |
|---|---|---|---|
| Fleet layer: leases, epochs, CAS ownership, handoff | Single host is v1; the machinery is celld-scale work | A second host is actually in use | `research/durable-objects-leases` branch; ADR 0004 history |
| Bucket state, hibernate/export, SeaweedFS | Removes an external store from install and M0 | Local disk pressure or a second host | `research/disk-state-pipeline`; ADR 0004 history |
| Chunked CAS / desync dedup | Only pays with many boxes or hosts | Export is used enough to hurt | `research/disk-state-pipeline` |
| Continuous replication (qcow2 + dirty bitmaps) | Impossible under Firecracker's raw-only drives | Dead unless the VMM changes | `research/disk-state-pipeline` |
| BuildStream / FSDK image factory | Cold builds 50 min–2 h / ~100 GB; FSDK's kernel ships virtio-blk as a module, breaking initrd-less boot; depends on a small core team; a scripted rootfs is enough | Images are distributed and the OCI path proves insufficient | `research/buildstream-microvm-images`; #66 |
| devcontainer.json compatibility mapper | Nice adoption hook, not needed for M0 | Users ask, or M1 docs want the hook | `research/guest-environment` §5 |
| Relay, per-box ports, web/phone pairing | SSH + port-forward covers desktop and TUI | Browser/phone clients are actually wanted | `research/client-pairing`; ADR 0006 history |
| `pluto forward` (a wrapper for the documented `ssh -L` recipe) | The `ssh -L` recipe already reaches in-box web UIs; the wrapper is ergonomics, not capability | The hand-rolled recipe gets used often enough to hurt | ADR 0006; #44 |
| headscale, lighthouse VPS, DERP, tsnet | Bring-your-own network is simpler and avoids the VPS plus tsnet's default phone-home | Non-SSH clients need remote reach | `research/overlay-and-access`; ADR 0006 history |
| Wake-on-connection | `attach` implies `up`, so nothing needs to reach a sleeping box directly | Clients must reach boxes while paused | #25 history |
| Busy-signal composition (inhibitors, agent APIs, cgroups, tmux) | A no-client + no-job rule is enough first | Auto-pause misbehaves in practice | `research/auto-pause-signals` |
| Fork / copy-on-write boxes | Not needed for the core loop | Try-two-approaches workflows demand it | ADR 0002 history |
| MCP surface | CLI + SSH cover laptop and in-box agents | Laptop agents want structured tools | #26 history |
| herdr agent-aware panes | tmux is sufficient and non-load-bearing | Agent-aware pane UX is wanted | `research/box-pty-owner` |
| Multi-shape images, sysexts, per-project cache sharing | One image + the contract covers M0 | Real demand appears | `research/guest-environment` |
| Fork naming (`#n` keys) | Follows fork | Fork revives | ADR 0002 history |
| Adoption work: release channels, docs site, support matrix, sponsorship stance | Nothing to adopt yet | M0 works and someone shows up | `research/oss-onboarding`; #20 history |
| Team / multiplayer features | Single user | A real multi-user need | — |
| Host-loss backups | Git is the floor | Pain is felt; export is the first step | ADR 0002 |
