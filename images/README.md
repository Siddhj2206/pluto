# Base image

Builds the bootable artifact the runner will boot: a stock kernel, an ext4
rootfs, and a manifest — all without root.

The builder is host-only: it downloads the kernel and Firecracker, builds the
guest agent, and assembles the rootfs with rootless podman. It is not run in
CI except for the image-build job's import check (see
[docs/testing.md](../docs/testing.md)).

## Host prerequisites

Building (`go run ./cmd/pluto-image-builder`) needs:

- Linux x86_64
- unprivileged user namespaces and rootless podman with subuid/subgid entries
  (`/etc/subuid`, `/etc/subgid`) and newuidmap/newgidmap — `podman info` reports
  what is missing
- `mkfs.ext4`, `tar` (e2fsprogs, GNU tar)
- a Go toolchain (builds `pluto-agent` and `pluto-vsock`)
- ~3 GB free disk under `images/out` (the rootfs image is sparse)

Booting (`images/boot.sh`) additionally needs a writable `/dev/kvm`,
`slirp4netns`, `/dev/net/tun`, `ssh`/`ssh-keygen`, `debugfs`, and `ip`
(iproute2). Nothing is installed system-wide; the builder and boot script change
no host files.

## Build

```sh
go run ./cmd/pluto-image-builder
```

Every image input lives in one pins manifest, [`pins.yaml`](pins.yaml): the
base image tag and digest, the apt snapshot date and packages, the kernel
URL+hash, and the Firecracker version+URL+hash. The builder verifies both
downloads against their pinned SHA-256 before copying anything into the
artifact, so a truncated or substituted download fails the build rather than
importing cleanly and failing at first boot. It passes the base image, apt
snapshot, and package set to the Containerfile as build args, so the manifest
stays the single source; apt resolves against `snapshot.ubuntu.com`, so
package versions do not float.

Produces `images/out/{vmlinuz, rootfs.img, manifest.json}` plus `bin/`,
`cache/`, and `context/` working directories. Flags (defaults in parentheses):
`-pins` (`images/pins.yaml`), `-out` (`images/out` or `$PLUTO_IMAGE_OUT`),
`-disk-mb` (2048), `-root` (`.`), `-images` (`images`).

## Boot and verify

```sh
images/boot.sh            # boot rootless, verify ssh + egress, print timing
images/boot.sh --shell    # ... then drop into an interactive ssh session
images/boot.sh --keep     # leave the VM running for inspection
```

Environment overrides: `PLUTO_IMAGE_OUT` (artifact dir, also `-out`),
`BOOT_ARGS` (kernel cmdline), `-disk-mb` (build-time rootfs size).

Expected result (measured 2026-10-04, 12-core host, three runs):

```
==> guest sshd up in 2.51s: SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.19
==> ssh (first command after 0.34s): Linux 6.1.186 dev Linger=yes active
==> ctrl-alt-del target: /usr/lib/systemd/system/reboot.target
==> egress: 7fd1a60b01f91b314f59955a4e4d4e80d8edf11d
==> ok: sshd 2.51s, first command 0.34s, ssh ok, egress ok
```

## What is baked in

- Ubuntu 24.04 with `openssh-server`, `git`, `iproute2`, `dbus`, `udev`,
  `libpam-systemd` (the user manager and linger), `systemd`, `tmux`
- user `dev` (uid 1000) with linger enabled, so the user manager runs at boot
- sshd socket-activated on `vsock::22` and loopback
- systemd-networkd static `10.0.2.15/24` via `10.0.2.2`, DNS `10.0.2.3`
  (the slirp4netns address plan)
- the pluto guest agent as a user unit, listening on vsock to apply the box
  contract (provision, wake, services, sessions)
- `ctrl-alt-del.target` pinned to `reboot.target`, so a pause's
  `SendCtrlAltDel` runs systemd's orderly shutdown, the kernel resets through
  the i8042 controller (`reboot=k`), and Firecracker exits
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
