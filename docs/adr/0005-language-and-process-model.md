# Go everywhere; one daemon, per-box systemd units

pluto's runtime is Go end to end — host daemon, runner, CLI, and guest agent — because its two locked anchors are Go libraries: `tsnet` (the embeddable overlay client the access decision requires; no production-grade equivalent exists elsewhere) and `desync` (chunking, importable with no shell-out). Python stays in image tooling, and Rust's VMM-world affinity is moot because Firecracker is driven over HTTP and never linked. The host daemon runs as a systemd user service with linger and is the single writer, exposing HTTP+JSON over a unix socket with the CLI as the same binary in client mode. Each box runs under its own `pluto-box@<uuid>.service`, so VMM processes are not daemon children: a daemon restart re-adopts running boxes from their state dirs and API sockets, a deliberate divergence from Ignite, firecracker-containerd, Kata, and Fly, which all stop and reconcile — systemd supervision makes re-adoption cheap, and a daemon upgrade must not kill running builds or agents.

## Considered options

- **Rust, or a Go/Rust split**: no embeddable tailnet client (an external `tailscaled` would break the host-relay design) and desync would become a subprocess.
- **Daemon-spawned children with PID files**: hand-written reaping and re-adoption, weaker isolation, no journal or cgroup story — systemd user units already do this job.
- **Stop-and-reconcile on restart** (the comparables' pattern): would kill running work on every daemon upgrade.
- **`firecracker-go-sdk`**: semi-dormant (last release 2022); a small handwritten client over the UDS covers the roughly six stable endpoints pluto needs.

## Consequences

- Recovery contract: a host reboot stops boxes (units do not auto-start; the daemon decides what to resume); a daemon restart leaves them running and re-adopted.
- Six runtime dependencies total — `tsnet`, `desync`, `aws-sdk-go-v2/service/s3`, `netlink`, `robfig/cron`, `BurntSushi/toml` — with systemd interaction via `systemctl`/`loginctl` execs rather than D-Bus.
- Everything pluto-shaped is written: runner, lease/epoch protocol, store layout, relay, guest agent, `pluto-firstboot`, CLI/API, image build script.
