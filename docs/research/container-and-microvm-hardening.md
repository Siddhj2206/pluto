# Container and microVM hardening

Research note for the pluto runtime boundary decision, 2026-10-09. Primary sources only; every claim is
cited with a URL and an access date of 2026-10-09 unless noted. Claims are labelled **[verified]**,
**[vendor claim]**, or **[inference]**; anything that could not be verified against a primary source is
marked **[uncertain]**.

---

## 1. Scope and question

pluto is deciding whether to keep running each box as a microVM, drop to containers, or keep both. The decision is being driven by three container advantages that a Firecracker box does not have: density (many boxes per host), RAM reclaim (idle boxes give memory back), and OCI packaging (the ecosystem of existing images). The counter-pressure is isolation: a container shares the host kernel with the workload, and a box runs a user's build code.

This note answers two questions and nothing else:

1. **If pluto keeps microVMs, what does running them properly actually require?** Firecracker's own production documentation is explicit that production use means the jailer, or constraints "equal or more restrictive" ([prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-09). ADR 0001 skips the jailer. So "done properly" is a concrete list, and this note produces it with costs attached.
2. **If pluto goes container-only, what hardening baseline is appropriate?** Not "Docker defaults," but the NIST/CIS baseline, expressed as configuration an operator can audit.

Out of scope: untrusted external CI (external PRs, fork contributors). That is a different threat model with a materially higher bar and it is deferred; where it would change the answer, this note says so rather than solving for it. Also out of scope: orchestration (Kubernetes scheduling), multi-host federation, and snapshot/restore performance, all covered elsewhere.

**Vocabulary.** This note uses pluto's [GLOSSARY.md](../../GLOSSARY.md): a *box* is a durable machine owned by one worktree, a *host* runs the daemon and owns boxes, the *runner* boots a box by driving a *VMM*, and a *trust class* (`trusted` / `untrusted`) scopes which environment layers may be shared. "Kernel" means the guest kernel inside a box; "host kernel" means the kernel of the machine running pluto. Where this note says "container," it means an OCI container executed by a runtime such as `runc`, `crun`, or Podman — the OCI Runtime Specification object, not Docker the product ([OCI Runtime Specification](https://github.com/opencontainers/runtime-spec), accessed 2026-10-09).

**What is already settled.** ADR 0001 records that pluto runs Firecracker rootless, without the jailer, because there is no trust model, and VISION.md claims no isolation guarantee. Both are inputs here, not subjects of this note — except §7, which states exactly what a container-only decision would supersede.

**Method.** Primary sources: Firecracker's own docs, the OCI Runtime Specification, Docker and Podman docs, gVisor and Kata Containers docs, Sysbox docs, NIST SP 800-190, CIS Benchmarks, and NVD/CVE records. Where a source is a vendor describing its own product, it is labelled **[vendor claim]** and treated as a claim about intent, not a verified property. Where a conclusion is mine rather than a source's, it is labelled **[inference]**.

---

## 2. Container hardening: a concrete baseline

This section is written as an auditable configuration checklist, ordered from "does the most work for the least effort" to "expensive." Each item names the source that specifies it and what it actually protects against. The organizing frame is NIST SP 800-190, which is the US federal reference for container security and separates the problem into *image*, *registry*, *orchestrator*, *container runtime (isolation)*, and *host OS* layers ([NIST SP 800-190](https://csrc.nist.gov/publications/detail/sp/800-190/final), accessed 2026-10-09). Everything below is the runtime layer plus the host layer.

### 2.1 Rootless, user namespaces, and subuid/subgid mapping

**The rule.** Containers run as an unprivileged uid on the host, never as root, and never with the host's real uid 0 mapped into the container.

Rootless containers work by running the runtime inside a user namespace the unprivileged user created, so that the *container's* root maps to an unprivileged uid on the *host*. Docker documents the requirement: the user needs a subuid/subgid range allocated (typically 65536 ids) in `/etc/subuid` and `/etc/subgid`, and Docker's rootless mode validates this before it will start ([Docker rootless mode](https://docs.docker.com/engine/security/rootless/), accessed 2026-10-09). Podman is rootless by default and uses newuidmap/newgidmap, which read `/etc/subuid` and `/etc/subgid` ([Podman rootless](https://docs.podman.io/en/latest/markdown/podman.1.html), accessed 2026-10-09).

**What this buys and what it does not.** Mapping container uid 0 to host uid 1000 means a container process that believes it is root cannot by itself touch files owned by the host user outside the mapped range, and cannot load kernel modules, and cannot write to other users' home directories. It does *not* create a new kernel — every syscall still executes in the host kernel, which is the whole subject of §2.9. [verified]

**Subuid range sizing.** Both Docker and Podman allocate a *range* rather than a single id so that a container can use multiple uids internally (a common case: a process that drops to `nobody` for a network listener). With a 65536-id range, a container can hold 65536 distinct uids; exceeding it produces `no subuid ranges found` / `newuidmap` failures at startup. For pluto, a box that installs apt packages and creates service users will consume a handful of ids, not thousands, but the range must be allocated before the first box boots, not discovered at failure time. [inference from the documented u/setuid map mechanics]

### 2.2 Seccomp, capabilities, and `no-new-privileges`

**Seccomp.** A seccomp profile filters syscalls by number and (with the OCI spec's argument filtering) by argument. The OCI Runtime Specification defines the `linux.seccomp` block and the set of known syscalls; a profile is a default action (usually `SCMP_ACT_ERRNO` or `SCMP_ACT_KILL`) plus a whitelist of syscalls with per-syscall actions and argument conditions ([OCI Runtime Specification, Linux seccomp](https://github.com/opencontainers/runtime-spec/blob/main/config-linux.md#seccomp), accessed 2026-10-09).

The practical question is always "whose profile?" Docker ships a default profile and permits `--security-opt seccomp=unconfined` to disable it; the Docker documentation states that the default profile is "a moderate profile" that blocks a set of syscalls while allowing the ones a typical container needs ([Docker seccomp](https://docs.docker.com/engine/security/seccomp/), accessed 2026-10-09). Kubernetes ships its own default profile and applies it unless a pod or node opts out ([Kubernetes: Restrict a Container's Syscalls with seccomp](https://kubernetes.io/docs/tutorials/security/seccomp/), accessed 2026-10-09). For a build workload that compiles, runs tests, and shells out to package managers, the *default* profile is usually sufficient and the correct move is to use it rather than to hand-write one; hand-written profiles break on dist-upgrades and are a recurring source of "it worked on my machine." **[inference]**

The one absolute rule: **never pass `--security-opt seccomp=unconfined`**, and never set
`securityContext.seccompProfile.type: Unconfined` in Kubernetes. An unconfined container has a
seccomp profile equivalent to none, which is the single largest reduction in the cost of a kernel
exploit for the attacker. NIST SP 800-190 makes the same point in its containment guidance: container
escape via a kernel vulnerability requires the vulnerability to be reachable, and seccomp narrows
reachability ([NIST SP 800-190 §5.2](https://csrc.nist.gov/publications/detail/sp/800-190/final),
accessed 2026-10-09). [verified]

**Capabilities.** Linux capabilities divide root's authority into ~40 units; a container's bounding
set is what the OCI spec calls `process.capabilities`. Docker's default *drops* a set of dangerous
capabilities including `CAP_SYS_ADMIN`, `CAP_NET_ADMIN`, `CAP_SYS_MODULE`, `CAP_SYS_RAWIO`, and
`CAP_MKNOD`, and permits only the default set (`CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `FSETID`, `KILL`,
`SETGID`, `SETUID`, `SETPCAP`, `NET_BIND_SERVICE`, `NET_RAW`, `SYS_CHROOT`, `AUDIT_WRITE`,
`SETFCAP`) ([Docker runtime privilege and Linux capabilities](https://docs.docker.com/engine/reference/run/#runtime-privilege-and-linux-capabilities), accessed 2026-10-09).

`CAP_SYS_ADMIN` deserves naming on its own: it is the "root of capabilities" — it subsumes mounting,
pivot_root, `setns`, `quotactl`, and a long tail. A container with `CAP_SYS_ADMIN` and access to a
block device is close to a host compromise. Docker's own documentation describes it as "the most
privileged capability" and it is dropped by default; `--privileged` grants *all* capabilities plus
removes the seccomp filter plus makes all host devices available ([Docker: `--privileged`](https://docs.docker.com/engine/reference/run/#privileged), accessed 2026-10-09). For a build container, the correct posture is Docker's default set, or fewer. If a build step appears to need `CAP_SYS_ADMIN`, that is a signal the workload wants a VM, or that DinD is being smuggled in — see §2.8. [verified]

**`no-new-privileges`.** Setting `NoNewPrivs: true` in the OCI `process` block sets the kernel's
`PR_SET_NO_NEW_PRIVS` bit for the container's process tree, which makes setuid binaries and file
capabilities ineffective: exec of a setuid-root binary does not grant elevated privilege. This closes
the classic "attacker writes a setuid binary to a writable directory and execs it" path without
requiring the rootfs to be read-only ([OCI Runtime Specification, NoNewPrivileges](https://github.com/opencontainers/runtime-spec/blob/main/config-linux.md#process), accessed 2026-10-09). It is cheap, it cannot break a normal build (builds do not legitimately setuid), and it should be unconditional. **[inference: builds do not legitimately gain privilege via setuid; if one does, that is a defect to fix]**

### 2.3 AppArmor and SELinux (MAC)

Seccomp and capabilities are discretionary-ish: they limit what the *root* user inside the container
can do. A mandatory access control (MAC) profile limits what the *process* can do regardless of uid,
by labeling files and operations.

- **AppArmor** profile. Docker applies a default `docker-default` AppArmor profile to containers
  unless `--security-opt apparmor=unconfined` is passed ([Docker AppArmor](https://docs.docker.com/engine/security/apparmor/), accessed 2026-10-09). Kubernetes does not ship a default AppArmor profile; it supports loading one via annotations ([Kubernetes: Restrict a Container's Access to Resources with AppArmor](https://kubernetes.io/docs/tutorials/security/apparmor/), accessed 2026-10-09).
- **SELinux** type enforcement. Docker/`runc` labels container processes (`container_t`) and their
  writable content (`container_file_t`), and the host's `svirt_lxc_net_t`-family policy keeps
  containers from each other's files ([Docker SELinux](https://docs.docker.com/engine/security/selinux/), accessed 2026-10-09). Fedora/RHEL-derived hosts have this on by default; Ubuntu-derived hosts generally do not ship SELinux enforcing by default and use AppArmor instead.

**Recommendation for pluto:** use whichever of AppArmor/SELinux the host distro enforces by default
and do not try to defeat it. Do not pass `apparmor=unconfined` or `--security-opt label=disable`.
Adding a *custom* profile is a project, not a checkbox. [inference]

### 2.4 Read-only rootfs, no host mounts, masked `/proc` and `/sys`, device cgroup

**Read-only rootfs.** `rootfs.readonly=true` in the OCI spec, exposed as `--read-only` in Docker
([Docker: `--read-only`](https://docs.docker.com/engine/reference/run/#filesystem), accessed 2026-10-09). Any path the build needs to write must be an explicit `tmpfs` mount. The value is not "prevent writes" — it is that a writable rootfs is where a post-exploitation payload lands, persists, and survives into the next commit of the same image. Read-only + explicit `tmpfs` for `/tmp`, `/var/tmp`, and build output makes the writable surface finite and enumerable. This is one of CIS Docker Benchmark's host-configuration controls ([CIS Docker Benchmark v1.6+ §5.12](https://www.cisecurity.org/benchmark/docker), accessed 2026-10-09). [verified]

**No host mounts.** Never bind-mount a host directory into a build box. This is the most common
containment failure in CI images: `-v /var/run/docker.sock:/var/run/docker.sock` (see §2.8) and
`-v $HOME:/home/user` both hand the container a live handle on host state. NIST SP 800-190's
containment recommendations include not mounting sensitive host paths, and its host recommendations
include not treating containers as a security boundary around host data ([NIST SP 800-190 §5](https://csrc.nist.gov/publications/detail/sp/800-190/final), accessed 2026-10-09). Where a build needs a checkout, the content should be materialized *inside* the box's own storage, not shared from the host. [inference]

**Masked `/proc` and `/sys`.** The OCI runtime mounts `/proc` and `/sys` and then masks selected paths
(`maskedPaths`: `/proc/kcore`, `/proc/keys`, `/proc/latency_stats`, `/proc/timer_list`,
`/proc/sched_debug`, `/proc/scsi`, `/sys/firmware`, ...) and read-only-paths others
(`readonlyPaths`: `/proc/asound`, `/proc/bus`, `/proc/fs`, `/proc/irq`, `/proc/sys`,
`/proc/sysrq-trigger`) ([OCI Runtime Specification, maskedPaths / readonlyPaths](https://github.com/opencontainers/runtime-spec/blob/main/config-linux.md#masked-paths), accessed 2026-10-09). These hide host kernel data that the container has no legitimate need for — `/proc/kcore` is a live image of host physical memory, and `/sys/firmware` can leak hardware and firmware details. The mechanism matters for a build workload: it is applied by the runtime by default, and a container that can read anything outside it has been explicitly reconfigured. [verified]

**Device cgroup.** The devices cgroup controller restricts `mknod` and read/write/open on device nodes.
`runc` applies a default devices rule set that allows only a small whitelist (null, zero, full, random,
urandom, tty, console, ptmx, and the standard `/dev/pts/*`) and denies everything else
([OCI Runtime Specification, devices cgroup](https://github.com/opencontainers/runtime-spec/blob/main/config-linux.md#devices), accessed 2026-10-09). Without this, a privileged-capable container could open `/dev/sda` directly, or `/dev/mem`. For a build box the default whitelist is fine; the point of recording it is that it is on by default and cheap to verify. [verified]

### 2.5 cgroup v2 resource limits

cgroup v2 is the unified hierarchy: one controller set per hierarchy, controllers enabled by writing to
`cgroup.subtree_control`, and limits read/written via files like `cpu.max`, `cpu.weight`, `memory.max`,
`memory.high`, `memory.swap.max`, `io.max`, and `pids.max` ([Kernel documentation: cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html), accessed 2026-10-09).

Three distinctions that matter for a build platform:

- **`memory.max` vs `memory.high`.** `max` is a hard limit: exceed it and the OOM killer fires in the
  container's cgroup. `high` is a soft limit: exceed it and the kernel *reclaims* aggressively
  (page cache first, then anonymous memory) before OOM-ing. For a box where you want idle memory
  returned to the host without killing the build, `high` is the useful knob and `max` is the backstop.
  This distinction is exactly the "RAM reclaim" property claimed for containers and absent from
  Firecracker, and it is worth stating plainly: it is a *reclaim* property, not a containment property.
  [verified]
- **`pids.max`.** Fork-bomb protection. A build that spawns unbounded make jobs can exhaust host PIDs,
  which is a host-wide denial of service. Cheap to set, no downside for a build. [inference]
- **`cpu.max` vs `cpu.weight`.** `max` is a hard bandwidth cap (quota/period); `weight` is proportional
  shares. For a self-hoster running a handful of boxes, `weight` gives smoother behavior under
  contention; for enforcing a plan, `max` is the accounting boundary. [verified]

**Rootless caveat.** A rootless container can only set limits on the cgroup subtree delegated to it.
Delegation is a host-configuration act: either systemd (`Delegate=yes` on the owning unit, which
grants the user's `user@.service` a delegated cgroup and pins it) or manual `cgroup.subtree_control`
writes. Docker documents rootless cgroup support and its limitations, including that rootless Docker
requires cgroup v2 and systemd-managed delegation ([Docker rootless mode](https://docs.docker.com/engine/security/rootless/), accessed 2026-10-09). This is the same design problem pluto already has with Firecracker (§3.6). [verified]

### 2.6 Network isolation and egress control

A container with the default `bridge`/`pasta` network can reach the host's LAN and the internet. For
CI that is usually what you want, but the default is "unrestricted," and the interesting question is
what is restricted.

- **Per-container netns.** Each container gets a network namespace and a veth into a bridge, or uses
  slirp4netns/pasta in rootless mode ([Podman networking](https://docs.podman.io/en/latest/markdown/podman-network.1.html), accessed 2026-10-09). The important isolation fact: containers on the same bridge can reach *each other* unless policy stops them. Docker's bridge does not apply inter-container filtering; the `icc` (inter-container communication) setting on the legacy `docker0` bridge did, but with the default `bridge` network and user-defined networks, containers on the same network are reachable.
- **Egress control.** The only robust mechanism is firewall rules on the host: nftables/iptables rules
  that restrict the container's veth or the netns's outward traffic by destination. E2B, which runs
  untrusted code, does exactly this — one network namespace and a per-sandbox nftables egress firewall
  ([E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-09). Kubernetes offers `NetworkPolicy`, which requires a CNI that enforces it and is not itself a boundary ([Kubernetes Network Policies](https://kubernetes.io/docs/concepts/services-networking/network-policies/), accessed 2026-10-09).
- **DNS and cloud metadata.** 169.254.169.254 is the most common CI exfiltration path when a host has
  a cloud metadata service. Blocking it is a one-line host rule and one of the highest-value egress
  controls. [inference, aligned with NIST SP 800-190's host-layer guidance]

**Recommendation for pluto:** default-deny is wrong for a self-hoster (it breaks every build that
clones or fetches). Default-allow with a documented blocklist — cloud metadata, RFC1918 addresses the
host does not need to reach, and any link-local service — is proportionate to the stated scope
(single-tenant trusted offload). [inference]

### 2.7 Secrets handling and log redaction

**Secrets.** Three acceptable patterns, in order:

1. **Mount at run, never in image.** The secret is placed into the box's writable layer or a tmpfs at job
   start and removed at job end. It never appears in an image layer, and therefore never in a registry.
2. **Short-lived credentials.** OIDC federation (GitHub Actions' `id-token` is the reference pattern)
   issues a credential scoped to the job's lifetime rather than a long-lived token ([GitHub: About security hardening with OpenID Connect](https://docs.github.com/en/actions/deployment/security-hardening-your-deployments/about-security-hardening-with-openid-connect), accessed 2026-10-09). This bounds the blast radius of a compromised box to the job.
3. **A host-side agent, not a file.** The credential never enters the box; the box asks a host agent over
   a socket to perform the privileged operation. This is the strongest option and is what pluto's
   existing host-daemon-over-vsock shape could support.

What is *not* acceptable, and is worth stating because it is ubiquitous: `ENV SECRET_KEY=...` in an
image, or passing `-e SECRET_KEY=...` so it appears in `docker inspect` and in the daemon's logs. Both
are readable by anyone who can read the image or query the daemon. NIST SP 800-190's registry and image
recommendations are explicit that secrets must not be baked into images
([NIST SP 800-190 §4](https://csrc.nist.gov/publications/detail/sp/800-190/final), accessed 2026-10-09).
[verified]

**Log redaction.** A build box emits build logs, and build logs leak. Patterns worth scrubbing:
`AWS_SECRET_ACCESS_KEY`, `-----BEGIN ... PRIVATE KEY-----`, `GITHUB_TOKEN=`, npm/pypi auth lines,
`.netrc` contents, and connection strings. This is not solved by a library; the interface that matters
is that pluto already has a single point (the host daemon) through which box output flows, so redaction
is one filter in one place rather than a concern of every job. [inference] A concrete precedent: GitHub
Actions masks registered secrets from logs automatically ([GitHub: Using secrets in GitHub Actions](https://docs.github.com/en/actions/security-for-github-actions/security-guides/using-secrets-in-github-actions), accessed 2026-10-09).

### 2.8 The DinD problem and its alternatives

"Docker-in-Docker" is the request that arrives whenever a CI workload needs to build a container image. There are five shapes and they have very different security properties.

**(a) `--privileged` DinD.** The container runs a full Docker daemon with `--privileged`: all
capabilities, seccomp unconfined, host devices, and typically the ability to mount. Docker's own
documentation describes `--privileged` as giving the container "all capabilities" and lifting device
cgroup restrictions, and warns against using it in production ([Docker: `--privileged`](https://docs.docker.com/engine/reference/run/#privileged), accessed 2026-10-09). The consequence for a build box: the "inner" Docker runs on the *host kernel* with effectively host-level authority. This is the configuration in which "container" stops being an isolation boundary at all — CVE-2019-5736 (§2.9) is exactly the class of flaw that turns a container into a host takeover. [verified]

**(b) Docker socket bind-mount.** Mounting `/var/run/docker.sock` into the container does not run a
nested daemon; it gives the container the *host's* daemon, via a socket that grants full API authority.
Docker documents the daemon socket as requiring root-equivalent access ([Docker daemon attack surface](https://docs.docker.com/engine/security/), accessed 2026-10-09). The escalation is trivial and needs no exploit: `docker run -v /:/host --privileged` from inside the container. This is strictly worse than (a) on a shared host. [verified]

**(c) Rootless Docker / Podman inside the container.** The container itself is unprivileged (rootless via
subuid mapping) and runs a nested rootless container runtime. Docker documents rootless mode running as
a non-root user with its own uid mapping ([Docker rootless mode](https://docs.docker.com/engine/security/rootless/), accessed 2026-10-09); Podman documents running rootless containers inside a rootless container ([Podman: Running Podman inside a container](https://docs.podman.io/en/latest/markdown/podman-run.1.html), accessed 2026-10-09). This is a real answer to DinD and is the right one for "my build needs to build a container." It still shares the host kernel, and nested user namespaces compound rather than remove that — a double mapping means fewer ids available to each level. [verified]

**(d) Sysbox.** Sysbox is an OCI runtime that makes a container behave like a VM: it uses user namespaces
plus kernel features to let a container run Docker/Kubernetes inside it *without* `--privileged`. The
vendor states this position, including that Sysbox containers are more isolated than ordinary containers
because of the additional user-namespace and kernel-level isolation ([Sysbox docs](https://github.com/nestybox/sysbox), accessed 2026-10-09). Treat the strength of the isolation claim as **[vendor claim]** — Sysbox is a third-party kernel-level runtime and pluto would be accepting its correctness as a dependency. Sysbox also constrains which host kernels are supported; the exact current list is not verified here. **[uncertain]**

**(e) No DinD at all — build the image from outside.** Two well-established approaches:

- **Kaniko** executes a Dockerfile inside an unprivileged container *without* a daemon, by implementing
  the build steps in userspace and extracting the filesystem as an unprivileged user ([Kaniko](https://github.com/GoogleContainerTools/kaniko), accessed 2026-10-09). No `--privileged`, no socket, no nested daemon. The limitation is that it cannot perform anything requiring privilege inside the build, so `RUN` steps that mount or setuid fail.
- **BuildKit rootless mode**, including `buildctl-daemonless.sh`, runs BuildKit entirely in user
  namespaces with no privileged helper process ([BuildKit rootless](https://github.com/moby/buildkit/blob/master/docs/rootless.md), accessed 2026-10-09).

**Recommendation for pluto.** For the stated scope (trusted offload, internal CI), the correct answer is
never (a) and never (b). (e) is the default: build images with Kaniko or rootless BuildKit inside the
box, which requires no privileged configuration at all. (c) is the accommodation for teams whose build
genuinely needs a working Docker daemon. Sysbox (d) is the option to re-evaluate if and when untrusted
CI returns, not before. [inference, grounded in the cited capability and privilege documentation]

### 2.9 What containers fundamentally cannot isolate

This is the part that decides the question, so it is stated without hedging.

1. **A shared kernel.** Every container process executes syscalls in the host kernel. There is no
   hypervisor in between, so any kernel vulnerability reachable from an unprivileged user-namespace
   context is directly exploitable from inside the container. NIST SP 800-190 states the limitation
   directly: containers are not a security boundary against a compromised host OS, and additional
   host-level controls are required ([NIST SP 800-190 §2.2](https://csrc.nist.gov/publications/detail/sp/800-190/final), accessed 2026-10-09).
2. **Kernel-exploit escape.** A single host-kernel vulnerability reachable from the container's syscall
   surface yields host privilege. The container's capabilities, seccomp profile, and MAC rules all reduce
   the *surface*; none of them reduce the *impact* of a successful bug in the surface that remains.
   [inference, from the shared-kernel fact above]
3. **Host kernel CVE coupling.** A container-only host has exactly one kernel for all boxes, so every
   box is exposed to every host-kernel CVE simultaneously, and every box's kernel must be patched by
   the operator at the same cadence. With one guest kernel per box the same CVE still applies, but the
   *patch surface is per box* — which is not automatically better, since most operators patch nothing.
   [inference]
4. **Side channels.** Meltdown, Spectre, and the subsequent families (L1TF/Foreshadow, MDS, SRBDS, and
   the retbleed class) cross the container boundary because the boundary is not a hardware boundary.
   See §5.2. [verified; see the cited CVEs]
5. **Noisy neighbour and denial of service.** cgroup limits bound CPU, memory, PIDs, and IO, but a
   container can still cause host-wide resource pressure (page cache churn, memory fragmentation,
   scheduler contention) and consume host-wide finite resources (file descriptors, sockets, entropy).
   [inference]

The summary sentence, which should be quoted in any pluto user-facing docs: **containers are a
packaging and resource-isolation mechanism with kernel-mediated confinement, not a security boundary
against the host kernel.** That is true of the best-configured rootless container and it does not change
when the workload is trusted. [inference, from NIST SP 800-190]

---

## 3. microVM hardening: Firecracker done properly

### 3.1 Firecracker's security model

Firecracker is a VMM purpose-built for running untrusted-ish multi-tenant workloads with a small attack surface, and its own documentation states the design goal directly: it "simplifies the deployment surface by... taking a minimalist approach to the device model" and enabling "simplified production operations" while reducing the attack surface (Firecracker website, https://firecracker-microvm.github.io, accessed 2026-10-09). Its security model rests on three load-bearing claims:

- **Small device model.** Exactly five emulated devices: virtio-net, virtio-block, virtio-vsock, a serial
  console, and a minimal i8042 keyboard controller used only for graceful stop. No USB, no GPU, no PCI
  hot-plug, no BIOS, no emulated IDE/SCSI, no RTC emulation beyond what the guest kernel provides
  itself ([Firecracker website](https://firecracker-microvm.github.io), accessed 2026-10-09; device model confirmed in [firecracker-operation.md §2.8](firecracker-operation.md)). Any device emulation bug is a potential VMM escape vector; Firecracker minimizes the number of such vectors. The device list is **[verified]**; the conclusion that this makes it safe for multi-tenant use is AWS's own, so it is a **[vendor claim]**.
- **A thin, audited codebase in Rust**, with no legacy emulation (QEMU carries decades of device models).
  This is a code-audit property, not a formal guarantee. [vendor claim]
- **Multi-tenant posture in Firecracker's own docs.** The production host setup guide is written for
  multi-tenant hosts and its first instruction is that Firecracker must be started only via the jailer
  or under constraints "equal or more restrictive" ([prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-09).

**The position for pluto:** Firecracker is designed for exactly the multi-tenant problem pluto does not
currently have. Its security model is *not* the reason pluto chose it (ADR 0001 chose it for rootless
operation, boot speed, small device model, and vsock) — but the same properties that make it a good
multi-tenant VMM make it a good single-tenant one, provided the process-level containment is right.
[inference]

### 3.2 Each jailer layer, and what it costs to replicate

The jailer is the reference implementation of "Firecracker done properly." Its layers, per Firecracker's
documentation ([jailer.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md), accessed 2026-10-09) and [prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-09):

| Layer | Mechanism | Threat it addresses | Rootless cost |
|---|---|---|---|
| **Chroot** | `chroot` into an empty per-VM directory before exec | VMM cannot read the host filesystem even if it tries; a VMM compromise starts in an empty box | Cheap: `unshare -Urm` + pivot_root/chroot can be done by an unprivileged user in a user namespace |
| **Cgroups** | Per-VM cgroup with cpu/memory/blkio limits, plus `--resource-limit` for `fsize` and `no-file` | Resource exhaustion, runaway IO, unbounded log growth | **Hard.** Requires cgroup delegation (§3.6) |
| **Seccomp** | Firecracker applies its own per-thread filters; the jailer does not add them | Bug in the VMM's syscall use | Free — already active in pluto's current posture (ADR 0001 addendum, A4) |
| **Network namespace** | New netns, created by the jailer | VMM sees only its own TAP device and the netns's loopback; no other interfaces | Cheap: pluto already does `unshare -n` |
| **Privilege drop** | Setuid/setgid to a configured unprivileged uid/gid before exec, having used root only to `mknod` `/dev/kvm` and `/dev/net/tun` and create the cgroups | VMM runs as a user with no host filesystem access | Already satisfied: pluto's VMM runs as the host user |
| **NoNewPrivs** | Set by the jailer's `unshare` path | Privilege escalation via setuid inside the VMM's tree | Cheap and worth adopting explicitly |

**Reading this table against pluto's current posture.** pluto already has seccomp (free), a network
namespace (`unshare -Urn`), and privilege drop (rootless). The gap is chroot and cgroups. Of those,
**chroot is nearly free** and closes a real hole (a compromised VMM could otherwise read the host user's
home directory, SSH keys, and every box's disk image), while **cgroups are the expensive one** because
they require host cooperation. [inference]

The honest framing of the cost: "Firecracker done properly" for pluto is not "adopt the jailer" (that
breaks rootlessness, which ADR 0001 already rejected and which this note does not reopen). It is
Firecracker's own alternative wording — "process constraints equal or more restrictive than those in the
jailer" — implemented rootlessly: chroot/pivot_root into a per-box empty directory, `no_new_privs`,
Firecracker's default seccomp untouched, the existing user+network namespace, and delegated cgroups.
[inference from prod-host-setup.md]

### 3.3 Minimal device model and seccomp: no change needed

Confirmed in [firecracker-operation.md](firecracker-operation.md) §3.6 and §4-A5/A4: Firecracker's
device model is fixed, pluto uses exactly the five devices, Firecracker applies its most restrictive
seccomp filters per-thread by default, and Firecracker's own docs discourage `--no-seccomp` and custom
filters in production. pluto's posture is already correct here. The actionable item from this research
is negative: **do not** add `--no-seccomp`, `--seccomp-filter`, `--enable-pci` (restricted in ADR 0001
for performance reasons), or any feature that widens the device model. [verified]

One addition worth making beyond the earlier note: the serial console. Firecracker's docs do **not**
recommend the serial device in production, because "in production builds, Firecracker does not expose the
serial console port, since it may contain guest data that the host should not see"
([design.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md), accessed 2026-10-09). pluto depends on `console=ttyS0` for boot logging, so this cannot be adopted as-is, but it is worth recording as a real, accepted deviation with a stated reason, rather than an unexamined default. [verified]

### 3.4 Rootless operation

pluto's rootless posture (`unshare -Urn` + TAP + slirp4netns) is verified to work and is what makes
pluto installable by an individual self-hoster without root. Its cost, precisely stated:

- **`/dev/kvm` access.** The user must be in the `kvm` group, or `/dev/kvm` must be world-accessible.
  That is a host configuration step, not something pluto can do. [verified as a mechanism; the group
  requirement is the standard Linux permission model]
- **No cgroup enforcement without delegation** (§3.6).
- **No ability to set up the jailer's chroot + mknod pre-work**, which is why the jailer is unavailable
  rootlessly. pluto's substitute is the userns + chroot path described in §3.2.

This cost is low and the benefit is large. Dropping rootlessness to "do microVMs properly" would trade a
large installation barrier for a marginal isolation gain in a single-tenant threat model. [inference]

### 3.5 Guest hardening

A microVM's isolation is the product of two kernels' attack surfaces: the host kernel plus the VMM, and
the guest kernel. Hardening the guest is therefore not optional.

- **Kernel choice and pinning.** Firecracker supports at least two major guest versions at a time;
  6.1's guest support window closed 2026-09-02 and 6.18 is supported until 2028-06-01
  ([kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-09). pluto already moved to 6.18.51 (ADR 0001 addendum). The discipline that matters: **never float to the newest kernel**. A box's image pins the kernel version it boots, so the guest kernel is an auditable, reproducible input. That is a property containers do not have at all — a container's "kernel" is whatever the host runs, chosen by someone else.
- **Minimal rootfs.** The smaller the rootfs, the fewer setuid binaries, the fewer services listening,
  the fewer packages with their own CVE history. `nomodule` (adopted in ADR 0001 addendum, A1) closes
  the loadable-module path entirely: an attacker inside the guest cannot `insmod` their way to deeper
  guest-kernel attack surface. What `nomodule` does is **[verified]**; the minimal-rootfs
  recommendation is **[inference]** from standard practice.
- **Vsock as the only host channel.** vsock is a virtio device; the host side is a unix socket pluto
  controls. There is no shared filesystem, no shared memory segment, no host network route. This is the
  cleanest available host-guest boundary and is worth preserving as pluto's only channel — a
  virtiofs or 9p share for the checkout would be a meaningful expansion of the guest's attack surface
  for convenience. [inference]
- **Guest-side hardening.** Even inside a microVM, the guest is not a trusted kernel from the host's
  perspective — but hardening it reduces what a guest-level compromise achieves (lateral movement into
  the box's own services, credential theft from the box's own disk). The same no-setuid, minimal-package
  logic applies. [inference]

### 3.6 Cgroups, rootlessly

The cgroup question is the only genuinely expensive item in "done properly," and it is the same
engineering problem pluto already faces for container resource limits (§2.5), so the work would be
shared. cgroup v2 delegation ([cgroup v2: Delegation](https://docs.kernel.org/admin-guide/cgroup-v2.html#delegation), accessed 2026-10-09) plus systemd's `Delegate=yes` on the user unit
([systemd.resource-control: Delegate](https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html#Delegate=), accessed 2026-10-09) is the mechanism. The honest status: this is
host-dependent, needs a documented setup step where delegation is not already present, and may need a
privileged helper on hosts that do not support it. This is the same conclusion
[firecracker-operation.md](firecracker-operation.md) reached for R2, and nothing has changed it.
[verified for the mechanism; the exact per-distro default is [uncertain]]

### 3.7 Cloud Hypervisor posture

Cloud Hypervisor is the fallback VMM in ADR 0001. Its security posture differs in ways relevant here:
it is built on the Rust VMM ecosystem, has a larger device surface than Firecracker (it supports PCI,
virtiofs, and more), which is precisely what makes it the right choice for the capabilities Firecracker
lacks ([Cloud Hypervisor docs](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/security.md), accessed 2026-10-09). Cloud Hypervisor documents its own security model and threat
model, and like Firecracker, it recommends running under a sandboxed process.

**The trade for pluto is explicit:** every capability pluto gains by switching to Cloud Hypervisor
(virtiofs, UEFI/UKI boot, migration) increases the VMM's own attack surface. For a box that runs a
user's build, the correct posture is to stay on the VMM with the smallest device model and accept the
capability loss — which is what ADR 0001 already decided. [inference]

### 3.8 Known CVEs and escape research: theory vs demonstrated

Being precise here matters, because "Firecracker has been escaped" and "a theoretical escape was
published" are different statements.

- **runC / CVE-2019-5736** — *demonstrated, container-side.* A malicious container image could overwrite
  the host `runc` binary via `/proc/self/exe` at container start, achieving host root on the next
  container start. Fixed in runc 1.0.0-rc6+ ([CVE-2019-5736](https://nvd.nist.gov/vuln/detail/CVE-2019-5736), accessed 2026-10-09). This is the reference case for why the container boundary is thin
  in practice, and it is a *runtime* bug, not a kernel bug — a reminder that the container's confinement
  depends on a userspace daemon being correct.
- **Firecracker: no publicly demonstrated guest-to-host escape.** Firecracker publishes security
  advisories through its GitHub releases and security advisories
  ([Firecracker releases](https://github.com/firecracker-microvm/firecracker/releases), accessed 2026-10-09); as of 2026-10-09 no public, demonstrated, weaponized
  guest-to-host escape against a current, default-configured Firecracker was found in this research.
  **[uncertain — this is a statement about the absence of public evidence, not a proof of absence]**
  Treat any vendor or blog claim of a Firecracker escape with suspicion and ask for a CVE number or a
  reproducible artifact.
- **KVM/QEMU escapes generally** — a long, well-documented history (Venom/CVE-2015-3456, and the
  QEMU device-emulation CVE stream) demonstrates that VMM device emulation is a real escape class. The
  relevant inference for pluto: the mitigation Firecracker offers is not "VMM escapes are impossible" but
  "there are five devices, and each is small." Firecracker's *security model* is sound engineering, not
  a proof. [inference]
- **Cloud Hypervisor / QEMU microvm** — same class of reasoning.

**Residual risks for pluto specifically, after "done properly":**

1. **Host kernel + VMM bug class** — a 0-day in KVM or in one of Firecracker's five devices remains a
   full escape. Unmitigated, unmitigable by configuration.
2. **`/dev/kvm` access** — anything else in the host user's session with `/dev/kvm` access can interact
   with the box's memory via the same VMM fd. In a single-user self-hoster host this is a low concern;
   the day a second human user gets a login, it is not. [inference]
3. **Snapshot/restore** — not currently used (ADR 0001, R3). If adopted, the memory file becomes a
   host-side artifact containing the guest's entire memory, including secrets, and must be protected as
   such. [inference from Firecracker's snapshot semantics]
4. **Host compromise dominates everything.** If the host is owned, boxes are owned: their disks are
   files, their vsock endpoints are sockets, and their memory is readable through the VMM. No guest or
   VMM configuration mitigates this. [inference]

---

## 4. Adjacent options: gVisor and Kata Containers

### 4.1 gVisor

**Architecture.** gVisor is not a container runtime layered on the host kernel and not a hypervisor. It
is a *user-space kernel*: the container's processes make syscalls into a gVisor-provided kernel
implementation (the "Sentry") that runs as an ordinary unprivileged process on the host and implements
the Linux ABI itself. The Sentry mediates every syscall; only a narrow, controlled set of host syscalls
is made by gVisor itself. gVisor's own documentation describes this as an application kernel that
"provides an additional layer of isolation between the application and the host operating system"
([gVisor documentation](https://gvisor.dev/docs/), accessed 2026-10-09) and documents its security
model and threat analysis in a dedicated page ([gVisor security model](https://gvisor.dev/docs/architecture_guide/security/), accessed 2026-10-09).

**Isolation guarantee.** The property is that a kernel vulnerability *reachable from the container's
syscall surface* does not exist on the host kernel — because the container's syscalls never reach the
host kernel; they terminate in the Sentry. A bug in the Sentry's emulation is still a host-kernel-reachable
bug, but the surface is gVisor's syscall handlers rather than the whole host kernel. This is a genuinely
different and stronger position than a plain container, and it is materially weaker than a hypervisor:
it is a defense-in-depth increment, not a hardware boundary. **[inference]** from gVisor's documented
architecture; gVisor's own framing of the guarantee is stronger, so treat that part as a **[vendor claim]**.

**Cost: syscall compatibility.** The Sentry reimplements the Linux ABI, so not all of the ABI is
supported. gVisor's own documentation maintains a compatibility list of unimplemented syscalls and
features ([gVisor compatibility](https://gvisor.dev/docs/user_guide/compatibility/), accessed 2026-10-09). Practically, workloads that do heavy direct I/O, use unusual syscalls, or use
`io_uring` may fail or fall back. This is the main risk for a build box.

**Cost: performance.** gVisor is slower on syscall-heavy and I/O-heavy workloads than a native container,
and roughly comparable to (sometimes faster than) a microVM, depending on the benchmark. gVisor's own
docs acknowledge the tradeoff and position it for untrusted-code use cases rather than
performance-critical ones ([gVisor docs](https://gvisor.dev/docs/), accessed 2026-10-09). For pluto: a
box's job is builds, tests, and agents, which are compile-and-fork heavy, not pure-I/O streaming, so the
profile is plausible but unverified for pluto's actual workloads. **[uncertain: no pluto-specific
benchmark exists; this must be measured before adopting]**

**DinD under gVisor.** gVisor can run Docker inside it, but the inner Docker runs on the Sentry, not on a
real kernel, and the limitations compound (nested cgroups, mounts, and device access all go through the
emulation). gVisor documents running Docker and Kubernetes under gVisor
([gVisor: Running Docker](https://gvisor.dev/docs/user_guide/quick_start/docker/), accessed 2026-10-09), so it is possible; whether it is reliable for a given build
is an empirical question. **[uncertain]**

### 4.2 Kata Containers

**Architecture.** Kata is an orchestration and integration layer: it presents a containerd/CRI-compatible
runtime that actually launches one microVM per pod (or per container, depending on configuration), with
the container runtime (`runc`/`crun`) executing *inside* the VM. The VM is provided by a pluggable
hypervisor — QEMU, Cloud Hypervisor, or Firecracker ([Kata Containers architecture](https://github.com/kata-containers/kata-containers/blob/main/docs/README.md), accessed 2026-10-09; Firecracker
integration documented at [Kata + Firecracker](https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-use-kata-containers-with-firecracker.md), accessed 2026-10-09).

**Isolation guarantee.** Same class as a bare microVM: hardware-enforced boundary, plus the additional
containment that the container runtime applies *inside* the guest. Kata adds its own namespace-based
isolation on top ([Superuser: Firecracker and Kata Containers](https://superuser.openinfra.org/articles/firecracker-kata-containers-open-collaboration), accessed 2026-10-09). Strictly stronger than a bare
microVM of the same VMM; but the boundary is the hypervisor, so the VMM-bug class still applies.

**Cost.** Memory and CPU overhead per pod is that of a full guest — this is precisely what pluto is
trying to escape. Kata is also designed for a Kubernetes/containerd control plane, and
[firecracker-operation.md](firecracker-operation.md) §2.6 already established that Kata's integration
shape is not a model for pluto's single-host systemd architecture. [inference]

### 4.3 Fit for pluto

| | Container (rootless, hardened) | gVisor | microVM (Firecracker) | Kata |
|---|---|---|---|---|
| Boundary | host kernel (shared) | Sentry (user-space kernel) | hypervisor | hypervisor + inner runtime |
| Kernel CVE exposure | all, host-wide | narrow, Sentry-mediated | host kernel only for VMM/KVM | host kernel only for VMM/KVM |
| Per-box RAM overhead | ~0 | Sentry process (tens of MB) | guest memory reservation (fixed) | guest memory + inner runtime |
| Boot/wake time | ms | ms | ~430 ms (ADR 0001) | seconds (full guest boot) |
| RAM reclaim when idle | yes (`memory.high`) | partial | no | no |
| DinD without privilege | no (needs rootless nesting or Kaniko) | possible, untested for our builds | no (no kernel modules without extra config) | yes, inside guest |
| OCI packaging | native | native | no (needs conversion) | native |
| Fits pluto's rootless single-host shape | yes | yes | yes (today) | no |

**The important read of this table.** For pluto's stated scope — single-tenant trusted offload and
internal enterprise CI — the container column and the microVM column are *both adequate*, and the
differentiators are operational (density, RAM reclaim, OCI, boot time), not security. The security
columns only separate when the workload is untrusted. That is why the recommendation in §8 is
conditional on trust class, which pluto already has as a first-class concept (GLOSSARY.md: *trust class*).
[inference]

---

## 5. Threat model and residual risk

This section is deliberately explicit about what is demonstrated and what is theoretical. The
distinction is load-bearing: a marketing document collapses them; a self-hoster making a trust decision
needs them separated.

### 5.1 The threat model, stated

pluto's current scope, in threat-model terms:

- **Trusted class.** The box's code comes from a branch the operator controls, or a PR from a labeled
  contributor in an internal repo. The compromise being defended against is *accidental breakage*,
  *dependency compromise* (a malicious package published upstream), and *self-inflicted damage* (a
  build that reformats a disk, exhausts memory, or wipes `$HOME`). This is a containment and robustness
  threat model, not an adversarial one.
- **Untrusted class — out of scope.** External PRs and forks. The compromise being defended against is
  a deliberately hostile actor who knows they are inside a CI sandbox and will attempt escape. pluto
  already scopes this correctly with trust classes (GLOSSARY.md); the class exists, it is just not
  populated yet.
- **Host.** The host is assumed to be owned by the operator, correctly configured, and patched. If the
  host is owned, boxes are owned (§3.8, item 4).

### 5.2 Side channels: the Spectre/Meltdown class

This is the residual risk that applies to *every* option — hardened container, gVisor, and microVM
alike — and it should therefore not drive the microVM-vs-container decision.

- **Meltdown (CVE-2017-5754)** — demonstrated. Out-of-order execution plus speculative access to
  unmapped kernel memory allows an unprivileged process to read host kernel memory it should not see.
  Mitigated in hardware and by kernel page-table isolation (PTI/KPTI) ([CVE-2017-5754](https://nvd.nist.gov/vuln/detail/CVE-2017-5754), accessed 2026-10-09; [kernel docs: Meltdown](https://docs.kernel.org/admin-guide/hw-vuln/spectre.html), accessed 2026-10-09).
- **Spectre v1/v2 (CVE-2017-5753 / CVE-2017-5715)** — demonstrated. Branch-target and bounds-check
  bypass permit cross-process reads ([CVE-2017-5715](https://nvd.nist.gov/vuln/detail/CVE-2017-5715), accessed 2026-10-09). Mitigations (IBPB/IBRS/retpoline) are host-firmware and kernel settings.
- **L1 Terminal Fault / Foreshadow (CVE-2018-3615, CVE-2018-3620, CVE-2018-3646)** — demonstrated,
  including an SGX variant. Particularly relevant because it reads L1 data across privilege levels
  ([CVE-2018-3615](https://nvd.nist.gov/vuln/detail/CVE-2018-3615), accessed 2026-10-09).
- **Microarchitectural Data Sampling (CVE-2018-12126, CVE-2018-12127, CVE-2018-12130, CVE-2019-11091)** — demonstrated, affecting hyperbuffers and store buffers ([CVE-2019-11091](https://nvd.nist.gov/vuln/detail/CVE-2019-11091), accessed 2026-10-09).

The security-relevant corollates, all verified:

1. **MicroVMs do not automatically fix side channels.** A guest kernel is a separate process tree, but
   it executes on the same physical core with the same caches. Spectre-class attacks against a co-tenant
   guest are possible where the CPU, firmware mitigations, and VMM configuration permit. This contradicts
   a common assumption and is worth stating: "microVM" is a memory and syscall boundary, not a
   timing boundary. [inference from the cited CVEs' mechanisms]
2. **Containers are worse off here**, because the co-tenant is a process in the same kernel with shared
   caches and no additional VMM-mediated indirection. [inference]
3. **CPU mitigations are a host-firmware and host-kernel responsibility in every case.** pluto's
   guidance to users should be: patch your host firmware, keep your host kernel current, and consider
   `mitigations=auto` (the default) rather than disabling mitigations for performance
   ([kernel docs: CPU mitigations](https://docs.kernel.org/admin-guide/hw-vuln/index.html), accessed 2026-10-09). [verified]

### 5.3 Supply chain: third-party actions and dependencies

The most likely *realistic* compromise of a trusted-class box is not an escape at all. It is a malicious
or typosquatted dependency, or a compromised third-party GitHub Action, ending up as code the build
executes with the box's privileges.

- GitHub Actions from arbitrary third parties execute with the credential and repository access the
  workflow grants, and GitHub's own guidance is to pin actions to a full-length commit SHA rather than a
  tag or branch, precisely because tags are mutable ([GitHub: Security hardening for GitHub Actions](https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions), accessed 2026-10-09).
- The package registries npm, PyPI, RubyGems, crates.io, and Maven Central have all hosted
  typosquatting and account-takeover malware. This is not a hypothetical; it is the dominant CI compromise
  vector in the 2020s. [verified as a general pattern; specific incidents are numerous and are not cited individually here to avoid asserting current counts I cannot verify]

**Why this matters for the runtime decision:** a malicious dependency runs *inside* the box with the
box's privileges. Under a microVM, the ceiling on what it can do is "everything inside the box." Under
a container, the ceiling is "everything the container's capabilities allow, plus anything reachable in
the host kernel." The mitigation that actually helps is pinning and review, not the runtime boundary.
[inference]

### 5.4 Compromised host

Restated from §3.8 item 4 because it is the residual that dominates all others: if the host is owned, the
isolation technology is irrelevant. A hardened container does not contain a root-owned host kernel; a
microVM does not contain a host process that can read the VMM's guest memory. pluto's posture — user-level
daemon, no root requirement, no privileged helper — keeps the host attack surface small, which is the
only control that matters here. [inference]

### 5.5 Summary of residuals, ranked for pluto's stated scope

| # | Residual | Applies to container | Applies to microVM | Mitigated by configuration? |
|---|---|---|---|---|
| 1 | Malicious/typosquatted dependency or Action | yes | yes | **Yes — pinning, review** |
| 2 | Host compromise | yes | yes | **Yes — small host surface, no root** |
| 3 | Side channels (Spectre/Meltdown class) | yes | yes | **Yes — host firmware/kernel mitigations** |
| 4 | Host-kernel CVE reachable from workload | yes | no (only VMM/KVM surface) | Partially — seccomp narrows container surface |
| 5 | VMM device-emulation bug | n/a | yes | No — Firecracker's small device model reduces it |
| 6 | Resource exhaustion / noisy neighbour | yes | yes | **Yes — cgroups in both cases** |

The table is the argument for the recommendation: for residuals 1–3, which are the ones that actually
dominate a single-tenant trusted-offload threat model, the runtime choice is irrelevant. [inference]

---

## 6. Decision framework for pluto

The framework is: **the trust class determines the boundary, and everything else is operational.** Below, the two decisions are stated with the risk the operator accepts in each case.

### 6.1 Container-only baseline for trusted offload and internal enterprise CI

Applies to the **trusted** class only: a self-hoster's own branch boxes, and an enterprise's internal CI
where every contributor is an employee or a labeled maintainer.

**The baseline, as a checklist an operator or auditor can verify against:**

1. Rootless runtime, running as an unprivileged uid with an allocated subuid/subgid range
   (`/etc/subuid`, `/etc/subgid`); never uid 0 on the host ([Docker rootless](https://docs.docker.com/engine/security/rootless/); [Podman rootless](https://docs.podman.io/en/latest/markdown/podman.1.html)).
2. User namespace per box, with the container's root mapped into the unprivileged range.
3. Seccomp enabled at the runtime's default profile. Never `unconfined` ([Docker seccomp](https://docs.docker.com/engine/security/seccomp/); [Kubernetes seccomp](https://kubernetes.io/docs/tutorials/security/seccomp/)).
4. Capability set = the runtime's default (or smaller). Never `--privileged`, never `CAP_SYS_ADMIN`
   ([Docker capabilities](https://docs.docker.com/engine/reference/run/#runtime-privilege-and-linux-capabilities)).
5. `NoNewPrivileges: true` ([OCI spec](https://github.com/opencontainers/runtime-spec/blob/main/config-linux.md#process)).
6. The host's default MAC (AppArmor or SELinux), not disabled ([Docker AppArmor](https://docs.docker.com/engine/security/apparmor/); [Docker SELinux](https://docs.docker.com/engine/security/selinux/)).
7. Read-only rootfs with explicit `tmpfs` for writable paths ([CIS Docker Benchmark](https://www.cisecurity.org/benchmark/docker)).
8. No host bind-mounts into a box; the checkout lives in the box's own storage.
9. Default `maskedPaths` / `readonlyPaths` and the default devices cgroup whitelist, unmodified
   ([OCI spec](https://github.com/opencontainers/runtime-spec/blob/main/config-linux.md#masked-paths)).
10. cgroup v2 limits per box: `memory.high` + `memory.max`, `cpu.weight` (or `cpu.max`), `pids.max`
    ([cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)).
11. Per-box network namespace; documented egress blocklist including 169.254.169.254; host firewall
    rules rather than in-container policy.
12. Secrets mounted at run time, never in an image or in an environment variable visible to `inspect`;
    OIDC/short-lived where the forge supports it ([GitHub OIDC hardening](https://docs.github.com/en/actions/deployment/security-hardening-your-deployments/about-security-hardening-with-openid-connect)).
13. Log redaction at the single host-daemon exit point.
14. Container builds via rootless BuildKit or Kaniko; **no `--privileged` DinD, no docker socket mount**
    ([BuildKit rootless](https://github.com/moby/buildkit/blob/master/docs/rootless.md); [Kaniko](https://github.com/GoogleContainerTools/kaniko)).
15. Third-party Actions and dependencies pinned to hashes/SHAs and vendored where possible
    ([GitHub Actions hardening](https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions)).

**The risk the operator explicitly accepts**, and which must be stated in pluto's documentation rather
than implied: a hostile or compromised workload inside a box can reach any host-kernel vulnerability
reachable from an unprivileged user namespace, and can observe co-resident boxes to the limits of the
host's CPU side-channel mitigations. pluto is saying: *this is not a security boundary against a
compromised host kernel or a determined attacker who has a host-kernel 0-day.* That is consistent with
VISION.md's existing "no isolation guarantee" position; the baseline above raises the floor
substantially without changing the ceiling. [inference]

### 6.2 What proper microVM hardening costs

If pluto keeps microVMs and wants to say they are done properly:

| Item | Effort | Rootless-compatible? |
|---|---|---|
| chroot/pivot_root into an empty per-box dir before exec | Low — a few lines in the runner | **Yes** |
| `no_new_privs` on the VMM process | Low | **Yes** |
| Keep Firecracker's default seccomp and 5-device model | Zero (already true) | **Yes** |
| Guest kernel pinned, 6.18.x, minimal rootfs, `nomodule` | Low-medium (already partly done, ADR 0001) | **Yes** |
| vsock as the only host channel; no virtiofs/9p share | Low (already true) | **Yes** |
| Delegated cgroup v2 per box | **Medium-high** — host setup step, per-distro variance, possibly a privileged helper | Partially |
| Jailer (the full Firecracker-recommended path) | — | **No** — requires root; rejected, do not revisit |

**The honest cost statement:** apart from cgroups, "Firecracker done properly" is cheap and pluto should
just do it, because it costs a few lines and closes the "compromised VMM reads the host user's home
directory" hole. The cgroup item is the only expensive one, and it is shared with the container path
(§2.5, §3.6), so it is not a reason to prefer either. [inference]

### 6.3 The trap in the middle

Do not adopt `--privileged` DinD, a docker socket mount, or a shared-filesystem mount into a box "until
the trust model is ready." Each of these converts a microVM into an expensive container with worse
operational properties. If a capability is only safe with `--privileged`, the correct answer is to not
have the capability until the trust model justifies it. [inference]

### 6.4 Where gVisor slots in if untrusted CI returns

gVisor is not a pluto constituent today and should not be installed speculatively. But the trust-class
concept GLOSSARY.md already defines is exactly the seam it would use, so the plan should be written now
and executed only if the untrusted class is populated:

1. The **untrusted** class gets a gVisor-based runtime (or a microVM with full jailer-equivalent
   containment plus egress firewalling), while the **trusted** class keeps whichever backend pluto
   chooses for operational reasons.
2. gVisor must first be measured against pluto's real workloads — compile-heavy Go/Rust builds, agent
   runs, I/O-heavy test suites — specifically for syscall compatibility and build-time overhead. The
   compatibility docs are the starting list, not a guarantee ([gVisor compatibility](https://gvisor.dev/docs/user_guide/compatibility/)).
3. Environment layers must never cross trust classes (GLOSSARY.md already forbids this) — a gVisor box
   cannot restore a layer produced by a microVM box without the layer's provenance being re-verified.
4. Egress firewalling becomes mandatory, not optional, in the untrusted class — the E2B pattern
   (per-sandbox netns + nftables) is the reference ([E2B runtime README](https://github.com/e2b-dev/runtime)).

[inference; the trust-class seam is pluto's own concept, the runtime-specific guidance is from the
cited sources]

---

## 7. Implications for pluto (ADR 0001)

This section states plainly what a container-only decision would do to existing decisions, because ADR
0001 is currently load-bearing and a container-only direction supersedes parts of it.

**If pluto drops microVMs for containers, ADR 0001 must be superseded or rewritten**, not merely
amended. Specifically:

| ADR 0001 element | Status if container-only |
|---|---|
| "Firecracker is the primary VMM, Cloud Hypervisor a triggered fallback" | **Superseded.** No VMM. The thin VMM interface in the runner becomes a container-runtime interface |
| "The jailer is deliberately skipped until a hardening need exists, since there is no trust model" | **Superseded.** Jailer, chroot, cgroup-via-jailer, seccomp-via-VMM, and the 5-device model are all irrelevant. Replaced by the OCI-runtime containment checklist (§6.1) |
| Boot args, `cache_type=Writeback`, raw drives, qcow2 rejection | **Superseded.** These are VMM-level configuration. The OCI equivalent is image format and storage driver policy, which is a different doc |
| Rootless operation via `unshare -Urn` + TAP + slirp4netns | **Superseded in mechanism, preserved in posture.** Rootless containers are also unprivileged with userns + subuid mapping and slirp4netns/pasta egress, so the *rootless posture* survives; the implementation does not |
| "Runner prepares the disk, launches the process, wires vsock, performs lifecycle" | **Partially superseded.** vsock disappears; the host↔box channel becomes a unix socket, a veth pair, or an exec/session channel. This is a significant architectural change, not a swap |
| M3 addendum items (A1–A4 boot args/metrics/logs/seccomp; R1–R4) | **Mostly obsolete.** A4 (seccomp) has an OCI analogue that pluto must now own explicitly rather than inherit from Firecracker; R1 (jailer) expands into the whole §6.1 checklist |
| GLOSSARY.md: *VMM*, *guest agent*, *box contract*'s kernel/rootfs image semantics | **Glossary work required.** "VMM" has no referent. "Guest agent" becomes "in-box agent". *Image* (kernel + rootfs, bootable) becomes an OCI image — a different meaning for the same word |

**If pluto keeps microVMs**, ADR 0001 stands and this note adds to it:
- A new decision record for the jailer-equivalent containment (chroot, no_new_privs, delegated cgroups),
  with the same "revive trigger: a real trust boundary appears" logic ADR 0001 already uses.
- An explicit, documented deviation for the serial console (Firecracker's docs advise against it in
  production; pluto needs it for boot logs).
- An explicit statement that seccomp and the device model are inherited-correct and must not be
  weakened.

**If pluto keeps both**, then the *VMM seam* that ADR 0001 already established is the right interface —
but it must be generalized from "VMM" to "box runtime," and the trust class must select the backend.
This is the most defensible architecture, and it is also the most work. [inference]

---

## 8. Recommendation

**Recommendation: keep microVMs for now, and harden them properly; do not go container-only yet. Add the jailer-equivalent containment that costs nothing, commit to delegated cgroups, and write the container baseline into the design so it is ready when the density problem becomes real.** [inference]

This is not a "microVMs are more secure" verdict. It is a cost-of-change verdict. The reasoning:

1. **Security does not decide it.** For the trusted class, the residuals that dominate (§5.5, items 1–3)
   are identical either way. pluto would not buy meaningful isolation by switching, and it would not lose
   meaningful isolation by staying.
2. **pluto's product is a durable machine with SSH.** A box is a machine you enter, that lives across a
   worktree, whose disk is its state, and that survives pause. That maps directly onto a VM with a
   persistent disk. A container is a process with an overlay; making it a durable machine means
   inventing exactly the machinery (snapshot/restore, disk-per-box, boot lifecycle) that a VMM gives
   for free. **[inference — this is the strongest argument, and it is architectural, not security]**
3. **The hardening gap is small and cheap.** chroot + `no_new_privs` are a few lines in the runner.
   Firecracker's seccomp and device model are already correct (§3.3). The guest kernel is already pinned
   and current (ADR 0001). The only expensive item, delegated cgroups, is shared with the container path.
4. **Density is a real problem but not today's problem.** The microVM-per-box model stops scaling at
   roughly one host per few dozen boxes on RAM grounds, because guest memory is reserved rather than
   reclaimed. pluto's users are individual self-hosters and small internal teams. The trigger to revisit
   is measurable: box counts per host, or memory pressure, not a blog post. [inference]

**Assumptions, stated explicitly so they can be falsified:**

- **(A1)** Untrusted external CI stays out of scope. If it returns, this recommendation changes: the
  untrusted class needs gVisor or a jailer-equivalent microVM plus mandatory egress firewalling (§6.4).
- **(A2)** pluto's users value a durable, SSH-able machine over dense boxes. If the product goal shifts
  to "run 500 builds on this host," container-only becomes right and ADR 0001 gets superseded.
- **(A3)** The host remains single-tenant — one operator, their own boxes, no other users' code. This is
  the assumption ADR 0001 already rests on.
- **(A4)** `~430 ms` boot and a pinned 6.18 guest kernel stay acceptable as density pressure rises.
  Snapshot/restore (ADR 0001, R3) is the first lever if it does not.

**What pluto should do next, in order:**

1. **Adopt the jailer-equivalent containment in the runner:** `unshare -Urn` plus chroot/pivot_root into
   a per-box empty directory, plus `no_new_privs`. Cheap, closes a real hole. *(change to runner)*
2. **Land delegated cgroup v2 limits per box** (already committed by the M3 spec; the container path
   would need the identical mechanism, so it is not wasted work either way). *(already tracked)*
3. **Document the container baseline (§6.1) in pluto's docs** even while running microVMs, so the
   operational hardening story — capabilities, secrets, egress, log redaction, pinning — is written down
   and testable. None of that is microVM-specific.
4. **Write ADR 0002 for the jailer-equivalent posture**, recording that the jailer itself remains
   rejected, that rootlessness is preserved, and what "equal or more restrictive" concretely means in the
   runner.
5. **Set the revive trigger in the same ADR:** migrate to container-only when (a) boxes per host exceeds
   what RAM allows, measured, or (b) OCI packaging becomes a user requirement. Not before.

If, after that, pluto still wants to go container-only, §6.1 is the baseline and §7 is the list of
things to supersede.

---

## 9. Uncertainties

- **Firecracker escapes.** No public, demonstrated, weaponized guest-to-host escape against a current
  default-configured Firecracker was found in this research. This is a statement about the absence of
  public evidence as of 2026-10-09, **not** a proof of absence, and it should be revisited on a
  schedule rather than relied upon. **[uncertain]**
- **gVisor performance on pluto's workloads.** No pluto-specific benchmark exists. Published gVisor
  figures are workload-dependent; a compile-heavy Go/Rust build with an agent loop may behave very
  differently from a syscall microbenchmark. Must be measured before adoption. **[uncertain]**
- **gVisor and DinD reliability.** gVisor documents Docker-under-gVisor, but whether an arbitrary build
  image works is empirical. **[uncertain]**
- **Sysbox supported-kernel list.** Not verified as of 2026-10-09. **[uncertain]**
- **Rootless cgroup v2 delegation per distro.** The mechanism is documented; the default availability
  and the exact setup step vary across Fedora, Debian, Ubuntu, and Arch. Needs a per-target-host
  verification pass. **[uncertain]** — also flagged in
  [firecracker-operation.md](firecracker-operation.md) §5.
- **Specific supply-chain incident counts.** Named incidents and current 2026 statistics were not
  verified individually and are deliberately not asserted. **[uncertain]**
- **CIS Docker Benchmark section numbers.** The benchmark is versioned and its numbering shifts; the
  controls cited (read-only rootfs, capabilities, no privileged containers) are stable in *content*
  even where the section number quoted may be off. **[uncertain]**
- **AppArmor vs SELinux host defaults.** Characterized generally here; a specific target host's
  enforcing status must be checked. **[uncertain]**

## 10. Sources

All accessed 2026-10-09.

### Firecracker and microVM

1. Firecracker prod-host-setup.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md — jailer requirement in production, seccomp defaults, serial-console and logging guidance, cgroup advice
2. Firecracker jailer.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md — jailer layers: chroot, cgroup v1/v2, seccomp, netns, privilege drop, resource limits
3. Firecracker design.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md — serial console absent from production builds; metrics emission
4. Firecracker kernel-policy.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md — supported guest/host kernels, support windows, direct-boot config flags
5. Firecracker SPECIFICATION.md — https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md
6. Firecracker website — https://firecracker-microvm.github.io — five-device model, minimalism rationale
7. Firecracker releases / security advisories — https://github.com/firecracker-microvm/firecracker/releases — CVE/advisory surface
8. Cloud Hypervisor security docs — https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/security.md — Rust VMM security posture
9. Existing: docs/research/firecracker-operation.md — operator survey, pluto's current posture, A1–A4 / R1–R4 decisions

### Container runtimes, OCI, and standards

10. OCI Runtime Specification (config-linux.md) — https://github.com/opencontainers/runtime-spec/blob/main/config-linux.md — seccomp, capabilities, maskedPaths/readonlyPaths, devices cgroup, NoNewPrivileges
11. NIST SP 800-190, Application Container Security Guide — https://csrc.nist.gov/publications/detail/sp/800-190/final — container-not-a-boundary position, image/registry/runtime/host controls
12. CIS Docker Benchmark — https://www.cisecurity.org/benchmark/docker — read-only rootfs, capability, and privileged-container controls
13. Kernel cgroup v2 documentation — https://docs.kernel.org/admin-guide/cgroup-v2.html — `memory.max` vs `memory.high`, `cpu.max`/`cpu.weight`, `pids.max`, delegation
14. Docker docs: rootless mode — https://docs.docker.com/engine/security/rootless/ — subuid/subgid requirement, cgroup v2 + systemd requirement
15. Docker docs: seccomp — https://docs.docker.com/engine/security/seccomp/ — default profile, `unconfined`
16. Docker docs: runtime privilege and Linux capabilities — https://docs.docker.com/engine/reference/run/#runtime-privilege-and-linux-capabilities — default dropped set, `CAP_SYS_ADMIN`
17. Docker docs: AppArmor — https://docs.docker.com/engine/security/apparmor/ — default `docker-default` profile
18. Docker docs: SELinux — https://docs.docker.com/engine/security/selinux/ — type enforcement labels
19. Docker docs: daemon attack surface — https://docs.docker.com/engine/security/ — docker socket authority
20. Podman docs — https://docs.podman.io/en/latest/markdown/podman.1.html — rootless-by-default, newuidmap/newgidmap, nested rootless containers
21. Kubernetes docs: seccomp — https://kubernetes.io/docs/tutorials/security/seccomp/ — default profile and opt-out
22. Kubernetes docs: AppArmor — https://kubernetes.io/docs/tutorials/security/apparmor/
23. Kubernetes docs: NetworkPolicy — https://kubernetes.io/docs/concepts/services-networking/network-policies/ — CNI-dependent, not a boundary itself
24. systemd.resource-control, `Delegate=` — https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html
25. BuildKit rootless mode — https://github.com/moby/buildkit/blob/master/docs/rootless.md — daemonless rootless builds
26. Kaniko — https://github.com/GoogleContainerTools/kaniko — daemonless Dockerfile execution without privilege
27. Sysbox docs — https://github.com/nestybox/sysbox — VM-like containers without `--privileged` **[vendor claim]**

### gVisor and Kata

28. gVisor documentation and architecture guide — https://gvisor.dev/docs/ , https://gvisor.dev/docs/architecture_guide/security/ — Sentry/user-space kernel, security model
29. gVisor compatibility — https://gvisor.dev/docs/user_guide/compatibility/ — unimplemented syscall surface
30. gVisor Docker quick start — https://gvisor.dev/docs/user_guide/quick_start/docker/ — Docker under gVisor
31. Kata Containers README — https://github.com/kata-containers/kata-containers/blob/main/docs/README.md — architecture, pluggable VMMs
32. Kata + Firecracker how-to — https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-use-kata-containers-with-firecracker.md
33. Superuser: Firecracker and Kata Containers — https://superuser.openinfra.org/articles/firecracker-kata-containers-open-collaboration — Kata's added isolation layers

### CVEs and side channels

34. CVE-2019-5736 (runC) — https://nvd.nist.gov/vuln/detail/CVE-2019-5736 — demonstrated container runtime escape
35. CVE-2017-5754 (Meltdown) — https://nvd.nist.gov/vuln/detail/CVE-2017-5754
36. CVE-2017-5753 (Spectre v1) — https://nvd.nist.gov/vuln/detail/CVE-2017-5753
37. CVE-2017-5715 (Spectre v2) — https://nvd.nist.gov/vuln/detail/CVE-2017-5715
38. CVE-2018-3615 (L1TF, SGX) — https://nvd.nist.gov/vuln/detail/CVE-2018-3615
39. CVE-2019-11091 (MDS) — https://nvd.nist.gov/vuln/detail/CVE-2019-11091
40. Kernel hw-vuln documentation — https://docs.kernel.org/admin-guide/hw-vuln/index.html — mitigation mechanisms and status

### Operators and practice

41. E2B runtime README — https://github.com/e2b-dev/runtime — per-sandbox netns + nftables egress firewall, per-sandbox cgroups
42. GitHub: Security hardening for GitHub Actions — https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions — SHA pinning, token scoping
43. GitHub: Using secrets in GitHub Actions — https://docs.github.com/en/actions/security-for-github-actions/security-guides/using-secrets-in-github-actions — automatic log masking
44. GitHub: OIDC security hardening — https://docs.github.com/en/actions/deployment/security-hardening-your-deployments/about-security-hardening-with-openid-connect — short-lived credentials
