# M0 base image

Builds the bootable artifact the runner will boot: a stock kernel, an ext4
rootfs, and a manifest — all without root.

## Host prerequisites

- Linux x86_64 with a writable `/dev/kvm`
- unprivileged user namespaces (`/usr/bin/unshare -Urn`) and `/dev/net/tun`
- rootless podman, `slirp4netns`, `mkfs.ext4`/`debugfs` (e2fsprogs),
  `curl`, `tar`, `sha256sum`, `ssh`/`ssh-keygen`, `ip` (iproute2)
- Go toolchain (builds `pluto-agent` and `pluto-vsock`)
- ~3 GB free disk under `images/out` (the rootfs image is sparse)

Rootless podman is a system-level prerequisite: the user needs subuid/subgid
entries (`/etc/subuid`, `/etc/subgid`) and newuidmap/newgidmap, which the
`podman info` check will report if missing. Nothing else is installed
system-wide; the build and boot scripts change no host files.

## Build

```sh
images/build.sh
```

Produces `images/out/{vmlinuz, rootfs.img, manifest.json}` plus `bin/`,
`cache/`, and `context/` working directories. The manifest records versions
and SHA-256 for the kernel, Firecracker, the rootfs, and the guest agent.
The base image is pinned by digest in the Containerfile; apt package
versions inside it float until image publishing exists.

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
==> guest sshd up in 2.51s: SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.19
==> ssh (first command after 0.34s): Linux 6.1.186 dev Linger=yes active
==> egress: 7fd1a60b01f91b314f59955a4e4d4e80d8edf11d
==> ok: sshd 2.51s, first command 0.34s, ssh ok, egress ok
```

## What is baked in

- Ubuntu 24.04 with `openssh-server`, `git`, `iproute2`, `dbus`, `udev`,
  `libpam-systemd` (the user manager and linger), `systemd`
- user `dev` (uid 1000) with linger enabled, so the user manager runs at boot
- sshd socket-activated on `vsock::22` and loopback
- systemd-networkd static `10.0.2.15/24` via `10.0.2.2`, DNS `10.0.2.3`
  (the slirp4netns address plan)
- the pluto guest agent as a user unit, listening on vsock to apply the box
  contract (provision, wake, services)
- an empty `machine-id`, so systemd mints one per boot

## Using the artifact from pluto

The daemon keeps a local image store. `pluto image import images/out` verifies
the artifact's hashes against its manifest, copies it into
`<state>/images/<version>/` (reflink when the filesystem supports it), and
prints the content-derived version:

```sh
pluto image import images/out   # -> imported image fe2ff2088c425d24
pluto image ls
```

Boxes pin the version they first booted with, so rebuilding or re-importing
never changes an existing box; new boxes use the newest imported version.
Box disks are reflinks of the base image when the filesystem supports it (a
plain copy otherwise), so `pluto up` copies nothing eagerly and blocks are
shared until written. The runner then starts the box under
`pluto-box@<uuid>.service` and waits for sshd over vsock before reporting it
running.

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
