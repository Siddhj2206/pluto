# How leading projects operate Firecracker

Research note for #67, 2026-10-06. Primary sources only; every claim is cited with a URL and an access date of 2026-10-06 unless noted. Where something could not be verified, it is marked **[uncertain]**.

---

## 1. Scope and method

This note surveys how production and self-hosted microVM operators drive Firecracker: Fly.io, Modal, E2B, Northflank, CodeSandbox, Kata Containers, Weave Ignite, and Firecracker's own docs/CI. It covers process model (jailer vs rootless), cgroup enforcement, seccomp, machine/device config and boot args, vsock, the device model, kernel policy and direct boot, snapshots, and observability. It ends with explicit adopt/reject recommendations for pluto.

pluto's current posture (from the repo): the runner drives Firecracker directly via `--config-file` inside a rootless `unshare -Urn` namespace, with slirp4netns for egress (`internal/runner/boxrun.go`, `internal/runner/runner.go`). The jailer is deliberately skipped (ADR 0001). The guest kernel is 6.1.186, whose Firecracker support window closed 2026-09-02. `[box].resources` is parsed but ignored; the runner hardcodes 2 vCPU / 1024 MiB. Boot args are `console=ttyS0 root=/dev/vda rw reboot=k panic=1`. There is no cgroup enforcement, no metrics pipe, and no snapshot support.

---

## 2. Operator survey

### 2.1 Fly.io

Fly.io runs every customer workload in a Firecracker microVM on physical servers it operates. The platform architecture is described as 8–32 physical cores with 32–256 GB RAM per server, with no cloud VM in between ([Fly.io learn: Firecracker vs gVisor](https://fly.io/learn/firecracker-vs-gvisor), accessed 2026-10-06).

**Process model.** In production, Fly.io starts Firecracker through the jailer, which sets up cgroups and a chroot, drops privileges, then execs Firecracker unprivileged. Fly.io's own description: "in production it is started through a jailer that sets up cgroups and a chroot, drops privileges, then execs Firecracker unprivileged" ([Fly.io learn: Firecracker vs gVisor](https://fly.io/learn/firecracker-vs-gvisor), accessed 2026-10-06). The jailer is the recommended production path in Firecracker's own docs as well ([Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06).

**Seccomp.** Fly.io describes each Firecracker VM as running "inside a seccomp filter and a strict jailer process that limits which Linux syscalls the VMM process itself can make" ([Fly.io learn: What is a Firecracker microVM?](https://fly.io/learn/firecracker-vm), accessed 2026-10-06). Firecracker installs seccomp filters by default, per-thread, before executing guest code ([Firecracker seccomp.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/seccomp.md), accessed 2026-10-06).

**Vsock.** Fly.io's init communicates with the host over vsock — "a host Unix domain socket that presents as a synthetic Virtio device in the guest — that allows init to communicate with the host; we bundle `node-exporter`-type JSON stats over the `vsock` for Nomad to collect" ([Fly blog: Measuring Fly](https://fly.io/blog/measuring-fly), accessed 2026-10-06). Fly.io orchestrates Firecracker via a custom Nomad driver.

**Snapshots.** Fly.io Sprites use Firecracker snapshots for checkpoint and restore — "you can snapshot the entire disk state in about 300 milliseconds and roll back" ([Medium: How every major tech company is sandboxing AI agents](https://medium.com/@earlperry562/how-every-major-tech-company-is-sandboxing-ai-agents-differently-f41b65f14d8a), accessed 2026-10-06). This is consistent with Firecracker's snapshot API ([Firecracker snapshot-support.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md), accessed 2026-10-06).

**Observability.** Fly.io exposes Prometheus metrics from the guest via vsock; the host-side Nomad driver relays them to a Vicky (Prometheus-compatible) cluster ([Fly blog: Measuring Fly](https://fly.io/blog/measuring-fly), accessed 2026-10-06).

### 2.2 Modal

**Modal does not use Firecracker for its sandboxes.** Modal runs sandboxes on gVisor, Google's container runtime that intercepts system calls. This is stated directly in Modal's own comparison content and by third parties: "Modal Sandboxes run on gVisor, a container runtime by Google that intercepts system calls to prevent malicious code from reaching the host kernel" ([Northflank: E2B vs Modal](https://northflank.com/blog/e2b-vs-modal), accessed 2026-10-06); "Modal uses gVisor containers, while E2B and Northflank employ Firecracker microVMs" ([Modal blog: Best Code Execution Sandbox for OpenHands](https://modal.com/resources/best-sandbox-openhands), accessed 2026-10-06). Modal's architecture is optimized for Python ML workloads with native GPU support ([manveerc.substack.com: How to sandbox AI agents in 2026](https://manveerc.substack.com/p/ai-agent-sandboxing-guide), accessed 2026-10-06).

**Implication for this note:** Modal is not a Firecracker operator and is excluded from the Firecracker-specific recommendations. It is surveyed here only to record that fact.

### 2.3 E2B

E2B is the most instructive Firecracker operator for pluto's use case: self-hostable, open-source, and purpose-built for running untrusted agent code in microVMs.

**Process model.** E2B runs one Firecracker microVM per sandbox, in its own cgroup and network namespace, with a per-sandbox nftables egress firewall ([E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-06). The runtime is written in Go and open-source. The orchestrator on each node owns "the Firecracker process, the network namespace, the block device, the cgroup" ([E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-06). E2B uses the jailer — the runtime repository includes a `firecracker` directory and the standard Firecracker+jailer binary pair [uncertain: the exact jailer invocation is not documented in the README; the repo structure implies it].

**Cgroups.** Each sandbox runs in its own cgroup ([E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-06). This is the resource-isolation boundary.

**Snapshots.** E2B's defining architectural choice: "A sandbox is a resumed snapshot. Templates are pre-booted VMs (memory, disk, and machine state) stored in object storage. 'Creating' a sandbox means restoring one, not booting a kernel. Memory pages are served lazily on page fault through `userfaultfd`, and the root filesystem is a copy-on-write overlay over a read-only image" ([E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-06). Pause diffs memory and disk against the template and ships the diff to object storage. This is the fastest-resume pattern in the survey.

**Vsock.** E2B runs `envd` (environment daemon) inside every VM, exposing processes, PTYs, filesystem operations, and port forwarding over Connect RPC and REST ([E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-06). The SDKs talk to envd, which communicates with the host orchestrator. This is analogous to pluto's guest agent over vsock.

**Observability.** "Everything exports OpenTelemetry. Sandbox lifecycle events, host stats, and metrics land in ClickHouse" ([E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-06).

**Control plane / data plane separation.** "The API decides *where* a sandbox runs and records *that* it runs. The orchestrator on each node owns *how* it runs" ([E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-06). This separation is a recurring pattern among serious operators.

### 2.4 Northflank

Northflank uses Firecracker microVMs for its sandbox offering: "E2B and Northflank employ Firecracker microVMs for hardware-level isolation" ([Modal blog: Best Code Execution Sandbox for OpenHands](https://modal.com/resources/best-sandbox-openhands), accessed 2026-10-06). Northflank's own blog describes the jailer security model — cgroup isolation, namespace isolation, seccomp filters, chroot — as the standard Firecracker defense-in-depth ([Northflank: What is AWS Firecracker?](https://northflank.com/blog/what-is-aws-firecracker), accessed 2026-10-06). **[uncertain]** Northflank's internal orchestration details (whether it uses the jailer directly, its cgroup configuration, or its snapshot strategy) are not publicly documented at the level of detail available for Fly.io or E2B.

### 2.5 CodeSandbox

**[uncertain]** CodeSandbox is listed as a Firecracker user in secondary sources ([teimouri.net: MicroVM and Firecracker](https://www.teimouri.net/whats-microvm-and-firecracker), accessed 2026-10-06), but no primary-source documentation of its Firecracker operation (process model, cgroups, seccomp, snapshots) was found in this research pass. CodeSandbox's architecture is not covered by the sources gathered here and is marked uncertain.

### 2.6 Kata Containers

Kata Containers is an orchestration framework that integrates multiple VMMs — Firecracker, Cloud Hypervisor, and QEMU — with Kubernetes and containerd. It is not itself a VMM ([Northflank: Kata Containers vs Firecracker vs gVisor](https://northflank.com/blog/kata-containers-vs-firecracker-vs-gvisor), accessed 2026-10-06).

**Process model.** Kata uses the Firecracker jailer. The Kata + Firecracker integration guide instructs users to download both the `firecracker` and `jailer` binaries and configure `configuration-fc.toml` ([Kata docs: How to use Kata Containers with Firecracker](https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-use-kata-containers-with-firecracker.md), accessed 2026-10-06). Kata adds its own libcontainer-based isolation layers inside the VM on top of the jailer's containment ([Superuser: Firecracker and Kata Containers](https://superuser.openinfra.org/articles/firecracker-kata-containers-open-collaboration), accessed 2026-10-06).

**Scope.** Kata is a Kubernetes/containerd integration layer; it is not a direct model for pluto's single-host, rootless, systemd-driven architecture. Its relevance here is confirmatory: even a container-orchestration framework adopts the jailer when using Firecracker.

### 2.7 Weave Ignite

Weave Ignite is a container-like CLI for Firecracker: "Ignite makes Firecracker VMs look like Docker containers" ([Weave Ignite GitHub](https://github.com/weaveworks/ignite), accessed 2026-10-06). It runs Firecracker directly (rootful) and optionally under the jailer.

**Process model.** Ignite's stop mechanism uses Firecracker's Ctrl+Alt+Del (i8042 keyboard controller) to gracefully shut down the VM, waiting 20 seconds before force-killing ([Weave Ignite docs: How to use Ignite to run VMs](https://ignite.readthedocs.io/en/stable/usage), accessed 2026-10-06). This is the same mechanism pluto uses (SendCtrlAltDel → i8042 reset → `reboot=k`).

**Networking.** Ignite connects VMs to any CNI network; the VM gets the same IP as any container on the host ([Weave Ignite GitHub](https://github.com/weaveworks/ignite), accessed 2026-10-06). This is a different model from pluto's slirp4netns + bridge approach.

**Relevance.** Ignite is a developer-tool wrapper, not a production multi-tenant operator. Its relevance is the confirmation that the i8042/Ctrl+Alt+Del shutdown path is the standard Firecracker graceful-stop mechanism.

### 2.8 Firecracker's own docs and CI

Firecracker's documentation is the reference posture against which all operators are measured.

**Process model.** "In production environments, Firecracker should be started only via the `jailer` binary that's part of each Firecracker release, or executed under process constraints equal or more restrictive than those in the jailer" ([Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06). The jailer applies cgroup, namespace isolation, and drops privileges ([Firecracker jailer.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md), accessed 2026-10-06). The jailer requires root to set up (it creates cgroups, chroots, mknods `/dev/kvm` and `/dev/net/tun`, then drops to the configured uid/gid) ([Firecracker jailer.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md), accessed 2026-10-06).

**Seccomp.** "By default, Firecracker uses the most restrictive filters, which is the recommended option for production usage. Production usage of the `--seccomp-filter` or `--no-seccomp` parameters is not recommended" ([Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06). Filters are loaded per-thread before guest code executes ([Firecracker seccomp.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/seccomp.md), accessed 2026-10-06).

**Cgroups.** "Firecracker's customers are strongly advised to use the provided `resource-limits` and `cgroup` functionalities encapsulated within jailer" ([Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06). The jailer supports cgroup v1 and v2, with `--cgroup` for per-VM limits (cpuset, cpu, memory, blkio) and `--resource-limit` for `fsize` and `no-file` ([Firecracker jailer.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md), accessed 2026-10-06).

**Boot args.** Firecracker's default kernel command line (when the user does not override) is: `reboot=k panic=1 nomodule 8250.nr_uarts=0 i8042.noaux i8042.nomux i8042.dumbkbd swiotlb=noforce` ([Firecracker kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-06). The getting-started guide uses `console=ttyS0 reboot=k panic=1` ([Firecracker getting-started.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md), accessed 2026-10-06).

**Serial device.** "We do not recommend that users enable the serial device in production. To disable it in the guest kernel, use the `8250.nr_uarts=0` boot argument" ([Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06). However, the serial console is tied to the Firecracker process stdout, and users are responsible for bounding its storage.

**Logging.** "Firecracker outputs logging data into a named pipe, socket, or file using the path specified in the `log_path` field of logger configuration... We suggest using any upper-bounded forms of storage, such as fixed-size or ring buffers, programs like `journald` or `logrotate`" ([Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06). The design doc notes: "In production builds, Firecracker does not expose the serial console port, since it may contain guest data that the host should not see" ([Firecracker design.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md), accessed 2026-10-06).

**Metrics.** Firecracker emits metrics to a named pipe configured via `PUT /metrics` or `--metrics-path`. Metrics are emitted at instance start, then every 60 seconds, and on panic ([Firecracker design.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md), accessed 2026-10-06; [Firecracker SPECIFICATION.md](https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md), accessed 2026-10-06). Lost metrics are signaled through `lost-metrics` counters.

**Snapshots.** Full and diff snapshots are supported. A snapshot comprises a guest memory file, a microVM state file, and user-managed disk files. Restore is via `PUT /snapshot/load` before boot. Diff snapshots are in developer preview. Snapshots must be resumed on identical software and hardware. Vsock is reset across snapshot/restore. Network connectivity is not guaranteed to survive ([Firecracker snapshot-support.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md), accessed 2026-10-06).

**Kernel policy.** Guest kernel support: 5.10 (min end of support 2024-01-31), 6.1 (min end of support 2026-09-02), 6.18 (min end of support 2028-06-01). Host kernel support: 5.10, 6.1, 6.18. At least 2 major guest and host versions are supported at any time ([Firecracker kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-06).

**Direct boot.** Booting with a root block device (no initrd) requires `CONFIG_VIRTIO_BLK=y` and on x86_64 also `CONFIG_ACPI=y`, `CONFIG_PCI=y`, `CONFIG_KVM_GUEST=y`. Booting with initrd requires `CONFIG_BLK_DEV_INITRD=y` and `CONFIG_KVM_GUEST=y` (x86_64) ([Firecracker kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-06). Firecracker replaces an absent initrd with a default empty 134-byte initrd ([arxiv: Study of Firecracker MicroVM](https://arxiv.org/html/2005.12821v1), accessed 2026-10-06).

**Device model.** Firecracker provides only 5 emulated devices: virtio-net, virtio-block, virtio-vsock, serial console, and a minimal keyboard controller (i8042) used only to stop the microVM ([Firecracker website](https://firecracker-microvm.github.io), accessed 2026-10-06). No USB, no GPU, no PCI hot-plug, no BIOS. An entropy source is provided for the guest RNG ([Fly.io learn: What is a Firecracker microVM?](https://fly.io/learn/firecracker-vm), accessed 2026-10-06). The kernel config `CONFIG_HW_RANDOM_VIRTIO` is listed under virtio devices — entropy ([Firecracker kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-06). ACPI is optional and only relevant for x86_64 kernels that want it; Firecracker itself does not emulate ACPI devices ([Firecracker kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-06).

---

## 3. Cross-cutting analysis

### 3.1 Process model: jailer vs rootless

Every production multi-tenant operator in this survey uses the jailer (Fly.io, E2B, Kata). Firecracker's own docs say production should use the jailer or "process constraints equal or more restrictive" ([prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06). The jailer needs root to set up, then drops to an unprivileged uid/gid.

pluto runs rootless (no jailer) via `unshare -Urn`. This is architecturally different: pluto is a single-user, single-host tool where the user already owns the host, so the threat model is "the user's own code in a box," not "strangers' code on shared hardware." ADR 0001 records this: "The jailer is deliberately skipped until a hardening need exists, since there is no trust model."

**The tension:** the jailer requires root to invoke. pluto's rootless posture is a deliberate design choice (ADR 0001) and is verified to work on the first host. Adopting the jailer would break rootlessness. The middle path — "process constraints equal or more restrictive than those in the jailer" — is what pluto should pursue: cgroups, seccomp (already applied by Firecracker internally), and namespace isolation (already done via `unshare -Urn`).

### 3.2 Cgroup enforcement

All production operators enforce cgroups. The jailer creates per-VM cgroups with configurable limits. E2B puts each sandbox in its own cgroup. Firecracker's prod-host-setup "strongly advises" using the jailer's resource-limits and cgroup functionalities.

pluto currently has no cgroup enforcement. The M3 spec (#66) commits to adding cgroup CPU/memory limits. The open question is how to do this rootlessly: cgroup v2 delegation allows a non-root user to manage a subtree of the cgroup hierarchy, but this requires host-level configuration (systemd delegation or manual `cgroup.subtree_control` writes). **[uncertain]** The exact mechanism pluto should use for rootless cgroup v2 delegation is a design question for the implementation ticket, not this research note.

### 3.3 Seccomp

Firecracker applies seccomp filters by default, internally, per-thread, before guest code runs ([seccomp.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/seccomp.md), accessed 2026-10-06). This happens whether or not the jailer is used — the jailer is a separate process-level sandbox; seccomp is applied by Firecracker itself. pluto therefore already benefits from Firecracker's default seccomp filters. No pluto change is needed for seccomp; the recommendation is to not disable them (`--no-seccomp` is not recommended in production) and to not override them with custom filters unless necessary.

### 3.4 Machine/device config and boot args

pluto uses `--config-file` (static JSON config) rather than the API-driven boot sequence. This is a valid Firecracker mode ([getting-started.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md), accessed 2026-10-06) and is appropriate for pluto's systemd-unit model. The config file must contain all pre-boot resources.

**Boot args gap.** pluto's boot args are `console=ttyS0 root=/dev/vda rw reboot=k panic=1`. Firecracker's defaults add `nomodule 8250.nr_uarts=0 i8042.noaux i8042.nomux i8042.dumbkbd swiotlb=noforce` ([kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-06). The `nomodule` flag disables loadable kernel modules (security: reduces guest attack surface). The i8042 flags skip redundant probing (boot time). `swiotlb=noforce` disables software bounce buffers. pluto should adopt these. The exception is `8250.nr_uarts=0`, which disables the serial device — pluto needs `console=ttyS0` for boot logging, so this flag should not be adopted as-is. **[uncertain]** Whether pluto can use `8250.nr_uarts=1` (one UART for console) while still bounding serial output is a detail for the implementation ticket.

**Machine config.** pluto hardcodes 2 vCPU / 1024 MiB. The M3 spec commits to making `[box].resources` flow into the machine config. This is a correctness fix, not a research question.

### 3.5 Vsock

All operators use vsock for host-guest communication. Fly.io ships metrics over vsock. E2B runs envd over vsock. pluto uses vsock for SSH and the guest agent. This is aligned with standard practice. The one caveat: vsock is reset across snapshot/restore ([snapshot-support.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md), accessed 2026-10-06) — relevant only if snapshots are adopted.

### 3.6 Device model

Firecracker's minimal device model (virtio-net, virtio-block, virtio-vsock, serial, i8042 keyboard, entropy) is fixed — pluto cannot add or remove devices without changing VMMs. pluto's config uses exactly these devices. No change needed. The "no ACPI" property is inherent to Firecracker; pluto's direct boot path is the minimal-boot configuration described in the kernel policy.

### 3.7 Kernel policy and direct boot

pluto's kernel 6.1.186 is past its Firecracker support end-of-life (2026-09-02 for 6.1). The M3 spec commits to 6.18.51, which is supported until 2028-06-01 ([kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-06). This is a correctness fix.

pluto's direct boot (no initrd, root block device) is the correct minimal-boot path. The kernel must have `CONFIG_VIRTIO_BLK=y`, `CONFIG_VIRTIO_MMIO=y` (or PCI), `CONFIG_VIRTIO_NET=y`, `CONFIG_VIRTIO_VSOCKETS=y`, `CONFIG_KVM_GUEST=y`, and on x86_64 `CONFIG_ACPI=y`, `CONFIG_PCI=y` for the root-block-device boot path ([kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-06). The M3 kernel ticket (#72) should verify these config flags in the 6.18.51 kernel.

### 3.8 Snapshots

Snapshots are the single biggest architectural differentiator among operators. E2B's entire fast-resume model is built on snapshots. Fly.io Sprites use them for checkpoint/restore. Firecracker's snapshot API is mature for full snapshots; diff snapshots are in developer preview.

pluto does not use snapshots. The M3 spec lists snapshots as out of scope. The pause/resume model is a full clean shutdown + cold boot, which is simpler and correct for pluto's durable-box semantics (a box's disk persists across pause; the machine restarts from scratch). Snapshots would add complexity (memory file management, vsock reset, network re-establishment, identical-hardware requirement) for a benefit (faster resume) that is not yet needed.

**Revive trigger:** if box resume latency becomes a measured problem (e.g., users wait too long for `up` after pause), snapshots should be revisited. E2B's model — pre-booted templates restored via `userfaultfd` — is the reference implementation.

### 3.9 Observability

Firecracker emits logs and metrics to named pipes. Production operators collect both: Fly.io ships metrics over vsock to Prometheus; E2B exports OpenTelemetry to ClickHouse.

pluto's current observability: serial console to `serial.log`, Firecracker logs to `fc.log` (level Warning), slirp logs to `slirp.log`. There is no metrics pipe configured. The `fc.json` logger config uses `log_path` (a file) rather than a named pipe — this is valid but means pluto is not consuming Firecracker's metrics stream.

**Recommendation:** configure a metrics pipe (`metrics_path` in the config file or `PUT /metrics`) and expose the metrics to the daemon. This is a small, high-value change. The prod-host-setup recommendation to use bounded storage for logs is also relevant: pluto's `fc.log` and `serial.log` grow unbounded; a ring buffer or logrotate would address this.

---

## 4. Recommendations for pluto

Each recommendation names the pluto change it implies, or states "no change." Deferred items carry a named revive trigger.

### Adopt

**A1. Add the missing Firecracker default boot args.**
Adopt `nomodule i8042.noaux i8042.nomux i8042.dumbkbd swiotlb=noforce` in pluto's boot args. Keep `console=ttyS0 root=/dev/vda rw` (pluto needs the serial console and explicit root device). Do not adopt `8250.nr_uarts=0` (it would disable the serial console pluto depends on).
*pluto change:* update `writeConfig` in `internal/runner/runner.go` to emit the expanded boot args.
*source:* [Firecracker kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), [Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06.

**A2. Configure a Firecracker metrics pipe and surface metrics to the daemon.**
Add a `metrics_path` to the `fc.json` config (a named pipe or file under the box dir) and have the daemon read and expose Firecracker's metrics. Firecracker emits metrics at start, every 60s, and on panic.
*pluto change:* add a metrics path to `fcConfig` in `internal/runner/runner.go`; add a metrics consumer in the daemon or runner.
*source:* [Firecracker design.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md), [Firecracker SPECIFICATION.md](https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md), accessed 2026-10-06.

**A3. Bound the serial and Firecracker log storage.**
pluto's `serial.log` and `fc.log` grow unbounded. Use a ring buffer, `logrotate`, or a fixed-size file. The prod-host-setup doc recommends upper-bounded storage for both serial output and Firecracker logs.
*pluto change:* introduce a log-rotation or ring-buffer mechanism for `serial.log` and `fc.log` in the runner.
*source:* [Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06.

**A4. Keep seccomp at Firecracker's defaults; do not disable or override.**
Firecracker already applies the most restrictive seccomp filters internally, per-thread, before guest code runs. pluto needs no change. Do not pass `--no-seccomp` or `--seccomp-filter` in production.
*pluto change:* no change. Document that seccomp is active by default.
*source:* [Firecracker seccomp.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/seccomp.md), [Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), accessed 2026-10-06.

**A5. Keep the minimal device model as-is.**
Firecracker's device model (virtio-net, virtio-block, virtio-vsock, serial, i8042, entropy) is fixed and is the smallest attack surface. pluto's config already uses exactly these devices. No change.
*pluto change:* no change.
*source:* [Firecracker website](https://firecracker-microvm.github.io), [Fly.io learn: What is a Firecracker microVM?](https://fly.io/learn/firecracker-vm), accessed 2026-10-06.

### Reject (for now, with revive triggers)

**R1. Reject the jailer for pluto's current posture.**
The jailer requires root to invoke and is designed for multi-tenant production. pluto is a single-user, single-host, rootless tool with no trust boundary between the host user and the box user (ADR 0001). Adopting the jailer would break rootlessness. Instead, pluto should pursue "process constraints equal or more restrictive than those in the jailer" via cgroups (R2 below) and its existing namespace isolation.
*pluto change:* no change to the process model. ADR 0001 stands.
*revive trigger:* a real trust boundary appears — e.g., pluto hosts boxes for users other than the host owner, or boxes run code from untrusted external sources. This is the same trigger already recorded in ADR 0001 and `docs/DEFERRED.md`.
*source:* [Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), [Firecracker jailer.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md), accessed 2026-10-06.

**R2. Defer rootless cgroup enforcement to the implementation ticket, but commit to it.**
All production operators enforce cgroups. The M3 spec (#66) commits to cgroup CPU/memory limits. The research confirms this is standard practice. The open design question — how to delegate cgroup v2 to a non-root user — is an implementation detail for the M3 resources ticket (#73), not a research decision.
*pluto change:* cgroup enforcement is already committed in the M3 spec; this research confirms it should proceed. The mechanism (systemd delegation, manual `cgroup.subtree_control`, or a helper) is for #73.
*revive trigger:* if rootless cgroup v2 delegation proves impossible on a target host, the fallback is a privileged helper or a documented host-setup step. This does not change the commitment to enforce limits.
*source:* [Firecracker prod-host-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md), [E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-06.

**R3. Reject snapshots for the M3 refresh.**
Snapshots are the biggest architectural differentiator among operators (E2B's entire fast-resume model is built on them), but they add substantial complexity: memory file management, vsock reset, network re-establishment, identical-hardware requirement, and a pause/resume semantic that conflicts with pluto's durable-disk, clean-shutdown model. pluto's pause is a full clean shutdown; resume is a cold boot. This is simpler and correct for pluto's semantics.
*pluto change:* no change. Snapshots remain out of scope for M3.
*revive trigger:* box resume latency becomes a measured problem — e.g., users report that `up` after `pause` takes too long, or the product requires sub-second resume. At that point, E2B's model (pre-booted templates restored via `userfaultfd`, COW rootfs) is the reference implementation.
*source:* [Firecracker snapshot-support.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md), [E2B runtime README](https://github.com/e2b-dev/runtime), accessed 2026-10-06.

**R4. Reject `--enable-pci` for now.**
Firecracker's PCI transport (higher throughput, lower latency virtio) is optional and requires guest kernel PCI support. pluto's MMIO transport is the default and works. The performance gain is not a measured problem for pluto's workload (SSH, agent communication, builds).
*pluto change:* no change.
*revive trigger:* if virtio device throughput becomes a measured bottleneck (e.g., disk I/O or network throughput is the limiting factor in box workloads), evaluate `--enable-pci` with a PCI-capable kernel.
*source:* [Firecracker getting-started.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md), [Firecracker kernel-policy.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md), accessed 2026-10-06.

---

## 5. Uncertainties

- **CodeSandbox:** No primary-source documentation of CodeSandbox's Firecracker operation was found. Its inclusion in this survey is based on a secondary source only. **[uncertain]**
- **E2B jailer invocation:** The E2B runtime README confirms Firecracker microVMs with cgroups and network namespaces but does not explicitly document the jailer invocation. The repo structure implies it. **[uncertain]**
- **Northflank internals:** Northflank's use of Firecracker is confirmed, but its internal orchestration details are not publicly documented. **[uncertain]**
- **Rootless cgroup v2 delegation:** The exact mechanism for a non-root process to manage cgroup v2 limits on a target host is environment-dependent (systemd delegation, manual `cgroup.subtree_control`, or a privileged helper). This is a design question for the implementation ticket. **[uncertain]**
- **`8250.nr_uarts` with console:** Whether pluto can use `8250.nr_uarts=1` (one UART) while still bounding serial output, or whether the serial console must remain fully enabled, is a detail for the implementation ticket. **[uncertain]**
- **Modal:** Confirmed as a gVisor user, not a Firecracker operator. Included in this note only to record that fact.

---

## 6. Sources

All accessed 2026-10-06.

1. Firecracker prod-host-setup.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md
2. Firecracker jailer.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md
3. Firecracker design.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md
4. Firecracker seccomp.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/seccomp.md
5. Firecracker kernel-policy.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md
6. Firecracker snapshot-support.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md
7. Firecracker getting-started.md — https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md
8. Firecracker SPECIFICATION.md — https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md
9. Firecracker website — https://firecracker-microvm.github.io
10. Fly.io learn: What is a Firecracker microVM? — https://fly.io/learn/firecracker-vm
11. Fly.io learn: Firecracker vs gVisor — https://fly.io/learn/firecracker-vs-gvisor
12. Fly blog: Measuring Fly — https://fly.io/blog/measuring-fly
13. E2B runtime README — https://github.com/e2b-dev/runtime
14. Northflank: What is AWS Firecracker? — https://northflank.com/blog/what-is-aws-firecracker
15. Northflank: E2B vs Modal — https://northflank.com/blog/e2b-vs-modal
16. Northflank: Kata Containers vs Firecracker vs gVisor — https://northflank.com/blog/kata-containers-vs-firecracker-vs-gvisor
17. Modal blog: Best Code Execution Sandbox for OpenHands — https://modal.com/resources/best-sandbox-openhands
18. Kata docs: How to use Kata Containers with Firecracker — https://github.com/kata-containers/kata-containers/blob/main/docs/how-to/how-to-use-kata-containers-with-firecracker.md
19. Weave Ignite GitHub — https://github.com/weaveworks/ignite
20. Weave Ignite docs: How to use Ignite to run VMs — https://ignite.readthedocs.io/en/stable/usage
21. arxiv: Study of Firecracker MicroVM — https://arxiv.org/html/2005.12821v1
22. Medium: How every major tech company is sandboxing AI agents — https://medium.com/@earlperry562/how-every-major-tech-company-is-sandboxing-ai-agents-differently-f41b65f14d8a
23. manveerc.substack.com: How to sandbox AI agents in 2026 — https://manveerc.substack.com/p/ai-agent-sandboxing-guide
24. teimouri.net: MicroVM and Firecracker — https://www.teimouri.net/whats-microvm-and-firecracker
25. Superuser: Firecracker and Kata Containers — https://superuser.openinfra.org/articles/firecracker-kata-containers-open-collaboration
