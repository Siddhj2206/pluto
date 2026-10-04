# Boot-path check: Firecracker and Cloud Hypervisor on the first host

Ticket: #9. Status: done. All work ran as the unprivileged user `sid`
(uid 1000) on the current machine; no sudo, no system-wide changes.

## Verdict in one line

Both Firecracker v1.17.0 and Cloud Hypervisor v53.0 boot a stock Ubuntu 24.04
rootfs with the stock Firecracker CI kernel (6.1.186) to a working root shell
in **~1.5-1.8 s** (stock systemd boot) or **~0.5-0.85 s** (minimal
`/pluto-init` boot) on this host, entirely user-level, with no networking
required.

## Measured numbers

Method: wall clock from immediately before `execve` of the VMM to the first
serial-console line matching a marker, plus a host→guest input round-trip to
prove the shell actually works. Median of 3 runs each; a fresh ext4 image was
built for every run. 1 vCPU, 256 MiB RAM, no network devices, no rate
limiting, no CPU pinning; host (12 cores) under normal desktop load.

| Config | kernel entry¹ | init marker² | shell prompt³ | input round-trip⁴ |
|---|---|---|---|---|
| Firecracker, stock systemd boot | 0.031 s | — | **1.772 s** (1.765/1.772/1.790) | 1.773 s |
| Firecracker, `init=/pluto-init` | 0.033 s | **0.839 s** (0.838/0.839/0.839) | — | 0.840 s |
| Cloud Hypervisor, stock systemd boot | 0.028 s | — | **1.579 s** (1.537/1.579/1.669) | 1.580 s |
| Cloud Hypervisor, `init=/pluto-init` | 0.023 s | **0.480 s** (0.479/0.480/0.529) | — | — |

1. First serial line matching `Linux version` — VMM startup + kernel
   decompression/entry.
2. First line of a custom `/init` script (`PLUTO_INIT_MARKER`) that then
   `exec`s `/bin/sh`; the script also runs `/bin/sh -c 'echo ...'` to prove a
   shell executes.
3. First occurrence of `root@ubuntu-fc-uvm:~#` on ttyS0 (the CI image
   autologins root on ttyS0, see below).
4. Host sends `sync; echo PLUTO_SHELL_OK` over the guest console and sees the
   echo come back. For Firecracker this goes through the VMM's stdin pipe;
   for Cloud Hypervisor through `--serial socket=<unix path>` (its `tty` mode
   does not accept input from a pipe — see Observations).

Both VMMs were killed with SIGTERM at the end of each run; no VM was left
running between runs or afterwards.

Cross-checks and single runs:

- Firecracker booted identically inside a rootless user+net namespace
  (`unshare -Urn`): init marker 0.845 s. KVM access works from the namespace
  because `/dev/kvm` is world-writable.
- Firecracker with a virtio-vsock device configured: boots fine, creates its
  host UDS (`srwxr-xr-x`); kernel 6.1 guest has the vsock driver and the host
  `/dev/vhost-vsock` is world-writable, so vsock is usable unprivileged.
- Cloud Hypervisor with `--vsock cid=3,socket=...`: boots fine; the socket
  exists while the VM runs (`srwx------`) and is removed on clean exit.
- A single earlier Cloud Hypervisor run without `image_type=raw` (deprecated
  autodetect path) measured 3.18 s to prompt; re-runs with explicit
  `image_type=raw` were consistently 1.54-1.67 s, so the slow value is
  excluded.
- Firecracker's own `log_path`/`metrics_path` were configured; the metrics
  JSON has no boot-time field, so the harness measurement is the only number.

Deliverable runs: Firecracker is ~0.35 s slower than Cloud Hypervisor to the
minimal init marker (0.84 s vs 0.48 s) and ~0.2 s slower to the stock shell.
The gap appears to be device-model/VMM-setup related, not kernel boot
(kernel entry times are nearly identical). Not investigated further; more
repetitions under load are needed before treating it as a stable difference.

## Artifacts and provenance (all SHA-256 recorded)

Everything was downloaded on 2026-10-04 and verified; hashes are in
`docs/research/boot-path-check/SHA256SUMS.txt`.

| Artifact | Source | Version | SHA-256 |
|---|---|---|---|
| Firecracker binary | `github.com/firecracker-microvm/firecracker/releases/download/v1.17.0/firecracker-v1.17.0-x86_64.tgz` | v1.17.0 (2026-09-10) | tgz `06094a11…ade558` (matches published sum) |
| Cloud Hypervisor binary | `github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v53.0/cloud-hypervisor-static` | v53.0 | `448af3d4…c29ecc` |
| Guest kernel | `s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/x86_64/vmlinux-6.1.186` | Linux 6.1.186 (FC CI build) | `ea0e55d0…1f69c8` |
| Guest rootfs | `…/20260930-a738f18a8db0-0/x86_64/ubuntu-24.04.squashfs` | Ubuntu 24.04.5 LTS | `008a05de…b28f0` |

Both VMM binaries are static-PIE ELF x86-64 and need only extract + `chmod +x`.
Kernel and rootfs are the artifacts the official Firecracker quickstart
points at (same S3 bucket); no distribution packages were installed.

Tooling used from the host: `mkfs.ext4` 1.47.4, `unsquashfs` 4.7.5,
`debugfs`, Python 3.14.7. Host kernel: 7.2.8-300.fc45.x86_64, KVM AMD.

## Rootfs construction (unprivileged)

The CI rootfs is a squashfs; Firecracker and Cloud Hypervisor need a raw
ext4 disk:

```bash
cd /tmp/opencode/pluto-research-boot
curl -fLO https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/x86_64/ubuntu-24.04.squashfs
unsquashfs -d vm/rootfs ubuntu-24.04.squashfs
truncate -s 1536M vm/run-stock.ext4
mkfs.ext4 -q -d vm/rootfs -F -L pluto-root vm/run-stock.ext4
```

No `sudo chown -R root:root` was needed (the quickstart does this): guest
root can execute files owned by uid 1000. Verified by booting.

For the minimal-init variant, a `/pluto-init` script was injected without
mounting:

```bash
debugfs -w -R "write vm/pluto-init /pluto-init" vm/run-selftest.ext4
debugfs -w -R "sif /pluto-init mode 0100755" vm/run-selftest.ext4
```

`vm/pluto-init`:

```sh
#!/bin/sh
echo "PLUTO_INIT_MARKER uptime=$(cut -d' ' -f1 /proc/uptime)"
echo "PLUTO_SHELL_ALIVE=$(/bin/sh -c 'echo alive-pid-$$')"
exec /bin/sh
```

(`/proc` is not mounted under a bare init, so the `uptime=` value is empty;
the boot itself is unaffected. The kernel's own `[0.75]` dmesg timestamp is
the guest-side equivalent.)

## Console and measurement harness

`run_vm.py` (copied into `docs/research/boot-path-check/`) spawns the VMM,
timestamp-reads serial output line by line, fires regex rules, and can write
input to the guest console. For Firecracker, input is the VMM's stdin pipe
(works). For Cloud Hypervisor, input goes over `--serial socket=<path>`
(works); its `--serial tty` mode produced output but ignored stdin when stdio
was a pipe. Serial output is captured to `<name>.serial.log` next to
`results.jsonl`.

Firecracker was driven without its HTTP API:

```bash
firecracker --no-api --config-file vm/fc-stock.json
```

with a config setting `boot-source`, one `drives` entry, `machine-config`
(1 vCPU / 256 MiB), `logger.log_path`, `metrics.metrics_path`, and boot args
`console=ttyS0 reboot=k panic=1` (plus `rw init=/pluto-init` for the minimal
variant).

Cloud Hypervisor was driven directly from its CLI:

```bash
cloud-hypervisor \
  --kernel assets/vmlinux-6.1.186 \
  --disk path=vm/run-stock.ext4,image_type=raw \
  --cpus boot=1 --memory size=256M \
  --cmdline "console=ttyS0 root=/dev/vda rw reboot=k panic=1" \
  --serial socket=$PWD/vm/ch-serial.sock --console off
```

For Cloud Hypervisor, `image_type=raw` should be explicit: without it the
disk type is auto-detected (deprecation warning) and sector-0 writes are
rejected with a `ReadOnly` warning.

## Userspace requirements

- **root vs user — none of this needed root.** Both VMMs ran as uid 1000.
  The requirements are: read/write access to `/dev/kvm` (world-writable on
  this host; normally `kvm` group or ACL), ordinary write access to the disk
  image files, and an API/control socket or config file owned by the user.
  Firecracker's `jailer` (which needs privileges) was not used and is not
  needed for this path.
- **Networking — not required to boot.** No networking was configured for any
  measurement. TAP creation requires `CAP_NET_ADMIN` on the host netns, i.e.
  root. However, on this host unprivileged user+net namespaces are enabled
  (`user.max_user_namespaces = 60529`): inside `unshare -Urn` the process has
  `cap_net_admin` in its netns, a tap device can be created, and Firecracker
  boots inside that namespace (verified). Host-side plumbing for real
  connectivity from that netns (slirp/passt/veth+NAT) was **not** tested —
  that is the next networking experiment.
- **vsock — available unprivileged.** `/dev/vhost-vsock` exists and is
  world-writable; Firecracker's vsock device activates and creates its host
  UDS; Cloud Hypervisor's vsock socket works too. No `modprobe` was needed
  (driver already present on this kernel).

## Host changes made, and how to undo them

- All files live under `/tmp/opencode/pluto-research-boot/` (tmpfs, ~572 MB
  after cleanup): the two VMMs, kernel/squashfs, one built ext4 image, the
  harness, and all serial/stderr logs and `results.jsonl`.
- A git worktree for this branch is at
  `/tmp/opencode/pluto-research-boot/branch` (created with
  `git worktree add -b research/boot-path-check ... master`).
- Nothing else: no packages installed, no files in `$HOME`, no services, no
  systemd units, no TAP/bridge/firewall changes on the host, no sudo, no
  edited system files.
- Undo: `rm -rf /tmp/opencode/pluto-research-boot` and, from the main
  checkout, `git worktree remove /tmp/opencode/pluto-research-boot/branch`
  (after this branch is merged/abandoned). A reboot also clears `/tmp`
  (tmpfs). `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun` modes are unchanged.
- Left for inspection: `vm/logs/*.serial.log` (all 12 measured boots plus
  exploratory runs), `vm/logs/results.jsonl` (raw JSON per run),
  `vm/run-stock.ext4` (bootable image), `vm/fc-*.json` configs. The
  `research/boot-path-check` branch is the durable pointer; the tmpfs copy
  disappears on reboot.

## Observations worth carrying into the runner decision

- Both VMMs are trivially deployable as single static binaries; the whole
  boot path needs no daemon, no root, no network.
- Serial console is a viable control channel: Firecracker stdin works over a
  pipe; Cloud Hypervisor needs `socket=` for input. For a host agent, the
  socket form is arguably better (explicit lifecycle, no stdio plumbing).
- Boot is fast enough that "boot a machine" is not a UX problem; disk-image
  state, not boot time, is the design constraint. Stock systemd boot costs
  ~1 s over a minimal init, so the guest image's init choice matters more
  than the VMM choice.
- Firecracker writes its banner line into guest serial stdout, so marker
  parsing should ignore non-kernel lines; Cloud Hypervisor keeps its log on
  stderr.
- Firecracker leaves its vsock UDS behind after SIGTERM; Cloud Hypervisor
  removes its serial/vsock sockets on clean exit. Cleanup is trivial but the
  host agent should not assume sockets vanish.
- Caveat: assets were freshly downloaded and page-cache-warm for most runs;
  cold-cache boot numbers were not isolated (dropping caches needs root).
  Fresh-image-per-run removed guest state, not host cache state.
