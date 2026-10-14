# Container backend behind the runner seam

Researched 2026-10-09 for wayfinder ticket #119 (part of #117, under the moonshot
#116). Primary sources only; quotes are short and verbatim, each claim carries its
link and an access date of 2026-10-09 unless noted. Evidence is tagged:

- **[docs]** — documented behaviour on the vendor's own pages.
- **[vendor claim]** — the vendor describing its own product; a claim about intent.
- **[repo]** — this repository's source or docs.
- **[inference]** — this author's reading, not a source's or a vendor's statement.
- **[uncertain]** — could not be verified against a primary source here.

**Companions, not repeats.** `container-and-microvm-hardening.md` settles the
security question (a hardened container and a microVM are near a wash for trusted
code on owned hardware) and carries the 15-item container baseline. That note is
assumed; this one answers the *engineering* question: what the current code
assumes, what a container backend needs, and what the smallest experiment is.
Where the two overlap, the hardening note is cited rather than re-derived.

**Scope.** Individuals and small teams, trusted code, hardware they own, a handful
of machines — not enterprises, not fleets. Complexity must justify itself at that
scale. Security is explicitly not the decider; density, DX, and cost of change
are.

---

## 1. The answer, up front

A container backend fits behind the seam pluto already has (`daemon.BoxRunner`),
and fits it *without* the substrate becoming a second architecture. Seven
decisions, each argued below and stated in full in §7:

| # | Decision | Confidence |
|---|---|---|
| D1 | The seam is `daemon.BoxRunner`; do not widen it. Add the optional capability via the existing optional-interface assertions. | [repo] verified |
| D2 | Container runtime = rootless podman + crun driven through its CLI, not raw namespaces and not the API socket. | [inference], evidence in §3.1 |
| D3 | Keep the guest agent and its protocol; change only the transport (vsock → `podman exec` stdio). | [repo] verified |
| D4 | One image source (the existing Containerfile), two artifact shapes: microVM blob and OCI image. | [inference], evidence in §3.2 |
| D5 | `Up` = one container per run from a warm base; `Destroy` = remove the container; the durable thing stays on the host. | [inference], mechanism in §3.2 |
| D6 | Enter-on-failure is `podman exec`, not ssh. `api.AttachInfo` grows an enter-argv field. | [inference], evidence in §3.3 |
| D7 | Build one container backend with one flag and measure; do not build the abstraction layer first. | [inference] |

---

## 2. What today's code assumes about microVMs

The facts below were read in the working tree on 2026-10-09; every claim cites
file and line. Nothing here was assumed from the earlier explorer pass without
checking.

### 2.1 The seam is already real, and it is clean

`daemon.BoxRunner` (`internal/daemon/daemon.go:33-48`) is a 14-method interface:
`Up`, `Pause`, `Attach`, `Reconcile`, `Refresh`, `RunJob`, `Logs`, `JobLog`,
`Metrics`, `AutoPauseWindow`, `ContractStale`, `Destroy`, `Import`, `Images`. The
`Server` holds it as a field (`daemon.go:57`) and takes it in `New`
(`daemon.go:90`). Nothing in the daemon names Firecracker, vsock, a kernel, or a
disk image. [repo]

Two things sit *outside* the interface and use optional-interface assertions,
which is the pattern the moonshot should keep rather than widen:

- `boxServiceForwarder` — `ForwardService` for public ingress
  (`daemon.go:50-52`, asserted at `daemon.go:262`).
- an inline interface for `AdvancePRRef` — scheduled PR fast-forward
  (`daemon/schedule.go:273-274`, called at `schedule.go:317`).

The whole daemon test suite already runs against a fake
(`internal/daemon/daemon_test.go:33`, `fakeRunner`), so a second implementation
inherits ~2,000 lines of daemon tests unchanged. [repo]

### 2.2 The concrete runner is wired in one place

`runner.Runner` is a concrete struct (`internal/runner/runner.go:52-96`) built by
`runner.New` (`runner.go:100-127`) and constructed exactly once, in
`internal/cli/host.go:56`, immediately followed by `rn.ReconcileAll()`
(`host.go:57`). `ReconcileAll` is *not* part of `BoxRunner`; a container backend
needs its own, so the startup call must move behind an assertion or an extended
interface. That single line is the whole cost of the seam on the host side.
[repo]

### 2.3 Where Firecracker is load-bearing

These are the parts a container backend must not inherit. All are in
`internal/runner`, except vsock.

| Concern | Code | What a container backend replaces it with |
|---|---|---|
| Per-box unit main process, namespaces, tap bridge, slirp4netns | `boxrun.go:20-104` (`BoxRun`), `:108-130` (`BoxHolder`), `:135-156` (`bridgeTaps`) | nothing — crun sets the namespaces up |
| Pre-boot VMM config | `runner.go:843-879` (`writeConfig`, `fcConfig`, `bootArgs`) | an OCI spec, or podman CLI flags |
| Stop path (graceful) | `fcapi.go:17` (`sendCtrlAltDel` → `PUT /actions`), used at `runner.go:358` | `podman stop` with a timeout |
| Host↔box channel | `internal/vsock` (`vsock.go` `Connect`, `listen.go` `Listen`), `agent.NewClient(vsockUDS)` at `agent/client.go:27-33`, `runner.NewAgent` at `runner.go:121` | `podman exec` stdio, or a socket (§3.5) |
| Metrics | `runner/metrics.go` (Firecracker's `metrics.json` flushes) | cgroup files, or none |
| Image artifact | `runner.go:684-701` (`checkImage`/`imageComplete`: `vmlinuz`, `rootfs.img`, `firecracker`), `runner.go:500-545` (`Import`, hash-verified manifest), `images/Containerfile`, `images/boot.sh` | an OCI image in podman's graphroot |
| Disk preparation | `disk.go` `prepareDiskFrom`, `markProvisioned` (debugfs into ext4), `envscrub.go` `removeExtTree` (debugfs) | an image layer, `podman commit`, and (scrubbing) a container filesystem diff |

### 2.4 What the guest side assumes, and how thin it is

The guest-side coupling is `agent.Systemd` (`internal/agent/system.go`), which is
surprisingly reusable:

- user systemd units for hooks, jobs, services, and sessions
  (`system.go:61-109` `RunHook`, `:121-190` `RunJob`, `:377-418`
  `RestartServices`, `:449-495` `StartSessions`);
- `pgrep -c -f 'sshd:.*@'` to count live ssh sessions (`system.go:241-254`) — this
  is the only genuinely ssh-shaped piece, and it feeds the auto-pause busy
  signal;
- cgroup v2 reads for the same busy signal (`system.go:543-619`: `cpu.stat`
  `usage_usec`, `io.stat` `rbytes`/`wbytes`);
- `journalctl --user` for service logs (`system.go:440-447`);
- `/home/dev/work/<project>` as the in-box worktree path
  (`runner/handoff.go:175-181`).

Everything there works inside a container as-is *if* the container runs a user
systemd manager — which rootless podman can do (`--systemd=always`, §3.2). The
two things that do not survive unchanged are the session counter (no sshd) and,
possibly, the cgroup visibility (§8).

### 2.5 The store, the contract, and the client

- `state.Box` (`internal/state/state.go:77-137`) carries `Image` (a content
  blob version), `Resources`, `Phases`, `Jobs`, `Remotes`, `EnvironmentLayer`,
  `CleanStop`, `ContractHash`. None of those fields is microVM-shaped except
  `Image`, and even that is "whatever the backend pinned". [repo]
- `contract.Box.Image` (`internal/contract/contract.go:169-175`,
  `docs/contract.md:104`) is documented as the blob version printed by
  `pluto image ls`. This is the vocabulary that has to grow a second kind.
- `api.AttachInfo` (`internal/api/api.go:79-84`) is `User`/`UDS`/`Key`/`Port`,
  and `internal/cli/attach.go:121-137` hard-codes an ssh invocation with
  `ProxyCommand=pluto vsock connect <uds> <port>`. Attach is the one client-side
  place where the substrate is baked in.
- Environment layers (`internal/runner/environment_cache.go:23-113`,
  `internal/envcache/cache.go`) are the existing bridge between durable prepared
  state and ephemeral machines: content-addressed by project + setup + image +
  trust class, published only after a clean stop and a successful provision,
  scrubbed before becoming visible. A container backend keeps the *policy* and
  replaces the *mechanism* (ext4 + debugfs → image layers + diff).
- Queue, schedules, tasks, trust, webhooks, providers
  (`internal/daemon/schedule.go`, `webhook.go`, `internal/state/queue.go`,
  `task.go`) are substrate-free. [repo]

---

## 3. What a container backend needs

### 3.1 Rootless podman versus raw namespaces

Two candidate implementations.

**(a) Rootless podman.** Podman documents itself as "a simple daemonless tool"
where "Most Podman commands can be run as a regular user, without requiring
additional privileges", creating a user namespace defined by `/etc/subuid` and
`/etc/subgid` ([podman(1)](https://docs.podman.io/en/latest/markdown/podman.1.html)). It
brings the OCI image store, a storage driver, snapshotting, per-container
namespaces and cgroups, log drivers, `podman exec` (enter), and registry auth.
Its default runtime is crun ("the default runtime is `crun`", [podman(1)]),
which the crun authors describe as "A fast and low-memory footprint OCI Container
Runtime fully written in C" with a published 100-container benchmark of 1.69 s
versus runc's 3.34 s ([crun README](https://github.com/containers/crun)) — that
is ~17 ms versus ~33 ms per `run /bin/true`, i.e. [vendor claim], but the only
published numbers available and they are the right order of magnitude for "many
short runs".

**(b) Raw namespaces.** pluto already has the skill: `unshare -Urn` plus
`pivot_root` in `boxrun.go`, a rootless bridge in `bridgeTaps`, and slirp4netns
egress. A container backend could reuse the imagebuilder's existing
"export the rootfs and unpack it" path (`internal/imagebuilder/assemble.go`
extracts a tar and builds ext4 under `podman unshare`) and run the unpacked tree
under a user namespace with an overlayfs upper layer.

**(a) is the right default, (b) is the fallback.** The reasons, at this scale:

- (b) means implementing overlayfs mounting, uid mapping, a process supervisor,
  an exec-into-the-namespace path, and per-run cleanup — the five things podman
  already does and that are exactly where container tooling earns its keep.
- (b) is *not* much code for a first prototype (a few hundred lines), and it has
  one real advantage: zero new host prerequisites. If podman/crun/pasta turn out
  to be too much to ask of a self-hoster, (b) is the escape hatch and it shares
  the whole runner side.
- Neither is a daemon. Podman's CLI shape ("daemonless") matches pluto's
  existing posture of shelling out to `systemctl`, `git`, `ip`, and Firecracker.

**Host prerequisites to document** (the current product's are `/dev/kvm`,
`slirp4netns`, `ip`, `debugfs`, `ssh` — `images/README.md`):

- `/etc/subuid` and `/etc/subgid` entries; Podman warns "It is required to have
  multiple UIDS/GIDS set for a user" ([podman(1) rootless section) and lists
  failures as a known rootless shortcoming ([rootless.md](https://github.com/containers/podman/blob/main/rootless.md)).
- `pasta` — "Currently pasta is required to be installed to create a network
  device, otherwise rootless containers need to run in the network namespace of
  the host" ([podman(1)]). Without it there is no egress isolation at all.
- overlayfs support: "The Overlay file system (OverlayFS) is not supported with
  kernels prior to 5.12.9 in rootless mode", otherwise the slower `fuse-overlayfs`
  is used ([podman(1)]). The podman rootless note states the same requirement as
  "a Linux kernel version >= 5.12" for native overlayfs as an unprivileged user
  ([rootless.md]) — the two doc pages disagree on the exact floor, so treat
  5.12 as the line and 5.12.9 as the safer one. [uncertain: which of the two is
  authoritative]
- A writable home directory "not mounted with noexec or nodev", and no NFS or
  parallel filesystem for storage ([rootless.md]). Relevant if pluto ever points
  a state dir at a network share.

### 3.2 A warm base plus an ephemeral layer

This is the "warm base, clean run" property from #116, and the container
mechanics for it are unusually cheap.

**Warm base.** An OCI image is a stack of layers; the runtime composes them into
one rootfs per container. Podman's default storage driver is overlay, with
fuse-overlayfs as the rootless fallback ([podman(1)]). The kernel documents the
shape: "An overlay filesystem combines two filesystems - an 'upper' filesystem
and a 'lower' filesystem", with multiple lower layers stacked right-to-left
([overlayfs docs](https://docs.kernel.org/filesystems/overlayfs.html)). One warm
base image plus a per-run upper directory *is* the architecture; no new
machinery is needed.

**Ephemeral layer.** Two mechanisms, both first-class:

- `podman run --rm` — "Automatically remove the container and any anonymous
  unnamed volume associated with the container when it exits"
  ([podman-run(1)](https://docs.podman.io/en/latest/markdown/podman-run.1.html)).
- The overlayfs `volatile` mount option, which exists precisely because "all
  forms of sync calls to the upper filesystem are omitted" when the upper's
  contents can be recreated ([overlayfs docs]) — the right trade for a run whose
  only durable artifacts are on the host.

**Read-only base.** `--read-only` mounts "the container's root filesystem as
read-only", with `--read-only-tmpfs` mounting a read-write tmpfs on `/dev`,
`/dev/shm`, `/run`, `/tmp`, and `/var/tmp` (podman-run). This is the same
"finite writable surface" the hardening note recommends as CIS control 5.12, and
it composes with the ephemeral layer: the image layers are shared read-only
across every run, and each run's writes live in its own upper dir.

**Transient metadata.** Podman's `--transient-store` "allows starting containers
faster, as well as guaranteeing a fresh state on boot", while explicitly being
"not compatible with a traditional model where containers persist across reboots"
([podman(1)]). For CI fan-out that is a feature, not a limitation; for a durable
dev box it is the wrong choice. This is a clean, documented place where the two
modes differ.

**Keeping systemd inside.** `--systemd=true|false|always` runs "the container in
systemd mode", mounting tmpfs on `/run`, `/run/lock`, `/tmp`, and
`/var/lib/journal`, setting the stop signal to `SIGRTMIN+3`, and — on cgroup v2 —
mounting `/sys/fs/cgroup` writable, which "allows systemd to run in a confined
container without any modifications" ([podman-run(1)]). That is what makes the
existing `agent.Systemd` code and the existing image (systemd, tmux, git, sshd,
a `dev` user with linger) reusable inside a container with almost no change.
[inference, but the mechanism is documented]

**Caveat to verify:** on SELinux hosts, "systemd attempts to write to the cgroup
file system. Containers writing to the cgroup file system are denied by default"
and podman documents the `container_manage_cgroup` boolean as the fix
([podman-run(1)]). On an AppArmor host the equivalent question is open (§8).

### 3.3 Networking

pluto's current egress is rootless Firecracker behind `unshare -Urn` with
slirp4netns and a tap bridge (`boxrun.go:83-98`, `bridgeTaps`). The container
equivalent is one flag, and the ecosystem has moved past slirp:

- Rootless default is pasta: "**pasta[:OPTIONS,…]**: use **pasta**(1) to create a
  user-mode networking stack. This is the default for rootless containers and
  only supported in rootless mode" ([podman-run(1)]); `containers.conf` records
  `default_rootless_network_cmd="pasta"` over `slirp4netns`
  ([containers.conf(5)](https://github.com/containers/common/blob/main/docs/containers.conf.5.md)).
- Upstream says so too: "As of Podman 5.0, pasta is the default networking tool"
  ([rootless.md]). passt's own man page describes pasta as providing "equivalent
  functionality to network namespaces, as the one offered by **passt** for
  virtual machines", and notes that unlike slirp4netns it "doesn't implement a
  full TCP stack" ([passt(1)/pasta(1)](https://passt.top/builds/latest/web/passt.1.html)).
- Podman already passes the flags that matter for isolation: `--no-map-gw` "to
  avoid direct access from container to host using the gateway address", and
  `-t none` / `-u none` when no host→container forwarding is configured, which
  "disable[s] automatic port forwarding based on bound ports"
  ([podman-run(1)]).

Two networking consequences worth naming now, because they shape the design:

1. **Host→container is off by default.** With pasta and no `--publish`, a
   container's listening ports are not reachable from the host. Any host-side
   channel that talks *to* the box (agent, service forwarding) needs either
   `--publish` (via rootlessport) or a different transport (§3.5).
2. **Low ports are unavailable rootless.** "Podman can not create containers that
   bind to ports < 1024"; the documented workaround is the
   `net.ipv4.ip_unprivileged_port_start` sysctl ([rootless.md]). This kills any
   plan to keep an in-container sshd on port 22 without a host configuration
   step — which is a strong argument for `podman exec` as the enter path (D6).

MTU is worth a line because pluto hit it before: slirp's default MTU of 65520
drops large TLS frames, which is why the current image pins `--mtu=1500`
(`images/boot.sh`, `images/README.md` "Rootless networking"). pasta's default is
also 65520, so the same discipline applies — podman documents
`pasta:--mtu,1500` as the example fix.

### 3.4 Per-run filesystem isolation

Container namespaces are created per container by the runtime, which is the
entire answer to "one run cannot see another's writes". The pieces pluto must
set explicitly, all one flag each:

| Property | Flag | Default to check |
|---|---|---|
| user namespace | `--userns` | rootless always uses one; `root` in the container maps to the invoking user ([podman-run(1)]) |
| pid limit | `--pids-limit` | `2048` "on systems that support 'pids' cgroup controller" ([podman-run(1)]); `containers.conf` default `pids_limit=1024` |
| read-only rootfs | `--read-only` | false by default; set it |
| no new privileges | `--security-opt no-new-privileges` | opt-in; "Disable container processes from gaining additional privileges through the `execve(2)` system call" |
| masked `/proc` | `--security-opt mask=…` / defaults | default mask list is `/proc/acpi`, `/proc/kcore`, `/proc/keys`, `/proc/latency_stats`, `/proc/sched_debug`, `/proc/scsi`, `/proc/timer_list`, `/proc/timer_stats`, `/sys/firmware`, `/sys/fs/selinux`, `/sys/devices/virtual/powercap` ([podman-run(1)]) |
| seccomp | default profile | `seccomp_profile="/usr/share/containers/seccomp.json"` ([containers.conf(5)]); never `seccomp=unconfined` |
| capabilities | default set | `default_capabilities` = CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL, NET_BIND_SERVICE, SETFCAP, SETGID, SETPCAP, SETUID, SYS_CHROOT — podman notes it runs "with less capabilities than Docker" ([containers.conf(5)]) |
| pivot_root | `no_pivot_root=false` | containers.conf default keeps `pivot_root` |
| cgroups | `--cgroup-parent`, `--cgroup-conf` | `cgroups="enabled"`, `cgroupns="private"` by default ([containers.conf(5)]) |

None of this is microVM-shaped and none of it is new work; it is flags. The
rootless limits are the honest cost: no `CAP_MKNOD`, so "Making device nodes
within a container fails, even when using privileged containers" ([rootless.md]),
and cgroupfs limits do not apply rootless — "`cgroupfs` doesn't work in Rootless
mode and containers cgroups aren't used" ([podman(1)]), which forces the systemd
cgroup manager and therefore cgroup delegation (§8).

### 3.5 The one real design decision: the agent transport

The agent protocol is transport-agnostic already: one JSON request line per
connection, an optional binary payload after it, one response line
(`internal/agent/proto.go:24-46`), and the client dials through a closure —
`dial: func() (net.Conn, error)` (`internal/agent/client.go:19-33`). The guest
listener is one line (`cmd/pluto-agent/main.go:31`, `vsock.Listen(agent.Port)`).
So the protocol and the runner-side `AgentClient` interface
(`internal/runner/handoff.go:17-27`) both survive; only the transport changes.
[repo]

Three candidate transports:

| | Mechanism | Mount needed? | Host port needed? | Cost |
|---|---|---|---|---|
| T1 | `podman exec` stdio, one-shot per request | no | no | one small agent mode (`--serve-stdio`); ~one process per call |
| T2 | unix socket in a per-run directory, bind-mounted | yes | no | a host bind mount into the box, which the hardening note's checklist item 8 tells us not to do |
| T3 | `--publish 127.0.0.1:<port>` + TCP | no | yes | bookkeeping; an extra rootlessport proxy hop per connection |

**Recommend T1 for the prototype.** It needs no host mount (so it does not
weaken the containment posture the hardening note argues for), no port
allocation, and no socket-path length worries. It is also the same primitive as
enter-on-failure, which is the whole point of the experiment. T3 is the upgrade
if per-call process spawns prove too slow for the sync path. [inference]

---

---

## 4. Density and DX: what the evidence says

### 4.1 Density, honestly

No primary source publishes a pluto-shaped density benchmark (N containers
versus M microVMs on one host running Go builds). The defensible claims, with
their sources:

- **The mechanism.** A container's footprint is process memory bounded by
  `memory.high`/`memory.max`; a microVM reserves guest RAM. `memory.high` is a
  soft limit where the kernel "reclaims aggressively (page cache first, then
  anonymous memory) before OOM-ing", and the hardening note already establishes
  that reclaim as the property Firecracker lacks
  ([cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html), §2.5 there).
  [docs]
- **The baseline cost per run.** crun's published figure is ~17 ms per container
  for `run /bin/true` (100 sequential runs, 1.69 s) against runc's ~33 ms
  ([crun README]) — [vendor claim], unverified, and measured for a trivial
  payload. Podman's own layer on top (conmon, logging, cgroup setup) is not in
  that number. Treat it as an order-of-magnitude input to the prototype's
  measurement plan, not as a result.
- **What pluto measures today.** The microVM path boots a stock guest in
  ~430 ms and reaches sshd in ~2.5 s on the host where it was measured
  (`docs/review-1-explainer.md` §6, `images/README.md` boot timings, 2026-10-04
  on kernel 6.1.186) [repo]. Both numbers are *cold boot including a service
  manager*, so a container that also runs a user systemd manager is not
  automatically 20× faster — it is faster, but the systemd-in-container cost has
  to be measured, not assumed.

**The honest position:** the density advantage is real and mechanism-level, but
its size for *pluto's* workload (a build, a test suite, maybe an agent) is
unknown, and the prototype is what turns it into a number. That is why §7 proposes
measurement before any migration decision, and it matches the hardening note's
revive trigger ("boxes per host exceeds what RAM allows, measured", §8 there).
[inference]

### 4.2 What the ecosystem buys

This is where the substrate stops being commodity and starts being a moat, and
it is the strongest argument for a container backend on *product* grounds.

**OCI images, and specifically the ones people already have.** A devcontainer is
"an existing local directory `path` storing the manifest, layer tarballs and
signatures as individual files" and registry images by digest
([podman-run(1)] transports). That means:

- `ghcr.io` is usable directly. It "stores container images within your
  organization or personal account", supports "Open Container Initiative (OCI)
  Specifications" alongside Docker Schema2, authenticates with `GITHUB_TOKEN`
  inside a workflow, and documents pull-by-digest so an image can be pinned
  ([Working with the Container registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)).
- Any image on Docker Hub is a candidate base. That is a materially different
  onboarding story from "build a bootable ext4 image with a pinned kernel".

**devcontainer interop, as an import direction.** The spec's reference lists the
properties pluto would need to honour: `image` or `build.dockerfile`,
`runArgs`, `mounts`, `containerEnv`/`remoteEnv`, `containerUser`/`remoteUser`,
`hostRequirements`, `overrideCommand`, `shutdownAction`, and the lifecycle
scripts `onCreateCommand` → `updateContentCommand` → `postCreateCommand` →
`postStartCommand` → `postAttachCommand`, ordered by `waitFor`
([devcontainer.json reference](https://containers.dev/implementors/json_reference/)).
pluto already has a close analogue of that lifecycle split in
`provision` / `wake` / `sessions`, and the earlier research note on guest
environments mapped exactly this (`docs/research/guest-environment.md` §5),
where a `devcontainer.json` mapper is parked in `docs/DEFERRED.md` with the
revive trigger "users ask". A container backend is what makes that trigger
cheap to pull. [inference]

**Caches.** pluto's environment layers are a *rootfs snapshot* cache. The
ecosystem's equivalent is registry-pushed image layers, which is how CI fan-out
shares a warm base across machines — the exact property #116 asks for
("a shared cache and shared environment layers so nobody re-provisions").
Where the semantics overlap, the GitHub Actions cache is the reference:
"A repository can have up to 10GB of caches", caches "not accessed within the
last week will also be evicted", and a cache is "scoped to the key, version, and
branch", with the default branch's cache available to other branches
([actions/cache README](https://github.com/actions/cache)). Fork PRs get
restore-only access ("A common case is a workflow triggered by a pull request
from a fork: such runs can **restore** existing caches but may not be permitted
to **save** new ones"). Those are the numbers and the policy a
pluto-side cache has to be honest about when it plugs in or replaces them.
[docs]

**Runner-in-container is the ecosystem's own answer to #120.** GitHub's workflow
syntax has `jobs.<job_id>.container` with `image`, `credentials`, `env`,
`ports`, `volumes`, and `options` — "Use `jobs.<job_id>.container` to create a
container to run any steps in a job that don't already specify a container" —
and notes that container jobs require a Linux runner with Docker installed
([Workflow syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax)).
Two caveats matter for pluto's translation layer, not for this ticket: the
`--network` and `--entrypoint` options are explicitly "not supported" in
`container.options`, and GHES/GHA-cache versioning couples the cache service to
the runner version. [docs]

### 4.3 What the ecosystem does not buy

- **No per-job container orchestration for free.** Running N jobs means N
  containers on one host with no scheduler beyond what pluto already has
  (`MaxRunningBoxes`, the queue in `internal/state/queue.go`). Nothing in the
  container ecosystem helps at this scale, and Kubernetes-shaped answers are
  explicitly out of scope. [inference]
- **No density magic from images alone.** Every warm base still costs disk, and
  pull costs are paid once per host; the shared cache across machines is what
  makes it pay off, which is #121's territory. [inference]
- **Reproducibility changes shape, it does not improve.** Today's artifact is
  hash-locked: `manifest.json` content-addressing, `SOURCE_DATE_EPOCH` clamping,
  a fixed ext4 UUID and hash seed, and a byte-identical double-build asserted by
  a host test (`images/README.md` "Reproducibility"). An OCI image is
  content-addressed by digest, which is *better* for pinning, but the
  byte-identical-build recipe does not carry over unchanged and has not been
  re-established here. [uncertain — needs its own spike]

---

## 5. What survives of the current code

Read against the tree on 2026-10-09. "Survives" means unchanged or
mechanically adapted; the count is a rough line estimate, not a measurement.

| Layer | Fate | Notes |
|---|---|---|
| `internal/api`, `internal/state` (Box, Job, Queue, Task, Schedule, Trust) | **survives** | substance-free of substrate; `Box.Image` gains a second *kind* |
| `internal/daemon` (HTTP surface, scheduler, auto-pause, webhooks, queue) | **survives** | `BoxRunner` unchanged; tests already fake it |
| `internal/contract` | **survives** | `[box].image` semantics widen |
| `internal/cli` (grammar, status, run, logs, jobs) | **survives** | except attach (§5.1) |
| `internal/envcache` policy (key, trust, claim/publish) | **survives** | mechanism replaced by layers |
| `internal/agent` protocol (`proto.go`), `AgentClient` interface | **survives** | transport changes (§3.5) |
| `internal/agent/system.go` (systemd user units, jobs, services, sessions, journalctl) | **survives if the container runs systemd** | the single highest-value reuse |
| `internal/runner` lifecycle shape (`Up`/`Pause`/`Destroy`/`Reconcile` control flow, layer claims, `handoff` orchestration) | **survives as a shape** | the Firecracker-specific halves do not |
| `internal/vsock`, `internal/runner/boxrun.go`, `fcapi.go`, `metrics.go`, `disk.go`, `envscrub.go` | **dies for the container backend** | retained for the microVM backend; both coexist |
| `internal/imagebuilder` (Containerfile, pins, assemble) | **splits** | same pins; OCI build for containers, ext4+mkfs for microVMs |
| `internal/systemd` (box unit, cgroup drop-in) | **survives for microVM; replaced by quadlet for containers** | see §5.2 |

[inference] The single most valuable fact in this table: the highest-complexity,
highest-risk code in the repo (`agent/system.go`'s ~880 lines of unit rendering
and supervision, plus the contract phases it drives) is reusable inside a
container because podman can run systemd in one. Everything that dies is
Firecracker-facing plumbing.

### 5.1 Two attachment points that need a decision, not a rewrite

1. **Client attach.** `api.AttachInfo` + `cli/attach.go:121-137` hard-code ssh.
   Adding an optional enter-argv (or an `enter` endpoint) keeps ssh for the
   microVM backend and `podman exec -it` for containers. This is a small change
   with a wide blast radius in tests. [repo]
2. **Host service forwarding.** `runner.ForwardService` (`ingress.go:18-46`)
   shells out to the OpenSSH client and builds a per-connection tunnel over
   vsock. For containers the equivalent is a loopback listener bridged to a
   published container port; the *shape* (one listener, one bridge, a cleanup
   func) is reusable and the daemon's optional-interface assertion already
   accommodates a different implementation. [inference]

### 5.2 systemd: keep the unit, or use quadlet

Today each box is a `pluto-box@<id>.service` whose main process is
`pluto box run <id>` (`internal/systemd/systemd.go:50-60`), and cgroup limits
are a per-instance drop-in (`runner.go:904-938`). Podman publishes
[quadlet](https://docs.podman.io/en/latest/markdown/podman-quadlet.1.html)
("Allows users to manage Quadlets", which generate systemd units) and
`podman-systemd.unit(5)` for exactly this shape. Two viable paths:

- **(a) keep `pluto-box@.service`** and let the container be a child process of
  that unit (`podman run` from `BoxRun`'s successor). Least new machinery; cgroup
  delegation stays where it is today.
- **(b) write a `.container` quadlet per run** and let systemd drive podman.
  Less pluto code, more reliance on podman's unit generation, and a different
  restart/reconcile story (`Reconcile` reads `IsActive` today).

Recommend (a) for the prototype: it keeps `Reconcile`, `Destroy`, and the cgroup
drop-in path identical, which is what makes the experiment cheap. [inference]

---

## 6. Prototype sketch: the smallest experiment that answers the question

**The question:** can a run spin up, execute a job, and be entered on failure,
behind the existing seam, with a container doing the work?

**Design constraints for the prototype:** one flag, one code path, no
abstractions invented before they are needed, and it must be possible to delete
without touching the microVM backend.

### 6.1 Shape

- New package `internal/runner/container` (or `internal/containerrunner`)
  implementing `daemon.BoxRunner` plus the optional `boxServiceForwarder`-style
  assertion if service forwarding is attempted at all.
- One selection point: `internal/cli/host.go:56` becomes a backend choice
  (`--runner microvm|container`), and `ReconcileAll` moves behind an assertion
  (`interface{ ReconcileAll() }`) so the one non-interface call at `host.go:57`
  keeps working.
- The agent stays where it is. Two small changes: `pluto-agent` gains a
  stdio one-shot mode (serve a single request per invocation over stdin/stdout,
  reusing `Agent.handle`), and `agent.NewClient` gains a sibling constructor that
  dials by exec'ing `podman exec` into the container. `AgentClient`
  (`handoff.go:17-27`) is unchanged.
- The image gains an OCI variant built from the *same* `images/pins.yaml` and
  `images/Containerfile`, minus the vsock/slirp/ctrl-alt-del lines
  (`ssh.socket`'s `ListenStream=vsock::22`, the `10.0.2.x` networkd config, the
  `ctrl-alt-del.target → reboot.target` pin).

### 6.2 The five operations

| Operation | Implementation | Reuses today |
|---|---|---|
| `Up` | `podman run --detach --replace --cidfile <boxDir>/cid --conmon-pidfile … image pluto-agent --serve-stdio` (plus `--read-only`? no — the agent writes state; `--read-only-tmpfs` for /tmp,/run), then wait for the agent's first `ping` instead of `WaitReady(vsock)` | `Up`'s control flow, `Store.Transition`, layer claims |
| `RunJob` | agent `run` over T1 transport; job output teed to the host-side log exactly as `runner/job.go` does | `job.go` verbatim, `state.Job` |
| `Attach` | `podman exec -it <cid> tmux attach-session -t <session>` (or a shell) | `AttachInfo` + a new `Enter` field |
| `Destroy` | `podman rm -f <cid>`, then `Store.DestroyBox` | `Destroy`'s control flow |
| `Reconcile` | `podman inspect -f '{{.State.Status}}' <cid>` mapped to live/failed/absent | `Reconcile`'s shape |

`Pause` for the prototype: not implemented — `podman stop` plus keeping the
container is the obvious mapping, but a paused container's filesystem is the
wrong shape for the current `CleanStop`/publish-layer path, and getting it wrong
would contaminate the measurement. Say so explicitly in the experiment.

### 6.3 What it must not do

- No changes to `daemon.BoxRunner`.
- No new state fields beyond what a container backend needs to find its
  container (`cid`).
- No attempt at the durable-dev-box story, cross-host resume, or shared cache
  (#118, #121).
- No OCI image *build* pipeline rework beyond the one Containerfile split; the
  spike is about the runner, not about reproducible OCI builds.

### 6.4 Measurements (the point of the exercise)

1. `Up` → agent-ready latency, cold image and warm image, `--systemd=always` on
   and off. Compare with the microVM path's ready numbers.
2. `RunJob` end-to-end for a real job, including the first-sync git bundle.
3. Enter-on-failure: time to `podman exec` after a failing job, and whether the
   container's filesystem still holds the failure state.
4. Density sweep: 1, 4, 16, 50 concurrent short runs. Record per-container
   `memory.current`, host free memory, start failures, and whether
   fuse-overlayfs is in play (kernel < 5.12).
5. Cleanup cost: `podman rm` time and graphroot growth over the sweep.

**Acceptance:** the run spins up, the job runs and its output streams, a failing
run can be entered, and everything is destroyed without leaking a container or a
file in the state dir. If any of those fails, the answer to #119's question is
"not behind this seam, and here is where it broke".

---

## 7. Decisions proposed

**D1 — The seam stays `daemon.BoxRunner`; narrow, not wide.** The interface
already contains exactly what the daemon needs and nothing substrate-shaped
[repo, verified]. Capabilities beyond it (`ReconcileAll`, `ForwardService`,
`AdvancePRRef`) continue through optional-interface assertions, which is the
existing pattern. Widening the interface for the container case would couple
every future backend to the union of all backends' needs.

**D2 — Container backend runs on rootless podman + crun, driven through the
podman CLI.** Reasons: the ecosystem provides the storage driver, namespaces,
cgroups, logs, exec, and registry auth; raw namespaces at this scale means
re-implementing those for no product gain. Cost accepted: documented host
prerequisites (subuid/subgid, pasta, kernel ≥ 5.12 for native overlayfs) that
are the container analogue of today's `/dev/kvm` and `slirp4netns`. Fallback:
if those prerequisites prove too much for self-hosters, the raw-namespace path
shares the entire runner side and is the documented escape hatch, not a rewrite.

**D3 — The agent survives; only its transport changes.** `agent.Client`'s dial
closure and `pluto-agent`'s one-line listener make the change small [repo,
verified]. Prototype transport = `podman exec` stdio (T1): no host mount, so the
hardening checklist's "no host bind-mounts" item stays satisfied; no port
bookkeeping; and the enter path is the same primitive. T3 (published port) is
the upgrade path if spawn-per-call is too slow.

**D4 — One image source, two artifact shapes.** The pins and Containerfile
already produce the environment; the OCI path is a subset of the current
pipeline (no kernel, no Firecracker, no mkfs). Split the microVM-only lines
(vsock socket activation, slirp networkd config, `ctrl-alt-del → reboot`) out of
the shared Containerfile. Accept that OCI digest pinning is the new
reproducibility contract and that byte-identical builds need their own spike
[uncertain].

**D5 — Ephemeral by construction.** One container per run from a warm base,
`--rm`/explicit removal on destroy, host-side records as the durable thing.
The existing split (host store durable, box ephemeral, environment layers as the
warm bridge) is the right shape and survives; only the layer mechanism changes
from ext4+debugfs to image layers.

**D6 — Enter is `podman exec`, not ssh.** Rootless containers cannot bind port
22 [docs], so an in-container sshd needs a host sysctl change. Add an
enter-argv to `api.AttachInfo` and let `pluto attach` prefer it when present.
Sessions keep working because they are tmux inside the box, and `podman exec`
can attach to the tmux session directly.

**D7 — Build one backend and measure; do not generalize.** One flag, one
container type, the measurements in §6.4. The abstraction question ("container
plus optional microVM tier") is decided by those numbers, not before them.

**What this is not:** a decision to drop microVMs. The current product stays on
microVMs (ADR 0001 stands, and the hardening note's cheap items — chroot,
`no_new_privs`, delegated cgroups — are still worth doing). This note argues
only that a container backend is *feasible behind the existing seam* and that
the feasibility question is answerable with one prototype.

---

## 8. Open questions

1. **Cgroup delegation rootless, for real.** `cgroupfs` "doesn't work in Rootless
   mode" [docs], so the container backend needs the systemd cgroup manager with
   delegation — the same host-dependent problem the microVM path already has
   (hardening §2.5, §3.6; ADR 0001 R2). Unverified on any target host here.
2. **The auto-pause busy signal inside a container.** It reads cgroup v2 counters
   from *inside* the box (`system.go:543-619`). With a private cgroup namespace,
   whether those files are visible inside the container is untested [uncertain].
   The safe fallback already exists in the daemon: an unreadable reading is
   "unknown — never idle" (`daemon/autopause.go:86-92`), so a container box would
   simply never auto-pause until someone fixes the counter. Cheap to test, must
   be tested.
3. **Is podman on the host acceptable?** It is a real prerequisite addition for
   self-hosters (subuid/subgid, pasta, a modern kernel). The moonshot's scope
   says yes; the demo and docs have to say it plainly. [inference]
4. **OCI reproducibility.** Byte-identical OCI builds, layer pinning by digest,
   and whether `SOURCE_DATE_EPOCH`-style clamping applies to image builds. Own
   spike. [uncertain]
5. **Two `image` vocabularies.** The contract, the glossary, and `pluto image ls`
   all mean "bootable kernel+rootfs" today. A container reference is a different
   kind of thing. GLOSSARY and ADR 0001 work, parked for #118/#122.
6. **Podman version skew.** The rootless notes describe behaviour "As of Podman
   5.0" and the pasta default; a minimum podman version is an unstated
   prerequisite. [uncertain]
7. **Density, for real.** Nothing published measures pluto's workload. §6.4 is
   the answer, and it is the number that decides "container plus optional
   microVM tier" versus "container-only".

---

## 9. Sources

All accessed 2026-10-09.

1. podman(1) — https://docs.podman.io/en/latest/markdown/podman.1.html — rootless defaults, subuid/subgid, pasta requirement, overlayfs/5.12.9 note, `--transient-store`, `--cgroup-manager`, crun default runtime
2. podman-run(1) — https://docs.podman.io/en/latest/markdown/podman-run.1.html — `--userns`, `--pids-limit`, `--read-only`, `--read-only-tmpfs`, `--replace`, `--rm`, `--security-opt` (no-new-privileges, mask list), `--systemd`, `--network` pasta defaults, `--transient-store`
3. podman rootless notes — https://github.com/containers/podman/blob/main/rootless.md — rootless shortcomings: <1024 ports, pasta-as-default since 5.0, subuid failures, no mknod, no NFS/parallel FS, overlayfs ≥5.12
4. containers.conf(5) — https://github.com/containers/common/blob/main/docs/containers.conf.5.md — defaults: seccomp profile, capability set, `pids_limit`, `cgroups`, `cgroupns`, `default_rootless_network_cmd="pasta"`, `no_pivot_root=false`
5. crun README — https://github.com/containers/crun — C runtime, "faster than runc and has a much lower memory footprint", 100-container benchmark **[vendor claim]**
6. passt(1)/pasta(1) — https://passt.top/builds/latest/web/passt.1.html — user-mode networking for netns, no full TCP stack, MTU 65520, low-port binding notes
7. overlayfs documentation — https://docs.kernel.org/filesystems/overlayfs.html — upper/lower, multiple lower layers, `userxattr` for unprivileged mounting, `volatile` mount
8. cgroup v2 documentation — https://docs.kernel.org/admin-guide/cgroup-v2.html — `memory.max` vs `memory.high`, `pids.max`, delegation
9. OCI Runtime Specification — https://github.com/opencontainers/runtime-spec — seccomp, capabilities, maskedPaths, NoNewPrivileges (also cited by the hardening note)
10. devcontainer.json reference — https://containers.dev/implementors/json_reference/ — `image`, `build`, `runArgs`, `mounts`, lifecycle scripts and `waitFor`, `hostRequirements`
11. GitHub: Working with the Container registry — https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry — OCI support, `GITHUB_TOKEN`, pull by digest, 10 GB layer / 10-minute upload limits
12. actions/cache README — https://github.com/actions/cache — 10 GB per repo, week-access eviction, key/version/branch scoping, fork restore-only, v2 cache service
13. GitHub: Workflow syntax for GitHub Actions — https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax — `jobs.<job_id>.container{image,credentials,env,ports,volumes,options}`, Linux+Docker requirement, `--network`/`--entrypoint` unsupported
14. podman-quadlet(1) / podman-systemd.unit(5) — https://docs.podman.io/en/latest/markdown/podman-quadlet.1.html — systemd unit generation for containers
15. Internal: `docs/research/container-and-microvm-hardening.md` — the security baseline and 15-item container checklist (assumed, not repeated)
16. Internal: `docs/research/devex-and-comparables.md`, `docs/review-1-explainer.md` §6, `docs/research/guest-environment.md` §5, `docs/DEFERRED.md`, `docs/contract.md`, `images/README.md`, `images/Containerfile`
17. Internal code read 2026-10-09: `internal/daemon/daemon.go`, `internal/daemon/schedule.go`, `internal/runner/{runner,boxrun,handoff,job,disk,envscrub,metrics,fcapi,environment_cache,ingress,autopause}.go`, `internal/agent/{system,client,proto}.go`, `cmd/pluto-agent/main.go`, `internal/vsock/{vsock,listen}.go`, `internal/cli/{host,attach}.go`, `internal/api/api.go`, `internal/state/state.go`, `internal/contract/contract.go`, `internal/envcache/cache.go`, `internal/systemd/systemd.go`

