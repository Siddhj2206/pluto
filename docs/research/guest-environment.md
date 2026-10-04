# Guest environment facts for #14

Fact note for the pluto guest-environment decision, 2026-10-04. Primary sources only;
one unprivileged empirical test (below). Artifact inspected:
`ubuntu-24.04.squashfs` from `s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/x86_64/`,
sha256 `008a05de13c4bd11b4bc8fb992b0c1df9f2e031d6bd04cee88b2e04df82b28f0`
(unpacked with `unsquashfs`; no repo writes, no sudo).

---

## 1. Contents of the Firecracker CI Ubuntu 24.04 rootfs

**How it is built (official recipe).** `resources/rootfs/setup-ubuntu-ci.sh`
([source](https://github.com/firecracker-microvm/firecracker/blob/main/resources/rootfs/setup-ubuntu-ci.sh))
starts from an Ubuntu OCI *minimized* base (the artifact's `/etc/cloud/build.info` says
`build_name: ubuntu-oci:minimized`, `serial: 20260911`) and runs
`apt install -y --no-install-recommends` with:
`udev systemd-sysv openssh-server iproute2 curl socat python3-minimal iperf3 iputils-ping fio
kmod tmux hwloc-nox vim-tiny trace-cmd linuxptp strace python3-boto3 pciutils` (+ `msr-tools`,
`cpuid` on x86_64). It then sets hostname `ubuntu-fc-uvm`, `passwd -d root`, adds the
ttyS0 autologin override, installs `fcnet.service` (`/usr/local/bin/fcnet-setup.sh`: assigns
`x.x.x.x/30` addresses derived from each NIC's `06:00:…` MAC — a CI test helper, not DHCP),
disables resolved/timesyncd, makes `/tmp` tmpfs, disables predictable NIC names, strips
`/usr/share/{doc,man,info,locale}`, and creates an empty `/var/lib/dpkg`.

**Observed in the actual artifact** (reverse-engineered by reading the unpacked tree):

| Item | Present? | Notes |
|---|---|---|
| systemd | **255.4-1ubuntu8.17** | `strings .../systemd`: `systemd 255.4-1ubuntu8.17`; core lib `libsystemd-core-255.so` |
| `sshd` | **yes**, OpenSSH **9.6p1** | `ssh.socket` enabled (`sockets.target.wants/`, `ssh.service.requires/`), `Accept=no`, TCP `:22` only; `sshd-socket-generator` (Ubuntu openssh package) present |
| `systemd-ssh-generator` | **no** | not in `usr/lib/systemd/system-generators/` (see §2) |
| cloud-init | **no** | `/etc/cloud/` contains only `build.info` |
| git | **no** | would need baking or an image rebuild |
| tmux, socat, curl, ip, python3, strace, fio, iperf3, vim-tiny | yes | matches the install list above |
| dhclient, wget | no | no DHCP client; `systemd-networkd` binary present but no network config enabled |
| `apt`/`dpkg` | binaries + `ubuntu.sources` present | **dpkg status db is empty**; no network up by default |
| root login | autologin root on **ttyS0** | override in `serial-getty@ttyS0.service.d/`; root password deleted |
| machine-id, SSH host keys | baked into the artifact | every copy of the artifact ships the same machine-id and host keys |
| kernel modules | none on the rootfs | `lib/modules/` absent; drivers come from the booted kernel |

**How quickstart users add packages.** Official guidance is build-time, not post-hoc:
the docs say use `./tools/devtool build_ci_artifacts rootfs` and "Feel free to adjust the
script(s) to suit your use case"
([rootfs-and-kernel-setup.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md));
the manual recipe bind-mounts the image into a Docker container and installs packages there.
The [getting-started guide](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md)
shows the non-root-friendly workflow used in #9: `unsquashfs` → edit tree → `mkfs.ext4 -d`.
There is **no official in-guest package-install path**; the CI image wasn't designed for it
(empty dpkg db, no network config, no cloud-init).

## 2. systemd-ssh-generator and SSH over AF_VSOCK

**Introduced in systemd v256, not v255.** `src/ssh-generator/ssh-generator.c` does not exist
at tags v253–v255 (raw fetch 404) and exists at v256. v256 NEWS, "SSH Integration": "A small
new unit generator `systemd-ssh-generator` has been added … If the system is run in a VM
providing AF_VSOCK support, it automatically binds sshd to AF_VSOCK port 22."
([v256 NEWS](https://github.com/systemd/systemd/blob/v256/NEWS),
[systemd-ssh-generator(8)](https://www.freedesktop.org/software/systemd/man/latest/systemd-ssh-generator.html);
its `systemd.ssh_auto=`/`ssh_listen=` knobs are also version 256.)

**Versions in distros.**
- Ubuntu 24.04 = systemd **255.4** → **no generator**. The stock FC CI image therefore has
  no vsock binding for sshd.
- Fedora 44 (current Cloud) = `systemd-259.5-1.fc44.x86_64`
  ([F44 package listing](https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Everything/x86_64/os/Packages/s/))
  → generator available wherever `sshd` is installed.

**Fallback that works on Ubuntu 24.04 (verified empirically).** systemd 255 already
supports vsock in socket units: `ListenStream=vsock:<cid>:<port>` with the CID optionally
empty is documented in the [v255 systemd.socket(5) source](https://github.com/systemd/systemd/blob/v255/man/systemd.socket.xml),
and parsed by `src/basic/socket-util.c` (`socket_address_parse_vsock`). Ubuntu's
openssh 9.6p1 carries `debian/patches/systemd-socket-activation.patch` which feeds
systemd-passed FDs to sshd and explicitly handles `AF_VSOCK`
([patch](https://git.launchpad.net/ubuntu/+source/openssh/plain/debian/patches/systemd-socket-activation.patch?h=ubuntu/noble-updates)).
Test (this machine, uid 1000, no network devices): add
`/etc/systemd/system/ssh.socket.d/10-vsock.conf` containing `[Socket]` / `ListenStream=vsock::22`
to the CI rootfs, boot Firecracker v1.17.0 + kernel 6.1.186 with a vsock device, then from the
host connect to `v.sock` and send `CONNECT 22\n`; response:
`OK 1073741824\nSSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.19`. Serial log shows
`Listening on ssh.socket` and `NET: Registered PF_VSOCK`. The drop-in route is one line and
needs nothing else installed.

Other standard routes:
- **socat proxy** (Firecracker's own documented pattern): `socat VSOCK-LISTEN:22,reuseaddr,fork
  TCP:127.0.0.1:22` in the guest, sshd on loopback — `socat` is already in the CI image.
- **Host-side client**: systemd ≥256 `systemd-ssh-proxy` has a `vsock-mux/<uds-path>` scheme
  aimed explicitly at "cloud-hypervisor/firecracker which … provide their own multiplexer"
  ([systemd-ssh-proxy(1)](https://www.freedesktop.org/software/systemd/man/latest/systemd-ssh-proxy.html)).
  This needs the guest side to listen on vsock somehow.

## 3. Booting distro cloud images (esp. Fedora Cloud Base) under Firecracker

**No official or community recipe found for Fedora Cloud Base on Firecracker.** FC's
[kernel policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)
validates only Amazon Linux microVM kernels (5.10, 6.1, 6.18): "While other versions and other
kernel configs might work, they are not periodically validated." FC's CI rootfs is Ubuntu;
`gh` searches of the FC repo/issues found no Fedora boot reports or tests.

**Enabling facts.**
- Firecracker supports `bzImage` since **v1.17.0** (PR #6037: "format is detected
  automatically") and `initrd_path` since v0.21.0; ACPI boot since v1.8.0
  ([CHANGELOG](https://github.com/firecracker-microvm/firecracker/blob/main/CHANGELOG.md)).
  Note the docs still recommend `vmlinux`; bzImage costs extra boot time/memory.
- Fedora 44's kernel config satisfies FC's minimal x86_64 requirements
  ([F44 config](https://src.fedoraproject.org/rpms/kernel/raw/f44/f/kernel-x86_64-fedora.config)):
  `VIRTIO_BLK=y`, `ACPI=y`, `PCI=y`, `KVM_GUEST=y`, `BLK_DEV_INITRD=y`, `BTRFS_FS=y`,
  `VIRTIO_PCI=y`, `VIRTIO_MMIO=m`, `VIRTIO_MMIO_CMDLINE_DEVICES=y`, `VIRTIO_VSOCKETS=m`.
  So with `--enable-pci`, a Fedora `vmlinuz` + btrfs root can boot without an initrd; the
  legacy MMIO transport needs `virtio_mmio` from initramfs (it is a module). Without
  `--enable-pci` the guest probes PCI and FC logs harmless IO faults (observed:
  `IO write @ 0xcf8 … Failed to find address range`).
- FC does not manage the root partition: it appends `root=/dev/vda` unless `boot_args` give
  `root=PARTUUID=…`; Fedora's root is a partition, so `root=` must be passed explicitly.
- The CI image's own kernel (`vmlinux-6.1.186`) is the only kernel #9 proved on this host.

**Fedora Cloud image facts.** Built from the Fedora Cloud Base kickstart; root filesystem
**BTRFS**, "depend[s] upon cloud-init by default", SELinux enforcing, no default firewall;
default cloud user is `fedora` with wheel/sudo (`fedora-cloud/cloud-init`
[cloud-init-fedora.cfg](https://github.com/fedora-cloud/cloud-init/blob/master/cloud-init-fedora.cfg),
[CloudBase Technical Specification](https://fedoraproject.org/wiki/CloudBase/Technical_Specification)).

**cloud-init NoCloud under Firecracker (standard recipe).** cloud-init docs document: a vfat or
iso9660 filesystem labelled **`CIDATA`** with `user-data`/`meta-data` in its root, or a line
config `ds=nocloud;s=…` delivered via kernel cmdline or DMI string
([NoCloud reference](https://cloudinit.readthedocs.io/en/latest/reference/datasources/nocloud.html)).
Mapping to FC: attach the seed file as a second drive with `is_root_device: false` (FC drives API)
and/or put `ds=nocloud;s=…` in `boot-source.boot_args`. FC exposes no SMBIOS/DMI serial or
OEM-string configuration (nothing in the swagger or `src/`), so the QEMU-style
`-smbios type=1,serial=ds=nocloud` route has no FC equivalent — use the cmdline or the labeled
disk. (The FC CI Ubuntu rootfs has no cloud-init; Fedora Cloud and Ubuntu cloud images do.)

## 4. Firecracker vsock mechanics

From [docs/vsock.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md):
- Device config: `guest_cid` (swagger: `minimum: 3`) and `uds_path`, e.g.
  `{"guest_cid": 3, "uds_path": "v.sock"}`.
- **Host → guest**: connect to the `uds_path` UDS, send text `CONNECT <port>\n`; FC replies
  `OK <hostside-port>\n` and bridges to the guest listener.
- **Guest → host**: guest connects to CID **2** on a port; FC forwards to a host UDS at
  `<uds_path>_<port>`. Linux defines `VMADDR_CID_HOST = 2` and `VMADDR_CID_LOCAL = 1`
  ([uapi/linux/vm_sockets.h](https://github.com/torvalds/linux/blob/master/include/uapi/linux/vm_sockets.h)).
- Documented examples use `socat` on both ends; snapshot restores can rename the UDS via
  `vsock_override`.

## 5. Precedents for "commands to run on start/resume" manifests

- **devcontainer.json** lifecycle scripts run in a fixed order:
  `initializeCommand` (host), `onCreateCommand`, `updateContentCommand`, `postCreateCommand`,
  then `postStartCommand` — "a command to run **each time** the container is successfully
  started" — and `postAttachCommand` — "each time a tool has successfully attached";
  `waitFor` selects which one must finish before the tool connects. Commands are string
  (shell) or argv (no shell), and a failure skips later scripts.
  ([spec reference](https://containers.dev/implementors/json_reference/))
- **Gitpod `.gitpod.yml` tasks** have `before` (prep), `init` (long setup, runs in prebuild
  and is cached), `command` (start app; may run forever). On **restart** Gitpod re-runs
  `before` + `command` only; `init` is not re-run once a prebuild exists
  ([tasks docs](https://www.gitpod.io/docs/configure/workspaces/tasks)). This is the closest
  published model to pluto's pause/resume split.
- **devenv** exposes `enterShell` (bash on entering the shell), `processes` (started by
  `devenv up`, run in the foreground), and `tasks` (a before/after dependency graph)
  ([options reference](https://devenv.sh/reference/options/)).
- Common shape: creation-time commands, start-time commands, attach-time commands — none
  resurrects processes; everything re-runs a command on the new boot.

## Empirical test log (kept, no processes left)

Directory `/tmp/opencode/pluto-facts/vsock-test/`: `serial.log`, `fc-vsock.json`,
`run-test.sh`, `overlay/…/10-vsock.conf`. Firecracker v1.17.0, kernel `vmlinux-6.1.186`,
1 vCPU/256 MiB, no network devices, rootfs as in §1 plus the drop-in. Result: boot to
autologin prompt (~3.4 s this run; console had extra PCI probe noise vs #9's 1.77 s), then
`OK 1073741824` + `SSH-2.0-OpenSSH_9.6p1` over the host side of the vsock. `pgrep` after the
run: no firecracker process. Large artifacts (disk, kernel, binary) deleted.
