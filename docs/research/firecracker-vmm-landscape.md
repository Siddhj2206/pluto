# Firecracker and the VMM landscape

Research for pluto ticket [#2](https://github.com/Siddhj2206/pluto/issues/2). Question: what does
the VMM landscape actually look like for pluto's runner, and what do we steal?

Method: primary sources only (project docs, source, specs, first-party reports), plus a bounded
smoke boot on the first host (Section 6.6). All URLs were fetched on 2026-10-04. Where only
vendor claims or indirect evidence exist, that is stated. The smoke test is a one-off observation,
not a benchmark.

---

## TL;DR

- **Firecracker fits pluto almost exactly.** Direct kernel boot with no BIOS/bootloader, a tiny
  virtio-only device model, a per-VM HTTP API over a Unix socket, vsock control channels, file
  backed virtio-blk, and no root requirement beyond `/dev/kvm` plus TAP creation. It is the
  implementation under Lambda, Fargate, Fly Machines, E2B and Hotcell, so the operational patterns
  for "fleet of small machines" are well documented in public.
- **Disk-only pause maps cleanly.** Firecracker's own snapshot/restore machinery is a memory
  snapshot feature pluto does not need. Under pluto's model a paused box is just a dead VMM plus
  its ext4 image file. One real caveat was found: Firecracker block devices default to
  `cache_type: Unsafe`, which silently drops guest flushes. A durable box must set
  `cache_type: Writeback` (Section 1.7).
- **Networking without root is real and was verified.** Firecracker docs assume `sudo` for TAP and
  NAT, but a TAP can be created inside a rootless user+network namespace (`unshare -Urn`) and
  Firecracker can attach to it; the guest pinged the host through it with no sudo anywhere
  (Section 6.6). For actual connectivity, `pasta`/`passt` and `slirp4netns` are capability-free
  user-mode options; Hotcell goes further and gives microVMs *no NIC at all*, using vsock only.
- **Verified boot on the first host**: Firecracker v1.17.0 booted the CI kernel `vmlinux-6.18.51`
  with a 2 MiB initramfs to userspace in **428–441 ms** (guest boot timer, serial console on), with
  ~18 ms from VMM start to `InstanceStart`, and a 128 MiB guest using ~58 MB RSS. No sudo, no
  system changes, no processes left behind.
- **Cloud Hypervisor is the capability fallback, not the default.** It adds UEFI firmware boot,
  virtio-fs, CPU/memory/PCI hotplug, VFIO, Windows, live migration and an offloaded
  snapshot/restore daemon, at the cost of a much larger device model and a PCI-centric design.
  Useful when a box genuinely needs UEFI or a shared filesystem; not needed for pluto M0.
- **QEMU microvm** is a debugging fallback (virtio-mmio only, no PCI/ACPI/hotplug, must boot from
  a host-side kernel). **libkrun** is the embeddable, cross-platform alternative (used by
  microsandbox), whose security model explicitly says guest and VMM are one security context —
  acceptable under pluto's no-trust model, but Firecracker has the stronger fleet precedent.
- **Recommendation**: Firecracker as the primary runner behind a thin seam (process + Unix-socket
  API + disk image + vsock), with Cloud Hypervisor and QEMU microvm documented as fallbacks.
  Run unjailed initially (no root) or rootless in a user namespace; treat jailer as later
  hardening, not a milestone.

---

## 1. Firecracker

### 1.1 Boot path: direct kernel boot

Firecracker has no BIOS and no bootloader. It loads the kernel directly using the Linux 64-bit
boot protocol and boots a minimal device model; this is why the start path is short and why
arbitrary kernels (and Windows) are not supported
([design](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md),
[NSDI 2020](https://www.usenix.org/system/files/nsdi20-paper-agache.pdf)).

Kernel image formats (per the current
[rootfs-and-kernel-setup doc](https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md)):

- x86_64: uncompressed ELF `vmlinux` **or** `bzImage`; format is auto-detected. `bzImage` is
  supported for compatibility, but it is decompressed inside the guest, costing boot time and host
  memory; `vmlinux` is recommended.
- aarch64: PE-format `Image`.
- An initrd/initramfs can be supplied (`initrd_path`) for boot. A rootfs block device is optional
  when booting from initrd — though the JSON config file still requires a `drives` field, even if
  empty (observed with v1.17.0; see Section 6.6).
- Firecracker appends its own kernel command line defaults: `reboot=k panic=1 nomodule
  8250.nr_uarts=0 i8042.noaux i8042.nomux i8042.dumbkbd swiotlb=noforce`, plus `pci=off` unless
  PCI mode is enabled ([kernel policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)).
- x86_64 boots via ACPI by default now; the legacy MPTable + `virtio_mmio.device=` cmdline path is
  deprecated. ACPI requires `CONFIG_ACPI=y` and `CONFIG_PCI=y` in the guest even though
  Firecracker has no PCI devices (unless `--enable-pci`; see below).

Guest-rootfs requirements: a filesystem image the guest kernel can mount, with at least an init
system; `/sbin/init` is the minimal contract. The docs' recipe is ext4 built from a squashfs tree
with `mkfs.ext4 -d`, or plain `mkfs.ext4` + copy, or the CI recipe for minimized Ubuntu
([rootfs-and-kernel-setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md)).
Any file system the guest kernel supports works; support must be compiled in.

### 1.2 Device model and API

The device set is deliberately small. The
[device API matrix](https://github.com/firecracker-microvm/firecracker/blob/main/docs/device-api.md)
and [design doc](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md) list:

- `virtio-block` (file-backed, one or more drives; root device by `is_root_device`)
- `virtio-net` (TAP-backed)
- `virtio-vsock` (host Unix socket ↔ guest AF_VSOCK)
- serial console, partial i8042 keyboard controller (used for reboot/reset)
- optional: `virtio-balloon`, `virtio-rng`, `virtio-pmem`, `virtio-mem` (memory hotplug), vhost-user-block
- MMDS: a metadata service reachable from the guest over the network path (requires `virtio-net`)

There is no virtio-fs, no GPU, no USB, no PCI by default, and no general passthrough. The current
release documents a **developer-preview PCI transport** (`--enable-pci`) with higher virtio
throughput, and a **developer-preview device hotplug** for virtio-block/net/pmem over PCI
([device-hotplug](https://github.com/firecracker-microvm/firecracker/blob/main/docs/device-hotplug.md),
[getting-started](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)).
Hotplug has no guest notification: the guest must rescan the PCI bus.

API model:

- One Firecracker process = one microVM, with API, VMM and per-vCPU threads. The API is an HTTP
  server on a **Unix socket** (`--api-sock`), with an OpenAPI/Swagger spec in-tree
  ([design](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md),
  [swagger](https://github.com/firecracker-microvm/firecracker/blob/main/src/firecracker/swagger/firecracker.yaml)).
- Typical flow: `PUT /boot-source`, `PUT /drives/...`, `PUT /machine-config`,
  `PUT /network-interfaces/...`, `PUT /vsock`, then `PUT /actions {"action_type":
  "InstanceStart"}`. Requests are handled asynchronously; the getting-started guide waits 15 ms
  before `InstanceStart`
  ([getting-started](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)).
- A static JSON config file (`--config-file`) can do all pre-boot configuration and start the VM;
  the API socket remains available afterwards for post-boot operations.
- Post-boot API: pause/resume, balloon target, memory hotplug, PCI device hotplug (preview),
  snapshot create/load, vsock UDS override, metrics flush
  ([swagger endpoints](https://github.com/firecracker-microvm/firecracker/blob/main/src/firecracker/swagger/firecracker.yaml)).
- Machine limits in the v1.17.0 spec: `vcpu_count` minimum 1, **maximum 32**; memory is
  `mem_size_mib` with no documented maximum in the spec. The design doc says microVMs with any
  combination of vCPU (up to 32) and memory, and claims a steady mutation rate of 5 microVMs per
  host core per second on a minimal kernel
  ([design](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md)).
- Logs and metrics are emitted to named pipes passed via API; metrics every 60 s and on start/panic
  ([design](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md)).
- vsock is host-mediated: the host connects to a Unix socket, sends `CONNECT <port>\n`; guest
  initiated connections go to `<uds_path>_<port>`. No vhost kernel data path. Host kernel needs
  `CONFIG_VHOST_VSOCK=m`; guest needs `CONFIG_VIRTIO_VSOCKETS=y`
  ([vsock](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md)).

### 1.3 Networking and root requirements

Firecracker's only network backend is TAP ("currently ... TUN/TAP ... with no multi queue
support"), and it does no filtering: egress must be filtered on the host
([network-setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md),
[design](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md)).

The documented setup uses `sudo` for: creating the TAP, assigning addresses, enabling IP
forwarding, and adding nft/iptables NAT and forwarding rules
([network-setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md)).
Three documented routing styles: NAT, bridge (LAN-visible), and namespaced NAT for clones. Guest
networking can be configured statically via kernel cmdline (`ip=`), avoiding `iproute2` in the
guest.

Root requirements, unpacked:

- `/dev/kvm` read/write. On the first host it is world-rw (`crw-rw-rw- root kvm`), so **no KVM
  privilege work is needed** (observed; see Section 6.5).
- TAP creation requires `CAP_NET_ADMIN`. In the host namespace that means root; inside a new
  user+network namespace it does not. This was verified: `unshare -Urn` + `ip tuntap add` worked,
  and Firecracker running inside that namespace attached to the TAP and passed guest↔host traffic
  (Section 6.6).
- Guest↔host and guest↔internet connectivity inside a rootless namespace can be provided by
  capability-free user-mode stacks: **pasta/passt** ("doesn't require any capabilities or
  privileges"; `pasta` maps namespace traffic over Layer-4 sockets, creating a TAP inside the
  namespace) ([passt](https://passt.top/passt/about/)), or **slirp4netns** ("connecting a TAP
  device in a network namespace to the usermode TCP/IP stack ... completely unprivileged")
  ([slirp4netns](https://github.com/rootless-containers/slirp4netns)).
- The third option is no network device at all: control and egress over vsock (Hotcell's choice,
  Section 4.2). Firecracker supports this today.

Known host-level side effects to plan for if NAT is used: enable forwarding, per-VM nft rules, and
SELinux can regulate Unix sockets on Fedora-family hosts
([network-setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md),
[getting-started troubleshooting](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)).
The first host runs SELinux in Enforcing mode (observed, Section 6.5).

### 1.4 Jailer

The jailer is the documented production wrapper: it creates cgroups, chroots into a per-VM
directory, joins network/PID namespaces, drops to a supplied uid/gid, and execs Firecracker, which
then runs unprivileged with seccomp filters installed before guest code executes
([jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md),
[design](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md),
[prod-host-setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)).

Operational notes for pluto:

- Setting up cgroups/chroot/namespaces requires elevated permissions at launch time. The jailer is
  meant to be run by a privileged third party; the Firecracker process itself can then be
  non-root ([jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md)).
- On cgroup-v2-only hosts, pass `--cgroup-version 2`; the default is v1. If no `--cgroup` flags
  are given under v2, the jailer does not create a cgroup
  ([jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md)). The
  first host is cgroup v2 only (observed).
- Jailer inputs are treated as trusted and must be root-owned/non-writable by unprivileged users;
  the docs advise a dedicated non-privileged uid/gid and chroot-relative resource paths
  ([prod-host-setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)).
- Under pluto's no-trust model this is hygiene, not security. The cost (a privileged launcher and a
  state-dir layout change) is why Hotcell defers jailer to its own milestone
  ([Hotcell plan, B.6](https://github.com/sinameraji/hotcell/blob/main/docs/plan.md)).

### 1.5 Boot latency: claims vs measurements

Claims:

- Firecracker's enforced spec: **≤125 ms** from `InstanceStart` API call to the start of guest
  `/sbin/init`, measured with serial console disabled, minimal kernel and rootfs. VMM start to API
  socket ≤8 CPU ms (wall-clock typically ~12 ms, spread 6–60 ms), VMM memory overhead ≤5 MiB for
  1 vCPU / 128 MiB ([SPECIFICATION.md](https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md)).
- NSDI 2020, measured by the Firecracker team but peer-reviewed and comparative: 500 serial boots
  of a minimal 4.14.94 kernel, 1 vCPU / 256 MiB, pre-configured vs end-to-end. Pre-configured
  Firecracker and Cloud Hypervisor "boot twice as fast as QEMU"; Cloud Hypervisor was "marginally
  faster" than pre-configured Firecracker in serial. With 50 concurrent boots, pre-configured
  Firecracker had the tighter distribution, 99th percentile **146 ms vs 158 ms** for Cloud
  Hypervisor. The paper also reports: kernel choice dominates (the Ubuntu 18.04 kernel added
  ~900 ms), compression costs ~40 ms, a statically configured NIC adds ~20 ms, and qboot saved
  ~20 ms for QEMU ([NSDI 2020 §5.1](https://www.usenix.org/system/files/nsdi20-paper-agache.pdf)).

Independent / on-host:

- An independent 2020 study paper mostly restates the 125 ms vendor claim and the architecture; it
  contains no original timing experiment ([arXiv 2005.12821](https://arxiv.org/html/2005.12821v1)).
- A well-known independent walkthrough found 2–3 s for "relatively large" Ubuntu/systemd VMs, i.e.
  distro rootfs effects dominate; "less than a second" for a minimal image
  ([jvns.ca](https://jvns.ca/blog/2021/01/23/firecracker--start-a-vm-in-less-than-a-second/)).
- **On this host** (AMD 12-core, kernel 7.2.8, FC v1.17.0, CI kernel 6.18.51, 1 vCPU/128 MiB,
  serial console *on*): guest boot timer reported **428 ms and 441 ms** across two runs; VMM start
  to `InstanceStart` was ~18 ms (log timestamps). The boot timer fires when the initramfs writes
  its marker, slightly after `/sbin/init` starts. This is consistent with the 125 ms class claim
  once the enabled serial console, the larger CI kernel and the host variance are accounted for.
  Raw output in Section 6.6.

Verdict: the 125 ms figure is real for the documented minimal configuration, but it is a
best-case, console-off, kernel-tuned number. A practical expectation for a distro rootfs is
several hundred milliseconds to a few seconds. Pluto does not need the last 300 ms.

### 1.6 Limits

- No UEFI firmware, no BIOS, no bootloader, cannot boot arbitrary kernels (Windows needs
  significant work) ([NSDI 2020](https://www.usenix.org/system/files/nsdi20-paper-agache.pdf)).
- No virtio-fs (block only; the NSDI paper says this was a security choice: file systems are large
  code bases) ([NSDI 2020 §3.1](https://www.usenix.org/system/files/nsdi20-paper-agache.pdf)).
- No general device passthrough/GPU; VFIO is not a Firecracker feature.
- Network backend is TAP only, single queue; no vhost-net kernel data path; no userspace slirp
  ([network-setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md),
  [arXiv study](https://arxiv.org/html/2005.12821v1)).
- Maximum 32 vCPUs in the v1.17.0 API spec; one VMM process per VM (no in-process pooling).
- Hotplug is developer preview and PCI-transport-only; no guest notification.
- Guest kernels are version-constrained: currently validated host/guest kernels are 5.10, 6.1 and
  6.18 (6.18 added in v1.16), with a minimum 2-year support promise; other versions "might work"
  but are not validated. Snapshot compatibility is even narrower
  ([kernel policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)).
- Snapshots require dirty-page tracking and are not cross-version compatible; vsock snapshot
  support is limited ([snapshot docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)).
  Under pluto's disk-only pause this machinery is unnecessary.
- Release support window: patch support for the last two minor releases (up to 1 year), any minor
  for at least 6 months ([RELEASE_POLICY](https://github.com/firecracker-microvm/firecracker/blob/main/docs/RELEASE_POLICY.md)).

### 1.7 Storage semantics that matter for durable boxes

Firecracker block devices are file-backed virtio-blk. The important detail for disk-only durability:

- `cache_type` defaults to **`Unsafe`**: the device does not advertise `VIRTIO_BLK_F_FLUSH` and
  **guest flushes are ignored**. `cache_type: Writeback` advertises flush and translates each
  guest flush to `fsync` on the backing file
  ([block](https://github.com/firecracker-microvm/firecracker/blob/main/docs/block.md)).
- The default IO engine is synchronous blocking `read`/`write`/`fsync`; an `io_uring` Async engine
  is in developer preview ([block](https://github.com/firecracker-microvm/firecracker/blob/main/docs/block.md)).
- Drives are configured pre-boot; block device re-scan lets a running guest pick up backing-file
  size changes ([Firecracker README](https://github.com/firecracker-microvm/firecracker/blob/main/README.md)).
- `discard` is opt-in and Sync-engine-only; rate limiters are available per drive/iface
  ([block](https://github.com/firecracker-microvm/firecracker/blob/main/docs/block.md),
  [design](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md)).

Implication: pluto's box image should set `cache_type: Writeback`, and "pause" should quiesce the
guest (sync/flush) before killing the VMM. What "quiesce" means concretely (e.g. `fsfreeze`,
`sync`, or relying on ext4 journal replay after an abrupt kill) is an open question (Section 8).

### 1.8 Status and maintenance

- Latest release at time of writing: **v1.17.0**, 2026-09-10
  ([release](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.17.0)).
- Apache-2.0, Rust, maintained in the open; release train is steady. From the in-tree CHANGELOG:
  v1.14 added `virtio-pmem` and `virtio-mem` memory hotplug, v1.15 added the VMClock device and
  configurable serial output, v1.16 added Linux 6.18 host-kernel support; v1.17 is current
  ([CHANGELOG](https://github.com/firecracker-microvm/firecracker/blob/main/CHANGELOG.md),
  [release page](https://github.com/firecracker-microvm/firecracker/releases)).
- AWS-driven project; production at very large scale (15 trillion monthly Lambda invocations
  claimed on the project site: [firecracker-microvm.github.io](https://firecracker-microvm.github.io/)).

---

## 2. Cloud Hypervisor: what it adds

Cloud Hypervisor is a Rust VMM from the rust-vmm ecosystem, focused on "modern cloud workloads":
64-bit only, virtio/PCI-centric, no legacy devices, KVM or MSHV, x86-64 and AArch64 (riscv64
experimental). Latest release: **v53.0, 2026-07-12**; approximately 6-week major cadence, bug
fixes for two cycles; project governed under the Linux Foundation with backing from Alibaba, AMD,
Ampere, ARM, ByteDance, Intel, Microsoft, SAP and Tencent
([README](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md),
[releases](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/releases.md),
[cloudhypervisor.org](https://www.cloudhypervisor.org/)).

Differences that matter vs Firecracker:

- **Boot**: direct kernel boot (`--kernel`) requires a PVH-enabled ELF vmlinux or bzImage on
  x86-64; firmware boot (`--firmware`) with a lightweight Rust PVH firmware or an EDK2 UEFI build
  (`CLOUDHV.fd`) is supported
  ([README](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md),
  [UEFI doc](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/uefi.md)). The
  project's front page claims "boot to userspace in less than 100 ms with direct kernel boot"
  ([cloudhypervisor.org](https://www.cloudhypervisor.org/)). No first-class UKI boot path is
  documented; a UKI would ride inside the firmware/disk path, and x86-64 `--kernel` accepts only
  PVH ELF or bzImage (uncertainty noted: I found no primary source describing direct UKI boot).
- **Devices**: virtio-block/console/iommu/net/pmem/rng/vsock, vhost-user-blk/fs/net, VFIO
  passthrough; virtio devices are **PCI transport only**
  ([device model](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/device_model.md)).
- **virtio-fs**: supported via an external `virtiofsd`, with `--memory shared=on`; DAX is not
  available because the daemon side is not stable
  ([fs doc](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/fs.md)). Also a
  documented virtiofs-root path ([virtiofs-root](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/virtiofs-root.md)).
- **Hotplug**: CPU (x86), memory resize, PCI devices, and virtio-{net,block,pmem,fs,vsock} at
  runtime ([hotplug](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/hotplug.md),
  [README](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md)).
- **Snapshot/restore and live migration**: mature and actively developed (v53 added an offloaded
  snapshot/restore daemon, userfaultfd postcopy/prefault, mTLS migration); not compatible across
  versions ([snapshot-restore](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/snapshot_restore.md),
  [v53 release notes](https://www.cloudhypervisor.org/)).
- **Networking**: TAP is created automatically by Cloud Hypervisor, or supplied as a pre-opened fd
  (`--net fd=3`), or macvtap, or vhost-user-net. The docs' quick start uses
  `sudo setcap cap_net_admin+ep ./cloud-hypervisor` so the binary can create TAPs as a normal user;
  with an fd handed in, no capability is needed
  ([README](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md),
  [macvtap](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/macvtap-bridge.md),
  [device model](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/device_model.md)).
- **API**: CLI flags, an OpenAPI-compliant REST API over a Unix socket (or fd), and an optional
  D-Bus API; the CLI is a client of the REST API
  ([API doc](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/api.md)).
- **Sandboxing**: seccomp filters and optional Landlock (unprivileged, path-scoped filesystem
  confinement) ([landlock](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/landlock.md),
  [seccomp](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/seccomp.md)).
- **Windows guests** supported; VFIO/vDPA/TDX/vfio-user are the passthrough/confidential story
  ([README](https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md)).

For pluto M0, none of these capabilities are required; they are the escape hatch when a box needs
UEFI, a shared host directory, device passthrough or Windows. The price is a larger device model,
PCI-only virtio, an external virtiofsd for file sharing, and a version-locked snapshot feature
pluto does not use.

---

## 3. QEMU microvm and libkrun fallbacks

### 3.1 QEMU `microvm` machine type

Primary source: [QEMU microvm docs](https://www.qemu.org/docs/master/system/i386/microvm.html).

- A minimalist machine type "inspired by Firecracker", no PCI, no ACPI, up to **8 virtio-mmio**
  devices, ISA bus, optional PIC/PIT/RTC/serial.
- Explicit limitations: no PCI-only devices, **no hotplug of any kind**, no live migration across
  QEMU versions.
- Firmware boot is limited: by default it uses `qboot` for fast boot (SeaBIOS also possible), but
  no current firmware can boot a block device over virtio-mmio, so a microvm VM "needs to be run
  using a host-side kernel and, optionally, an initrd".
- Guest-initiated shutdown is not available via keyboard/ACPI; the recommended trick is a
  triple-fault reboot plus `-no-reboot`.

Useful as a debugging/reference VMM (full QEMU tooling: `slirp` user-mode networking, `-d`
tracing, GDB stub), not as pluto's production runner.

### 3.2 libkrun

Primary source: [libkrun README](https://github.com/libkrun/libkrun).

- A **dynamic library** that turns a process into a KVM (Linux) / Hypervisor.framework (macOS)
  microVM — no external VMM process, no daemon. Ships a kernel via `libkrunfw`.
- Virtio devices: console, block, fs, gpu (venus/native), net, vsock, balloon (free-page reporting
  only), rng.
- Networking, two mutually exclusive modes: **virtio-vsock + TSI** (Transparent Socket
  Impersonation — no NIC at all; the VMM proxies AF_INET/AF_INET6/AF_UNIX sockets) or
  **virtio-net + passt/gvproxy**. TSI requires the custom kernel from libkrunfw and is limited to
  stream/datagram sockets in those address families.
- Security model is explicit: "both the guest and the VMM pertain to the same security context";
  the VMM acts as the guest's proxy on the host, so host isolation must come from running the VMM
  inside OS isolation (namespaces), or not at all on single-user systems. virtio-fs is explicitly
  noted as providing no protection against reaching other host paths.
- Used by crun (container isolation), krunkit/Podman machine on macOS, muvm (Asahi), and
  microsandbox (Section 4.1).

libkrun is the strongest option if pluto ever moves to macOS hosts, and its TSI design is the
cleanest expression of "no NIC, no TAP, no root". On a Linux fleet, Firecracker's process-per-VM
model is easier to supervise and has more public fleet precedent.

---

## 4. What microsandbox and Hotcell choose and why

### 4.1 microsandbox: libkrun (a fork)

- The repo is now `superradcompany/microsandbox`; its `Cargo.toml` depends on `msb_krun` /
  `msb_krun_utils` v0.1.40 (a libkrun fork) with `net` and `blk` features
  ([Cargo.toml](https://github.com/superradcompany/microsandbox/blob/main/Cargo.toml),
  [`crates/runtime`](https://github.com/superradcompany/microsandbox/blob/main/crates/runtime/Cargo.toml)).
- Their own isolation doc: each sandbox is a microVM with "its own Linux kernel, supplied by
  microsandbox (built from libkrunfw)"; scheduled by KVM / Apple Hypervisor.framework "through the
  libkrun VMM"; the per-sandbox process runs as the launching user, "no setuid, no elevated
  capabilities", needs only `/dev/kvm` access on Linux. The only host-facing devices are
  virtio-console (control channel to `agentd`, PID 1), virtio-net, virtio-fs, virtio-blk and
  virtio-rng ([isolation](https://docs.microsandbox.dev/security/isolation)).
- Requirements: macOS Apple Silicon, Linux with KVM, Windows with WHP
  ([README](https://github.com/superradcompany/microsandbox/blob/main/README.md)). Claimed average
  boot under 100 ms; OCI-image compatible; embedded SDK (a microVM is a child process, "no
  long-running daemon") ([README](https://github.com/superradcompany/microsandbox/blob/main/README.md)).
- Snapshots: disk (default; boots a new VM) and full (disk + memory + running processes; resumes
  execution); forking creates independent copies
  ([snapshots](https://docs.microsandbox.dev/sandboxes/snapshots)).
- Networking: "All sandbox traffic flows through a host-side network stack... a user-space stack
  terminates every packet... There is no host kernel routing or NAT in the path"
  ([network](https://docs.microsandbox.dev/security/network)).
- Their own comparison with Firecracker frames the choice as engine vs car: libkrun is
  "embeddable, cross-platform"; Firecracker is the bare VMM primitive and Linux-only
  ([compare page](https://microsandbox.dev/compare/firecracker)).

Why they chose it: embeddability (library, sandbox-as-child-process), macOS+Linux+Windows from one
core, same-user operation, and OCI workflows. For pluto, the interesting steal is *not* libkrun
itself but the agent-over-virtio-console pattern and the "no host kernel networking in the path"
stance.

### 4.2 Hotcell: Firecracker on Linux, Apple VZ on macOS

Hotcell ("sandboxes for AI agents, on your own hardware") is a single Node daemon (`hotcelld`)
with a pluggable driver layer. Drivers shipped: `container` (Docker), `firecracker` (Linux + KVM),
`applevz` (macOS). The driver choice is per-sandbox or daemon-wide
([self-hosting doc](https://github.com/sinameraji/hotcell/blob/main/docs/self-hosting.md)).

Why Firecracker (from their plan and status):

- Hardware/VM-grade isolation as an upgrade tier over the shared-kernel container driver, behind
  an unchanged interface ([plan](https://github.com/sinameraji/hotcell/blob/main/docs/plan.md)).
- The primitives are all proven and documented; the plan's own verdict: "integration + DX +
  observability, not novel systems research" ([plan](https://github.com/sinameraji/hotcell/blob/main/docs/plan.md)).
- Operations actually shipped: `firecracker` + optional `jailer`, HTTP API over a Unix socket,
  OCI→ext4 via native `mke2fs`, **virtio-vsock agent** (`hotcell-agent`, Go, PID 1) shared with
  the Apple VZ driver, workspace as a second raw disk image, Firecracker native snapshots for a
  warm pool ([plan Appendix B](https://github.com/sinameraji/hotcell/blob/main/docs/plan.md)).
- Notable design decision: **guests have no network device at all**. Egress goes over vsock
  (guest agent opens a loopback listener; each connection is forwarded over a fresh guest→host
  vsock connection to the host's gateway). "Default-deny holds by construction with zero firewall
  configuration." Jailer is a tracked follow-up, because it needs a root-owned launcher and
  chroot-relative paths ([plan Appendix B](https://github.com/sinameraji/hotcell/blob/main/docs/plan.md)).
- Their own measurements (GCE n2-standard-4 **nested virt**, FC v1.10.1, CI kernel 5.10.223,
  Alpine 3.20 rootfs): 40/40 microVMs booted, cold boot p50 **2.49 s** / p95 **3.67 s** at
  concurrency 4, ~**54 MB host memory per live VM**, teardown of 40 in 1.0 s, snapshot resume
  ~**80 ms** vs ~2.5 s cold boot, warm-pool adopt ~**7 ms**
  ([density note](https://github.com/sinameraji/hotcell/blob/main/docs/plan.md),
  [benchmarks](https://github.com/sinameraji/hotcell/blob/main/docs/benchmarks.md)). Nested
  virtualization inflates these numbers; bare-metal numbers are not published yet in the repo.

The steal for pluto: Firecracker's API + vsock agent + ext4 workspace image is enough for a
whole fleet; the container/apple-vz drivers are useful evidence that a driver seam stays thin, and
the no-NIC vsock-only posture is a direct match for "no trust model, self-host, expose surfaces".

---

## 5. How real fleets run these

### 5.1 AWS Lambda (classic functions)

Primary source: the NSDI 2020 paper by the Firecracker team
([paper](https://www.usenix.org/system/files/nsdi20-paper-agache.pdf)).

- Lambda workers: e.g. 48 cores (SMT disabled as a side-channel mitigation), 384 GB RAM, hundreds
  to thousands of MicroVMs per worker; slots are pre-loaded execution environments.
- A per-worker **MicroManager** process owns Firecracker processes, slot locking/lifecycle, and a
  small pool of **pre-booted microVMs** because even 125 ms boot is too slow for the scale-up path.
- A separate **Placement service** leases slots with a time-based lease; sticky routing keeps a
  function on few workers for cache locality. Placement typically takes <20 ms.
- Jailer is used (`chroot`, netns/pid ns, privilege drop, seccomp); Firecracker itself provides
  the KVM boundary, seccomp filters, and rate limiters.
- Resources are soft-allocated and oversubscribed as a "statistical bet" (the paper's words), with
  the platform keeping resources busy while idle slots' resources can be sold to others. The paper
  lists memory deduplication as future work; it does not describe ballooning or snapshots at that
  time (both exist as Firecracker features today).
- Device/storage shape: virtio-net and virtio-blk only; no file-system passthrough by choice.

### 5.2 AWS Lambda MicroVMs (the 2026 managed product)

- Announced **2026-06-22**: a separate Lambda resource giving one Firecracker microVM per
  user/session/job, launched from **pre-initialized snapshots** for near-instant start, stateful up
  to **8 hours**, configurable idle suspend, dedicated HTTPS endpoints, Firecracker-powered
  ([AWS News Blog](https://aws.amazon.com/blogs/aws/run-isolated-sandboxes-with-full-lifecycle-control-aws-lambda-introduces-microvms/),
  [product page](https://aws.amazon.com/lambda/lambda-microvms)).
- You still build the environment image (Dockerfile → MicroVM image with lifecycle hooks),
  and you still own lifecycle orchestration; AWS sells the isolation and snapshot launch
  ([AWS Compute Blog](https://aws.amazon.com/blogs/compute/running-self-hosted-ai-agent-sandboxes-with-aws-lambda-microvms)).
- Relevance to pluto: it validates the exact shape pluto wants — per-box VM, snapshot-fast start,
  suspend on idle, state retention — but as a managed service. Pluto self-hosts the same shape
  with disk-only pause instead of memory snapshots.

### 5.3 Fly.io

Primary sources: [Fly architecture docs](https://fly.io/docs/reference/architecture/),
[Fly Machines blog](https://fly.io/blog/fly-machines/), [Fly stack page](https://fly.io/docs/hiring/stack/),
[Fly community posts](https://community.fly.io/t/exploring-faster-machine-creates/21770).

- Application code runs in Firecracker microVMs; they aim to dedicate a core to one microVM and
  avoid steal; hosts have 8–32 physical cores and 32–256 GB RAM.
- `flyd` (Go) orchestrates VMs and uses containerd to convert Docker images into root filesystems;
  `vold` provisions persistent encrypted storage; `fly-proxy` (Rust) fronts traffic with WireGuard
  backhaul; `corrosion` (Rust, SWIM) replicates routing/state; `init` inside the VM is their Rust
  code and owns signal handling, mounts and reaping.
- Machines are **pinned to specific hardware**; the image is prepared locally ahead of time, so a
  `start` is one message to the host and is measured as fast as the network (~10 ms same-region,
  ~150 ms cross-region), around **300 ms** for a create+start from an API elsewhere. The proxy can
  boot a stopped machine on request; stopped machines cost only image storage.
- Faster-create work: containerd snapshot → rootfs was a bottleneck; they added an override to
  boot a previously prepared image, and experimented with overlaybd (lazy block fetching via a
  userspace daemon over TCMU) as the rootfs ([community post](https://community.fly.io/t/experimental-speedy-machine-creation-with-overlaybd/18958)).

### 5.4 E2B

Primary source: the open-sourced E2B runtime (`e2b-dev/runtime`, Apache-2.0, Go;
[README](https://github.com/e2b-dev/runtime/blob/main/README.md),
[ARCHITECTURE.md](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)).

- Sandboxes are Firecracker microVMs. "A sandbox is a resumed snapshot": templates are pre-booted
  VM snapshots (memory + disk + machine state) in object storage; creating a sandbox loads and
  resumes one rather than booting a kernel.
- Per-node orchestrator (runs as root) owns the Firecracker process, cgroup, network namespace,
  block device. One FC process per sandbox, configured over the FC HTTP API on a Unix socket;
  guest metadata via MMDS.
- Lazy memory via **userfaultfd** from the template memfile; rootfs is a **copy-on-write overlay
  served as an NBD block device**; networking is a per-sandbox **network namespace with veth + tap,
  a unique host-side IP from a /16, NAT, and per-slot nftables egress rules**.
- The in-VM agent `envd` (Go) runs early in boot, exposes processes/PTYs/files/watchers/port
  forwarding over Connect RPC and REST on port 49983; orchestration traffic reaches it over the
  slot network (veth/tap into the VM), not vsock.
- Pause diffs memory and disk against the template and uploads to object storage; resume prefers a
  node with a cache; fork checkpoints in place and can start up to 100 new sandboxes from the
  snapshot while the original runs.
- Control plane: Postgres (durable: teams/templates/snapshots), Redis (running sandbox + routing),
  ClickHouse (events/metrics), object storage (memfile/rootfs/snapfile/metadata per build).
  Single-node "Embed" runs the whole stack via Docker Compose / Terraform / Kubernetes.

The steal for pluto: envd is the template for a guest agent API; the network slot (netns + veth +
tap + nftables) is the full-featured network shape; MMDS for guest identity; template + snapshot
storage layout. Pluto deliberately drops the memory-snapshot machinery and the multi-node control
plane.

### 5.5 Arrakis (research ancestor, not a fleet runtime)

The ticket's list includes Arrakis; in primary literature this is the OSDI 2014 best paper
[Arrakis: The Operating System is the Control Plane](https://www.usenix.org/conference/osdi14/technical-sessions/presentation/peter)
(Peter et al.). It splits the kernel into a control plane and a data plane: applications get
direct access to virtualized I/O devices, while the kernel enforces protection without mediating
every operation; reported 2–5× latency and 9× throughput improvements for a NoSQL store.

It is not a deployable VMM or fleet. Its relevance to pluto is conceptual vocabulary: separate
the control plane (placement/lifecycle/leases) from the data plane (the box and its I/O), and make
the fast path bypass the controller. That is the same shape Lambda, E2B and Fly use
(control plane decides where; node-local agent owns how). Uncertainty: if "Arrakis" in the ticket
referred to some newer product, it was not found in primary sources.

### 5.6 Cross-fleet summary

| System | VMM | Boot mechanism | Control channel | Networking | Pause model |
|---|---|---|---|---|---|
| Lambda functions | Firecracker | kernel boot + pre-booted pool | FC API + MicroManager | TAP/NAT at AWS scale | slot lifecycle |
| Lambda MicroVMs | Firecracker | memory snapshot restore | managed API | managed, per-session endpoint | suspend/resume, 8 h cap |
| Fly Machines | Firecracker | image on host, start message | flyd + FC API | fly-proxy/WireGuard | stop = process exit; start fast |
| E2B | Firecracker | UFFD memory snapshot resume | orchestrator gRPC + envd HTTP | netns + veth/tap + NAT + nftables | memory+disk diff snapshot |
| Hotcell | Firecracker (Linux) | cold boot + warm pool + FC snapshots | FC API + vsock agent | **none in guest**; egress over vsock | FC memory snapshot (~80 ms) |
| microsandbox | libkrun fork | in-process library boot | virtio-console agent | host userspace stack | disk or full snapshot |
| pluto (intended) | Firecracker | cold boot from disk image | FC API + vsock agent | TBD: vsock-only or rootless TAP | **disk-only: kill VMM** |

---

## 6. Booting a minimal Linux box on the first host

### 6.1 Host inventory (observed, read-only checks)

- OS: Neptuno 45.20261002 (Fedora-family image-based OS), kernel `7.2.8-300.fc45.x86_64`.
- CPU: 12 cores, AMD (`svm`), flags include `smap`, `smep`; 15 GiB RAM; 261 GB free on `/var`.
- `kvm` + `kvm_amd` modules loaded; `/dev/kvm` is `crw-rw-rw- root kvm` → read/write by the
  regular user; no sudo needed for KVM.
- cgroup v2 only (`cgroup2fs`); SELinux Enforcing.
- `unshare -Urn` works; a TAP can be created inside the namespace (`ip tuntap add`) with no
  capabilities on the host. `pasta`, `passt` and `slirp4netns` are all installed.
- Installed: `tailscale`, `bst`, `nft`, `iptables`, `ip`, `jq`, `curl`, `mkfs.ext4`,
  `unsquashfs` (linuxbrew), `zstd`. Not installed: `firecracker`, `cloud-hypervisor`,
  `qemu-system-x86_64`, `virtiofsd`, `socat`.
- Both `nft` and `iptables` (nft backend) exist for the documented NAT path if ever needed.

### 6.2 Kernel

- Use `vmlinux` (uncompressed) or `bzImage`; prefer `vmlinux`. Sources: Firecracker CI artifacts
  (`https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/<build>/x86_64/vmlinux-*`), the
  Amazon Linux `microvm-kernel-*` tags built from the configs in
  `resources/guest_configs/`, or a self-built kernel from those configs
  ([kernel policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md),
  [rootfs/kernel setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md)).
- Validated guest kernels as of the current docs: 6.1 and 6.18 (5.10 is at/past its end date).
  The host kernel (7.2.8) is newer than any validated version; the smoke test worked anyway, but
  this is outside the support matrix.
- Minimum guest config for block-root boot on x86_64: `CONFIG_VIRTIO_BLK`, `CONFIG_ACPI`,
  `CONFIG_PCI`, `CONFIG_KVM_GUEST`; add serial console config for logs
  ([kernel policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)).
- Size observed: CI `vmlinux-6.18.51` is 27.9 MB; CI `initramfs.cpio` is 2.1 MB.

### 6.3 Rootfs

- A file-backed image with an init system; ext4 is the documented default. Build with
  `truncate` + `mkfs.ext4 -d <tree>` from a squashfs/OCI unpack, or use the CI recipes
  ([rootfs/kernel setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md)).
- Alternative: boot from an initramfs entirely (verified). For pluto's persistent boxes the
  two-image layout is the right one: a **read-only base rootfs** plus a **writable workspace
  disk**, which is exactly Hotcell's `vda-ro` + `vdb-rw` layout and a simplification of E2B's
  COW-over-NBD ([Hotcell plan B.4](https://github.com/sinameraji/hotcell/blob/main/docs/plan.md),
  [E2B architecture](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)).

### 6.4 Guest agent

- Firecracker's vsock gives a root-free host↔guest channel: configure `PUT /vsock`
  (`guest_cid`, `uds_path`), then the host connects to the Unix socket and speaks
  `CONNECT <port>\n`; guest-initiated connections appear at `<uds_path>_<port>`
  ([vsock](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md)).
- Guest needs `CONFIG_VIRTIO_VSOCKETS=y`; host kernel `CONFIG_VHOST_VSOCK=m`
  ([vsock](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md)).
- Precedents: E2B `envd`, Hotcell's Go PID-1 agent (exec/files/processes/ports/stats over vsock),
  microsandbox `agentd` over virtio-console. All three converge on the same design: a tiny
  in-guest agent as PID 1 or early systemd unit, host-driven RPC, no inbound host access to
  arbitrary guest ports.
- If a NIC exists, MMDS can pass box identity/metadata to the guest; with vsock-only, identity
  belongs on the kernel cmdline or the agent handshake (MMDS requires `virtio-net`, per the
  [device matrix](https://github.com/firecracker-microvm/firecracker/blob/main/docs/device-api.md)).

### 6.5 Networking concrete options for pluto

1. **No NIC, vsock-only** (Hotcell's shipped design): zero host network config, default-deny by
   construction; expose SSH/HTTP/MCP through host-side proxies that speak to the guest agent over
   vsock. Strongest match for pluto's no-trust/self-host posture.
2. **Rootless TAP in a user+network namespace** (verified): create the TAP inside `unshare -Urn`,
   run Firecracker there; use `pasta`/`slirp4netns` if internet egress is needed, or bridge only
   to host-side proxies. No sudo, no system-wide changes. Caveat from a quick check: launching
   `pasta` *inside* the fresh namespace sees no external interface (only `lo` exists there), which
   is expected — the documented rootless pattern (podman-style) is for `pasta` to run on the host
   side and join the VM's namespace; that composition still needs a working prototype (Section 8).
3. **Host TAP + nft NAT** (Firecracker's documented path): simplest to reason about, needs root
   (or a one-time privileged setup) for TAP/forwarding/rules; gives guest IPs, DHCP-less static
   config, LAN visibility if bridged. E2B's production shape.

The first host's kernel/namespace support makes option 2 work today; option 1 needs no privilege
at all and is the cleanest M0 target.

### 6.6 Verified smoke boot on the first host

What was run (all under `/tmp/opencode/fc-smoke`, no sudo, no system changes; foreground processes
only, killed after each run):

1. Downloaded Firecracker v1.17.0 (7.5 MB) and CI artifacts `vmlinux-6.18.51` (27.9 MB),
   `initramfs.cpio` (2.1 MB) from the `firecracker-ci/20260930-...` build.
2. Booted with `--no-api --config-file cfg.json --boot-timer`, config: CI kernel + initrd,
   `console=ttyS0 ...`, 1 vCPU / 128 MiB.
3. Rebuilt the initramfs with an init that configures `eth0` and pings the host, then booted
   **inside `unshare -Urn`** with a TAP created in that namespace and attached as
   `host_dev_name: t0`.

Observed results:

```text
[anonymous-instance:main] Running Firecracker v1.17.0
[anonymous-instance:main] Successfully started microvm ...          # ~18 ms after start
[fc_vcpu 0] Guest-boot-time = 428469 us 428 ms, 423778 CPU us      # run 1
[fc_vcpu 0] Guest-boot-time = 441692 us 441 ms, 438693 CPU us      # run 2
>>> Welcome to fcinitrd <<<

# rootless network run, inside `unshare -Urn`:
[Net:eth0] notifying queues
[    0.762078] device=eth0, hwaddr=06:00:ac:10:00:02, ipaddr=172.16.0.2, gw=172.16.0.1
=== NET TEST ===
NET_OK
eth0: 406 5 0 ...   # guest /proc/net/dev counters
[    1.777971] reboot: Power off not available: System halted instead
```

Also measured: the FC process with a 128 MiB guest used ~58 MB RSS by the host's accounting
(guest RAM + VMM), consistent with Hotcell's ~54 MB/VM observation; `/sbin/init` boot with
`console=ttyS0` enabled lands in the ~430 ms class on this machine.

Gotchas discovered in the process:

- The JSON config file requires a `drives` field even for an initrd-only VM (`missing field
  drives` error otherwise). Use `"drives": []`.
- Firecracker appends its default cmdline; setting `console=ttyS0` duplicates `pci=off` etc. but is
  harmless.
- Block devices default to `cache_type: Unsafe` (flush ignored). Set `Writeback` for durability.
- The CI initramfs drops to a BusyBox shell; for a real box you want your own init/agent and an
  ext4 rootfs.

---

## 7. Implications for pluto

1. **VMM choice**: Firecracker as the primary runner. It is the only option with a public fleet
   playbook at three independent operators (AWS, Fly, E2B) plus a directly comparable
   self-hosted agent sandbox (Hotcell) that has already made the exact design decisions pluto
   faces (vsock agent, OCI→ext4, no NIC, warm pool later).
2. **Keep a thin seam, not an abstraction framework**: model a box as a VMM process + Unix-socket
   API + disk images + vsock channel. Both Firecracker and Cloud Hypervisor present nearly the
   same shape (HTTP API over UDS, virtio-blk, vsock copied from FC); QEMU microvm can be a debug
   escape hatch. The seam needs to be thin because Hotcell's experience is that the driver layer
   is only ~one file once the guest agent exists.
3. **Disk-only pause**: pause = quiesce guest, kill VMM, keep the ext4 workspace image; resume =
   boot from the same images. No snapshots, no UFFD, no memory files. Set
   `cache_type: Writeback`; treat ext4 journal replay as the fallback. Verify quiescing semantics
   (open question).
4. **No sudo required for M0**: `/dev/kvm` is world-rw here, and a rootless user+network namespace
   with a TAP was verified. The jailer is deferred; pluto has no trust model, so the jailer's
   security value is low relative to its privileged-launcher cost. If it is ever wanted, budget
   for a privileged helper and `--cgroup-version 2`.
5. **Networking**: start with vsock-only (no NIC), expose SSH/HTTP/MCP through host-side proxies to
   the guest agent; add a TAP only when a workload needs real sockets. This keeps the first host
   clean and matches the no-trust posture.
6. **Kernels and images**: use `vmlinux` from the Firecracker CI or Amazon Linux microvm kernels
   for M0; build OCI→ext4 with `mkfs.ext4 -d`. Pin artifact versions and record them with each
   box (kernel 6.18.51 is the current validated line; the host's 7.2.8 kernel is newer than
   validated, empirically fine).
7. **Boot-time budget**: expect a few hundred ms to low seconds, not 125 ms; fine for M0 and for
   `git`-based workflows. Pre-warming/snapshotting is a later optimization and, per pluto's
   disk-only decision, would be disk-image caching rather than memory snapshots.

---

## 8. Open questions surfaced

1. **Pause quiescence**: what is the correct sequence to flush an ext4 workspace before killing
   the VMM (guest `fsync`/`fsfreeze` via agent vs. `cache_type: Writeback` + journal replay)?
   Needs an experiment with a crash-consistency test.
2. **Guest surfaces without a NIC**: how exactly to expose SSH/HTTP/MCP over vsock (socat-style
   proxy? agent-mediated port forwarding? E2B envd-style HTTP over a proxy?) — a design ticket,
   not a VMM question. Related: produce a working rootless internet-egress composition
   (`pasta`/`slirp4netns` joining the VM namespace podman-style) if a box ever needs real outbound
   sockets.
3. **Network identity per box**: if TAPs are used, MAC/IP allocation across a /16 (E2B pattern) or
   per-box netns (FC clone guidance); if vsock-only, boxes may need no identity at all beyond CID.
4. **Image pipeline**: Firecracker CI artifacts vs Amazon Linux microvm kernels vs own kernel
   config; OCI→ext4 tooling; per-project cache disks (Hotcell's `vdb`, E2B's volumes).
5. **Warm pool/pause semantics**: with disk-only pause, is resume just cold boot? If M0 boot is
   ~0.4–1 s, a pool may be unnecessary; measure before designing.
6. **Jailer + cgroup v2 on this host**: if adopted later, `--cgroup-version 2`, a dedicated uid,
   and root-owned chroot paths are required; confirm the image-based OS's writable locations.
7. **Cloud Hypervisor UKI**: no primary doc found for direct UKI boot; only firmware+disk. If UKI
   is a requirement, prototype on CH before committing.
8. **Kernel support drift**: 6.1's minimum support window has passed and 6.18 is the current line;
   confirm which kernel the box image pipeline should standardize on and how host kernel 7.x
   interacts with future FC validation.

---

## Appendix A — Sources

Firecracker (repo docs fetched from `main`, plus v1.17.0 release artifacts):

- Design: https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md
- Device API matrix: https://github.com/firecracker-microvm/firecracker/blob/main/docs/device-api.md
- Getting started: https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md
- Rootfs & kernel setup: https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md
- Network setup: https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md
- Vsock: https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md
- Jailer: https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md
- Prod host setup: https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md
- Kernel policy: https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md
- Block devices/caching: https://github.com/firecracker-microvm/firecracker/blob/main/docs/block.md
- Ballooning: https://github.com/firecracker-microvm/firecracker/blob/main/docs/ballooning.md
- Memory hotplug: https://github.com/firecracker-microvm/firecracker/blob/main/docs/memory-hotplug.md
- Device hotplug (preview): https://github.com/firecracker-microvm/firecracker/blob/main/docs/device-hotplug.md
- Release policy: https://github.com/firecracker-microvm/firecracker/blob/main/docs/RELEASE_POLICY.md
- Specification: https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md
- Swagger (v1.17.0 artifact): https://github.com/firecracker-microvm/firecracker/releases/download/v1.17.0/firecracker-v1.17.0-x86_64.tgz (file `firecracker_spec-v1.17.0.yaml`)
- Release v1.17.0: https://github.com/firecracker-microvm/firecracker/releases/tag/v1.17.0
- CI artifacts used in smoke test: https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/x86_64/

Papers and independent sources:

- Agache et al., "Firecracker: Lightweight Virtualization for Serverless Applications", NSDI 2020: https://www.usenix.org/system/files/nsdi20-paper-agache.pdf
- "Study of Firecracker MicroVM" (2020, descriptive): https://arxiv.org/html/2005.12821v1
- jvns.ca, "Firecracker: start a VM in less than a second" (2021): https://jvns.ca/blog/2021/01/23/firecracker--start-a-vm-in-less-than-a-second/
- Peter et al., "Arrakis: The Operating System is the Control Plane", OSDI 2014: https://www.usenix.org/conference/osdi14/technical-sessions/presentation/peter

Cloud Hypervisor:

- README: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/README.md
- Website + v53.0 notes: https://www.cloudhypervisor.org/
- Device model: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/device_model.md
- UEFI: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/uefi.md
- virtio-fs: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/fs.md
- Hotplug: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/hotplug.md
- Snapshot/restore: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/snapshot_restore.md
- API: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/api.md
- Releases: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/releases.md
- Landlock: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/landlock.md
- MACVTAP: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/macvtap-bridge.md
- virtiofs-root: https://github.com/cloud-hypervisor/cloud-hypervisor/blob/main/docs/virtiofs-root.md

Other VMMs and tools:

- QEMU microvm machine type: https://www.qemu.org/docs/master/system/i386/microvm.html
- libkrun: https://github.com/libkrun/libkrun
- passt/pasta: https://passt.top/passt/about/
- slirp4netns: https://github.com/rootless-containers/slirp4netns

Self-hosted agent sandboxes:

- Microsandbox README: https://github.com/superradcompany/microsandbox/blob/main/README.md
- Microsandbox isolation: https://docs.microsandbox.dev/security/isolation
- Microsandbox network: https://docs.microsandbox.dev/security/network
- Microsandbox snapshots: https://docs.microsandbox.dev/sandboxes/snapshots
- Microsandbox vs Firecracker: https://microsandbox.dev/compare/firecracker
- Hotcell README: https://github.com/sinameraji/hotcell/blob/main/README.md
- Hotcell plan (Firecracker driver, measurements): https://github.com/sinameraji/hotcell/blob/main/docs/plan.md
- Hotcell self-hosting: https://github.com/sinameraji/hotcell/blob/main/docs/self-hosting.md
- Hotcell benchmarks: https://github.com/sinameraji/hotcell/blob/main/docs/benchmarks.md
- E2B runtime README: https://github.com/e2b-dev/runtime/blob/main/README.md
- E2B architecture: https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md
- E2B Embed: https://github.com/e2b-dev/runtime/blob/main/embed/README.md

Fleets:

- Fly architecture: https://fly.io/docs/reference/architecture/
- Fly Machines: https://fly.io/blog/fly-machines/
- Fly stack: https://fly.io/docs/hiring/stack/
- Fly faster creates: https://community.fly.io/t/exploring-faster-machine-creates/21770
- Fly overlaybd experiment: https://community.fly.io/t/experimental-speedy-machine-creation-with-overlaybd/18958
- AWS Lambda MicroVMs announcement: https://aws.amazon.com/blogs/aws/run-isolated-sandboxes-with-full-lifecycle-control-aws-lambda-introduces-microvms/
- AWS Lambda MicroVMs product page: https://aws.amazon.com/lambda/lambda-microvms
- AWS Compute Blog on self-hosted agent sandboxes: https://aws.amazon.com/blogs/compute/running-self-hosted-ai-agent-sandboxes-with-aws-lambda-microvms

## Appendix B — Smoke-test commands (first host)

```bash
# artifacts (v1.17.0 binary + CI kernel/initramfs)
curl -LO https://github.com/firecracker-microvm/firecracker/releases/download/v1.17.0/firecracker-v1.17.0-x86_64.tgz
S3=https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/x86_64
curl -LO $S3/vmlinux-6.18.51
curl -LO $S3/initramfs.cpio

# minimal config (note: drives required even for initrd boot)
cat > cfg.json <<'EOF'
{
  "boot-source": {
    "kernel_image_path": "/tmp/opencode/fc-smoke/vmlinux-6.18.51",
    "initrd_path": "/tmp/opencode/fc-smoke/initramfs.cpio",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off"
  },
  "machine-config": { "vcpu_count": 1, "mem_size_mib": 128, "smt": false },
  "drives": []
}
EOF

# boot (foreground, killed by timeout; --boot-timer prints guest boot time)
timeout 8 ./firecracker-v1.17.0-x86_64 --no-api --config-file cfg.json --boot-timer

# rootless TAP + guest network test (guest init runs ping)
unshare -Urn sh -c '
  ip link set lo up
  ip tuntap add dev t0 mode tap
  ip addr add 172.16.0.1/30 dev t0
  ip link set t0 up
  exec timeout 25 ./firecracker-v1.17.0-x86_64 --no-api --config-file cfg-net.json --boot-timer
'
# cfg-net.json adds: "network-interfaces": [{"iface_id":"eth0","host_dev_name":"t0","guest_mac":"06:00:AC:10:00:02"}]
# guest cmdline includes: ip=172.16.0.2::172.16.0.1:255.255.255.252::eth0:off
# observed: NET_OK (guest -> host ping), guest /proc/net/dev counters, clean halt
```
