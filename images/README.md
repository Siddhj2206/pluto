# M0 base image

Builds the bootable artifact the runner will boot: a stock kernel, an ext4
rootfs, and a manifest — all without root.

## Host prerequisites

- Linux x86_64 with KVM (`/dev/kvm` readable)
- unprivileged user namespaces (`/usr/bin/unshare -Urn`) and `/dev/net/tun`
- rootless podman, `slirp4netns`, `mkfs.ext4`/`debugfs` (e2fsprogs),
  `curl`, `tar`, `sha256sum`, `ssh`/`ssh-keygen`
- Go toolchain (builds `pluto-agent` and `pluto-vsock`)
- ~3 GB free disk under `images/out` (the rootfs image is sparse)

## Build

```sh
images/build.sh
```

Produces `images/out/{vmlinuz, rootfs.img, manifest.json}` plus `bin/` and
`cache/`. The manifest records versions and SHA-256 for the kernel,
Firecracker, the rootfs, and the guest agent.

## Boot and verify

```sh
images/boot.sh            # boot rootless, verify ssh + egress, print timing
images/boot.sh --shell    # ... then drop into an interactive ssh session
images/boot.sh --keep     # leave the VM running for inspection
```

Environment overrides: `PLUTO_IMAGE_OUT` (artifact dir), `BOOT_ARGS` (kernel
cmdline), `DISK_MB` (build-time rootfs size).

Expected result (measured 2026-10-04, 12-core host, three runs):

```
==> guest sshd up in 2.54s: SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.19
==> ssh: Linux 6.1.186 dev Linger=yes active
==> egress: 7fd1a60b01f91b314f59955a4e4d4e80d8edf11d
==> ok: boot 2.54s, ssh ok, egress ok
```

## What is baked in

- Ubuntu 24.04 with `openssh-server`, `git`, `iproute2`, `dbus`, `udev`, `systemd`
- user `dev` (uid 1000) with linger enabled, so the user manager runs at boot
- sshd socket-activated on `vsock::22` and loopback
- systemd-networkd static `10.0.2.15/24` via `10.0.2.2`, DNS `10.0.2.3`
  (the slirp4netns address plan)
- a placeholder `pluto-agent` unit, so the service seam is exercised each boot
- an empty `machine-id`, so systemd mints one per boot

## Rootless networking

Firecracker and slirp4netns cannot share a TAP device: slirp holds the fd of
the TAP it creates, and the VMM's attempt to attach to it fails with EBUSY.
The boot script runs Firecracker inside `unshare -Urn`, lets it create
`tap-fc`, has slirp4netns create `tap-slirp`, and bridges the two inside the
namespace. The guest MTU is 1500 and slirp runs with `--mtu=1500`; a larger
slirp MTU drops the large frames of a TLS handshake.

## Known M0 limitations

- SSH host keys are baked into the image; per-box identity arrives with the
  runner.
- The static slirp address plan is a bring-up choice, not an architecture.
- The guest agent is a placeholder that keeps the unit alive; the real vsock
  protocol is the guest-agent ticket.
