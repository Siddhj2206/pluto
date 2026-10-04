# pluto fact note: language, stack, process model (#16)

Fact-finding for the grilling. Primary sources: project docs/source, release metadata, and local
builds on this machine (Go 1.27.1, Rust 1.98.1, CGO off, no repo writes). Fetched 2026-10-04.
Repo context: `research/overlay-and-access`, `research/disk-state-pipeline`,
`research/boot-path-check`, `research/guest-environment`, ADR 0001/0004.

---

## 1. tsnet (`tailscale.com/tsnet`)

- **Embedding**: package runs a self-contained Tailscale node in-process on a **userspace gVisor
  TCP/IP stack; no root, no TUN, no system daemon, multiple nodes per binary, state in a directory
  you control** ([tsnet package doc, v1.104.0](https://pkg.go.dev/tailscale.com/tsnet);
  [tsnet.go](https://github.com/tailscale/tailscale/blob/main/tsnet/tsnet.go);
  [KB tsnet page, validated 2026-07-24](https://tailscale.com/docs/features/tsnet)).
  `Server{Dir, AuthKey, Hostname, Ephemeral, AdvertiseTags, ControlURL}`; identity store defaults to
  `Dir/tailscaled.state`; auth via `AuthKey`/`TS_AUTHKEY` (or interactive URL).
- **Self-hosted control server**: `Server.ControlURL` — "optionally specifies the coordination
  server URL. If empty, it defaults to the `TS_CONTROL_URL` environment variable" (Server doc).
  Headscale supports the last 10 Tailscale client releases on all platforms
  ([headscale clients](https://headscale.net/stable/about/clients/)); users run tsnet nodes against
  headscale (headscale issue #3151, closed; same-host caveat only). tsnet is a control-protocol
  client, not headscale-specific.
- **Relay suitability**: `Listen(network,addr) (net.Listener)` / `Dial(ctx,…) (net.Conn)` route only
  over the tailnet; plus `ListenPacket`, `ListenTLS`, `ListenSSH`, `HTTPClient`, `Loopback()`
  (SOCKS5 + LocalAPI), `WhoIs` (caller identity), `TailscaleIPs`, `Up`. Standard `net` interfaces, so
  a relay can bridge tailnet conns to host-local unix sockets/vsock (full list from `go doc
  tailscale.com/tsnet.Server`).
- **Privacy**: tsnet starts a logtail uploader by default (logpolicy config written to
  `<Dir>/tailscaled.log.conf`, collection node `log.tailscale.com`; `tsnet.go:startLogger`). Only
  `tailscaled`/`k8s-operator` honor `TS_NO_LOGS_NO_SUPPORT` (`envknob.NoLogsNoSupport`); tsnet does
  not check it in v1.104.0. Self-hosters must pre-seed/accept the log config.
- **Maintenance**: part of `tailscale/tailscale`, BSD-3-Clause, repo pushed 2026-10-02, latest
  release v1.102.5 (2026-09-29), module tag v1.104.0 resolvable; used internally by Tailscale
  (golink, support tooling) per KB.
- **Non-Go equivalents**:
  - `tailscale-rs` — **official Tailscale org Rust library**, crate `tailscale` 0.6.1 (2026-09-18),
    active (~monthly), Apache-2.0. `Config.control_server_url` + `TS_CONTROL_URL`, Linux/macOS only.
    WIP caveats: "no compatibility guarantees", NAT traversal WIP ("may fall back to DERP"),
    unimplemented features; MSRV 1.97
    ([README](https://github.com/tailscale/tailscale-rs/blob/main/README.md),
    [config.rs](https://github.com/tailscale/tailscale-rs/blob/main/src/config.rs)).
  - `libtailscale` — official C ABI around tsnet (c-archive/c-shared), BSD-3-Clause, pushed
    2026-08-31, Swift/Python/Ruby bindings ([README](https://github.com/tailscale/libtailscale)).
    It embeds a Go runtime in the host process.
  - Stale: Rust `tsnet` crate 0.1.0 (2023) is a libtailscale binding
    ([badboy/libtailscale](https://github.com/badboy/libtailscale)).
- **License**: BSD-3-Clause (repo + module).

## 2. desync: library vs CLI

- **Importable and stable**: module `github.com/folbricht/desync` v1.1.4 (tag 2026-09-22), `go 1.25.0`,
  BSD-3-Clause. Repo pushed 2026-10-01; v1.1.0–v1.1.4 all shipped Aug–Sep 2026
  ([module metadata via `go get`, release list](https://github.com/folbricht/desync/releases)).
- **Everything pluto needs is exported from the root package** (verified with `go doc`; source in
  module cache):
  - make: `IndexFromFile(ctx, name, n, min, avg, max, pb) (Index, ChunkingStats, error)`,
    `ChopFile(ctx, name, chunks, WriteStore, n, pb)`, `ChunkStream`, `Chunker`, `NewDedupQueue`,
    `NewLocalStore`, `NewS3Store(location, creds, region, opt, lookupType)`, `S3Store.Prune`.
  - extract/seeds: `AssembleFile(ctx, name, idx, Store, []Seed, AssembleOptions)`,
    `NewIndexSeed(dst, src, index)`, `FileSeed.RegenerateIndex`, `NullSeed`, `Cache`/`RepairableCache`,
    `NewFailoverGroup`.
  - mount-index: `MountIndex(ctx, idx, MountFS, path, Store, n)` + `NewIndexMountFS`; FUSE impl is in
    the library (`mount-index.go`, `hanwen/go-fuse/v2`), so no shell-out.
  - indexes: `IndexFromReader`, `LocalIndexStore`, `NewS3IndexStore`, `NewHTTPIndexHandler`.
- **The CLI is thin wiring over those calls** (`cmd/desync/make.go` is `IndexFromFile` + `ChopFile` +
  `storeCaibxFile`; `extract.go` is store open + seeds + `AssembleFile`). Nothing pluto needs is
  CLI-only; CLI convenience code (`MultiStoreWithCache`, config files) is small and re-implementable.
- **Weight**: cmd/desync static binary built here = **38,985,888 B** (includes cobra, minio-go, GCS,
  OCI, SFTP); importing the root package and using only S3/local pulls a smaller link, but minio-go +
  go-fuse + zstd remain. Root package uses a global `logrus` `Log` var and a `ProgressBar` interface
  (pass `NewNullProgressBar`) — cosmetic, not blocking.
- License BSD-3-Clause; release health good (5 releases in the 6 weeks before this note).

## 3. Firecracker Go SDK vs raw HTTP API

- **SDK status**: `firecracker-microvm/firecracker-go-sdk` is **not archived** (Apache-2.0), but
  low-maintenance: **last release v1.0.0 (2022-09-07)**, last non-dependabot commits mid-2025, repo
  push 2026-02-10; open issue #590 "release: firecracker go sdk official release tag" (2025-09) and
  #690 "Swagger version too old" (2025-11). `Version = "1.0.0"` constant.
- It is more than a client: handlers that **exec the firecracker/jailer process**, CNI networking,
  vsock dialer. `Machine` has `Start`, `Shutdown`, `StopVMM`, `PauseVM/ResumeVM`, `CreateSnapshot`.
  `client/` + `client/models/` are separately importable OpenAPI-generated models;
  `vsock.Dial/Listener` is usable standalone.
- **Direct HTTP is the sane path for pluto's thin VMM seam**: Firecracker is "each process
  encapsulates one and only one microVM", API is an in-process HTTP server on a UDS
  ([design.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md));
  pluto already drives boot/drives/vsock/actions as `PUT` JSON (research/boot-path-check).
  Boot path: `PUT /boot-source`, `/drives`, `/machine-config`, `/vsock`, `/actions InstanceStart`;
  `SendCtrlAltDel` is the documented clean-shutdown trigger; there is no `shutdown` endpoint
  ([actions.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/api_requests/actions.md)).
  `net/http` + `http.Transport{DialContext: unix dialer}` covers everything; no CGO.
- **API stability**: Firecracker follows semver for its API; breaking changes bump major, deprecated
  endpoints survive until the next major ([api-change-runbook.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/api-change-runbook.md)).
  In-tree OpenAPI spec is the source of truth; current release v1.17.0 (2026-09-10).
  The SDK's stale generated models are a reason to not depend on it.

## 4. vsock plumbing

- **Host side = plain UDS.** Firecracker maps guest AF_VSOCK ports 1:1 to host AF_UNIX sockets:
  host→guest: connect to `<uds_path>`, send `CONNECT <port>\n`, read `OK <hostside_port>\n`;
  guest→host: Firecracker forwards to `<uds_path>_<port>`
  ([docs/vsock.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md)).
  Firecracker "bypass[es] vhost kernel code on the host", so the host side is ordinary Unix sockets;
  the doc still lists host `CONFIG_VHOST_VSOCK=m` and guest `CONFIG_VIRTIO_VSOCKETS=y` + `/dev/vsock`
  as prerequisites (boot-path-check observed `/dev/vhost-vsock` world-writable and vsock working
  unprivileged on this host).
- **Guest side = AF_VSOCK.** Go: `golang.org/x/sys/unix` v0.48.0 has `AF_VSOCK`, `SockaddrVM`,
  `RawSockaddrVM` on Linux ([syscall_linux.go](https://cs.opensource.google/go/x/sys/+/master:unix/syscall_linux.go));
  `mdlayher/vsock` v1.3.0 (MIT, pushed 2026-08-01) wraps it with `net.Conn`/`net.Listener`,
  `ListenContextID`, and `FileListener` (systemd socket-activation FDs)
  ([pkg.go.dev](https://pkg.go.dev/github.com/mdlayher/vsock)).
- Rust: no std support; `tokio-vsock` 0.7.2 (Apache-2.0, pushed 2026-06-23) and `vsock-rs` 0.5.4
  (Apache-2.0, pushed 2026-06-23) are the maintained crates ([rust-vsock org](https://github.com/rust-vsock)).
- Guest systemd ≥255 can bind `ListenStream=vsock::22` natively (guest-environment research).

## 5. systemd interaction

- **Go**: `coreos/go-systemd` v22.7.0 (Apache-2.0; tag 2024-05-03, repo pushed 2026-07-23 — actively
  touched, infrequent releases). `dbus.NewUserConnection` / `NewSystemConnection`;
  `StartUnit`, `StartTransientUnit`, `RestartUnit`, `EnableUnitFiles`, `ListUnits`,
  `SetSubStateSubscriber`; `login1.Conn.Inhibit(what, who, why, mode)` exists.
  **No linger wrapper** in `login1` (grep of v22.7.0): call `loginctl enable-linger` or the
  `org.freedesktop.login1.Manager` method via `godbus/dbus/v5` directly. Linger semantics:
  "a user manager is spawned for the user at boot and kept around after logouts"
  ([loginctl(1)](https://www.freedesktop.org/software/systemd/man/latest/loginctl.html)).
- **Rust**: `zbus` **5.19.0** (2026-08-09, 90M downloads, org now `z-galaxy/zbus`), pure Rust, "doesn't
  depend on C libraries" ([README](https://github.com/z-galaxy/zbus)) — call the same systemd/logind
  D-Bus interfaces with typed proxies; no exec of `systemctl` required. `zbus` alone does not provide
  systemd-specific helpers, so interface code is hand-written/XML-generated.
- Either way, the daemon path is D-Bus (user bus at `$XDG_RUNTIME_DIR/systemd/private` for user units);
  `systemctl` exec is the escape hatch. User units + linger is the no-root shape pluto wants.

## 6. Static binaries (measured here, CGO off / musl)

| Binary | Toolchain | Size | Notes |
|---|---|---|---|
| tsnet demo (Listen + Up) | Go 1.27.1, `CGO_ENABLED=0 -s -w` | **22,196,384 B** | statically linked; pulls gVisor/wireguard-go |
| agent demo (vsock + go-systemd dbus + cron) | same | **3,121,312 B** | statically linked |
| desync CLI (full backends) | same | **38,985,888 B** | statically linked |
| Rust hello | 1.98.1, `x86_64-unknown-linux-musl` | **563,584 B** | static-pie |
| Rust zbus + tokio-vsock + croner | same, release | **3,220,424 B** | static-pie, not stripped |

- Go static needs no musl: pure-Go std/net + deps, `CGO_ENABLED=0` is sufficient (all four Go builds
  here are `statically linked` ELF). Timezone: `import _ "time/tzdata"` adds ~450 KB
  ([time/tzdata doc](https://pkg.go.dev/time/tzdata)).
- Rust musl target installed via rustup; pure-Rust deps (zbus) keep it static. celld's shipped
  artifacts are `x86_64-unknown-linux-gnu.gz` (25.1 MB gz), not musl
  ([celld v0.6.1 assets](https://github.com/denoland/celld/releases/tag/v0.6.1)) — size difference is
  mostly profile/backends, not language.

## 7. Cron parsing

- **Go `robfig/cron/v3`**: API frozen at v3.0.1 (tag 2020-01-04); repo last pushed 2024-07-08; MIT;
  14k stars. `cron.New(cron.WithLocation(loc))` defaults to `time.Local`; each spec may carry
  `TZ=` or `CRON_TZ=` (parser.go: `loc = time.Local`; `time.LoadLocation`). Standard/DST-correct via
  `time.Location`. Dormant but stable; no recent bug-fix releases.
- **Rust `croner` 4.0.1** (2026-10-02, MIT) — "Evaluate cron expressions across different time zones",
  via `chrono-tz` or `jiff` ([README](https://github.com/hexagon/croner-rust)). Alternative
  `cron` 0.17.0 (2026-06-18, Apache-2.0, pushed 2026-06-21) also documents timezone support. Both are
  actively maintained; `croner` is the richer POSIX/Vixie + Quartz-specifier parser.
- Host-local-TZ semantics are available in all three; the fact to settle is only dependency
  preference (Go scheduler → robfig; Rust scheduler → croner).

## 8. Comparables' stacks / process models (brief)

- **celld** (`denoland/celld`, Rust, Apache-2.0, active): "run celld as one process on each machine"
  = a *node*; nodes share one bucket; no leader/membership — ownership by conditional write; release
  profile fat-LTO/strip/panic=abort. Relevant as the lease/single-binary precedent, not a VMM host.
  ([docs/README.md](https://github.com/denoland/celld/blob/main/docs/README.md),
  [Cargo.toml](https://github.com/denoland/celld/blob/main/Cargo.toml))
- **Weave Ignite** (Go, Apache-2.0, **archived 2023-12-07**): VMs run as containerd tasks; the
  `ignite-spawn` shim execs Firecracker and patches VM state to `stopped` when it exits
  ([ignite-spawn.go](https://github.com/weaveworks/ignite/blob/main/cmd/ignite-spawn/ignite-spawn.go)).
  Supervision = containerd's task model; archived, so a historical reference only.
- **firecracker-containerd** (Go, active, pushed 2026-09-25): preferred model is **1 host shim per
  VM**, shim out-of-process and coupled to the VMM; a single shim holds the VM's lifecycle
  ([shim-design.md](https://github.com/firecracker-microvm/firecracker-containerd/blob/main/docs/shim-design.md),
  [architecture.md](https://github.com/firecracker-microvm/firecracker-containerd/blob/main/docs/architecture.md)).
- **Kata Containers** (active; Go runtime + Rust `runtime-rs`): `containerd-shim-kata-v2` per
  sandbox; control channel to the guest agent over **vsock**
  ([VSocks.md](https://github.com/kata-containers/kata-containers/blob/main/docs/design/VSocks.md)).
  Same "shim owns the VM" pattern.
- **containerd runtime v2** (the pattern above): a shim is an independent daemon/socket that parents
  the workload; containerd reconnects to shim sockets rather than owning container processes
  ([runtime-v2.md](https://github.com/containerd/containerd/blob/main/docs/runtime-v2.md)). Current
  docs do not promise adoption of a workload after the *shim* itself dies.
- **Fly Machines** (public, Fly): per-host orchestrator `flyd` + a Firecracker supervisor; supervisor
  waits on `fc.sock` and retries the `bind`-before-`listen` race (community post "More reliable
  Machine resumes"); **migration stops the Machine first**, copies the volume, starts on the new host
  (same Machine ID, new 6PN) ([migration docs](https://docs.fly.io/reference/machine-migration.md));
  host outage leaves a single-Machine app down until re-created, volumes pinned
  ([host-unavailable docs](https://docs.fly.io/apps/trouble-host-unavailable.md)); suspended =
  Firecracker snapshot ([suspend/resume docs](https://docs.fly.io/reference/suspend-resume.md)).
  No public evidence that a restarted supervisor adopts running VMs; the public pattern is
  stop-then-recreate/reconcile.
- **Adoption after supervisor restart** (all comparables): the VMM process is spawned and parented by
  a per-VM agent (containerd shim / ignite-spawn / flyd supervisor). Reconnecting to a live
  Firecracker is *technically* just reconnecting to its API UDS, but no surveyed system documents
  adopting a running VMM across its supervisor's restart; recovery is reconcile/restart. This matches
  ADR 0003's "durable record + re-materialize" stance and ADR 0002's "paused box = dead VMM + disk".

---

## Decisive facts vs choices still to make

**Decisive (facts that constrain the choice)**

1. Go covers every locked integration natively and CGO-free: tsnet (no root, ControlURL→headscale),
   desync as an importable library, systemd D-Bus user units, AF_VSOCK, static binaries.
2. tsnet is the only *production*-grade embeddable overlay client with a Go API; the only maintained
   Rust path is official but WIP (`tailscale-rs` 0.6.x: no compat guarantees, NAT traversal
   incomplete); `libtailscale` embeds Go via C.
3. desync needs no shell-out: make/extract/seeds/mount-index/S3 stores are all exported from the root
   package; the CLI is thin wiring.
4. firecracker-go-sdk is semi-dormant (last release 2022; models drift from current Firecracker); the
   Firecracker HTTP/UDS API is small, semver-stable, and fully adequate for boot/block/vsock/actions.
5. vsock is plain UDS on the host and AF_VSOCK in the guest; both Go (`x/sys/unix`,
   `mdlayher/vsock`) and Rust (`vsock-rs`, `tokio-vsock`) have maintained support.
6. Static binaries are equivalent in practice: Go agent-shaped 3.1 MB vs Rust musl
   zbus+vsock+croner 3.2 MB; tsnet's 22 MB is the price of the overlay client, not of Go.
7. No comparable adopts running VMMs after a supervisor restart; they stop/reconcile. First-party
   recovery must come from pluto's own durable record (already ADR 0003 shape).

**Choices still to make (grill material)**

- One language (Go) vs Go host+CLI with Rust only if a Rust VMM surface appears; tsnet effectively
  forces Go for the host daemon unless `tailscale-rs`'s caveats are acceptable.
- Process model: one long-lived daemon with runner subprocesses (children, die with daemon) vs
  per-box self-supervising processes/units that outlive the daemon; user systemd units + linger vs
  in-daemon supervisor.
- Recovery semantics after daemon restart: re-adopt via API socket + persisted pid/socket, or kill &
  rebuild from the durable record (comparables choose the latter).
- Where the API lives: CLI-embedded, daemon HTTP surface, MCP — and whether tsnet is the only
  listener or loopback is also exposed.
- Dependency budget: tsnet (22 MB) + desync (minio-go/go-fuse) + systemd dbus + cron, versus
  re-implementing any piece; and whether to import the SDK's generated models or write the few
  Firecracker request structs by hand.
