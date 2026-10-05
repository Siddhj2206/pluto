# Go everywhere; one daemon, per-box systemd units

pluto's runtime is Go end to end — host daemon, runner, CLI, and guest agent — because it produces one static, CGO-free binary for all four roles and covers everything v1 needs from the standard library and a small dependency set: systemd units via `systemctl` execs, vsock via `x/sys/unix`, the Firecracker HTTP API over a unix socket, and TOML parsing. The previous justification leaned on `tsnet` and `desync`; both are deferred (`docs/DEFERRED.md`), so they no longer anchor the choice. Python stays in image tooling.

The host daemon runs as a systemd user service with linger and is the single writer; the CLI is the same binary in client mode talking HTTP+JSON over a unix socket. Each box runs under its own `pluto-box@<uuid>.service`, so VMMs are not daemon children: a daemon restart re-adopts running boxes from their state directories, and a daemon upgrade does not kill running work. A host reboot stops boxes; the daemon resumes only the ones asked for.

## Considered options

- **Rust**: no v1 requirement pulls toward it now that the overlay and chunking libraries are deferred.
- **Daemon-spawned children with PID files**: hand-written reaping and re-adoption instead of systemd's.
- **Stop-and-reconcile on restart** (Ignite, Kata, Fly): kills running builds and agents on every daemon upgrade.
- **`firecracker-go-sdk`**: semi-dormant; the roughly six stable HTTP endpoints are handwritten.

## Consequences

- Recovery contract: daemon restart re-adopts; host reboot stops boxes and `up` wakes them.
- Dependencies stay few: a TOML parser, a hand-rolled cron validator (M1 schedules), and a handwritten Firecracker client; no D-Bus, no HTTP framework, no database.
