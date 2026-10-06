# Base image

Builds the bootable artifact the runner will boot: a stock kernel, an ext4
rootfs, and a manifest — all without root.

The builder is host-only: it downloads the kernel and Firecracker, builds the
guest agent, and assembles the rootfs with rootless podman. CI builds it but
does not boot it: the
[image-build job](../.github/workflows/image-build.yml) runs the builder on PRs
touching the image or build inputs and asserts the artifact imports (see
[docs/testing.md](../docs/testing.md)).

## Host prerequisites

Building (`go run ./cmd/pluto-image-builder`) needs:

- Linux x86_64
- unprivileged user namespaces and rootless podman with subuid/subgid entries
  (`/etc/subuid`, `/etc/subgid`) and newuidmap/newgidmap — `podman info` reports
  what is missing
- `mkfs.ext4` from **e2fsprogs ≥ 1.47.1** (1.47.1 added `SOURCE_DATE_EPOCH`
  clamping; Ubuntu 24.04 ships 1.47.0, Fedora ships 1.47.4), and GNU tar. The
  builder probes `mkfs.ext4 -V` and fails before the expensive build when the
  host's version cannot honor `SOURCE_DATE_EPOCH`, rather than silently baking
  the build clock into the rootfs.
- a Go toolchain. The builder builds the guest helpers with the toolchain
  pinned in `pins.yaml` (`GOTOOLCHAIN=go1.27.1`), downloading it when the host
  does not have it, so the helper binaries do not depend on the host's Go.
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
base image tag and digest, the apt snapshot date and packages, the Go
toolchain that builds the guest helpers, the kernel URL+hash, and the
Firecracker version+URL+hash. The builder verifies both
downloads against their pinned SHA-256 before copying anything into the
artifact, so a truncated or substituted download fails the build rather than
importing cleanly and failing at first boot. It passes the base image, apt
snapshot, and package set to the Containerfile as build args, so the manifest
stays the single source; apt resolves against `snapshot.ubuntu.com`, so
package versions do not float.

The kernel is pinned to the Firecracker CI artifact `vmlinux-6.18.51`. It
boots directly from the root block device — no initramfs — because the drivers
a box needs are built in: virtio-blk (root disk), virtio-vsock (sshd and the
agent), virtio-net (egress), and the i8042 controller that turns pause's
`SendCtrlAltDel` into a clean reboot (`reboot=k`). The Firecracker config
references no initrd, and the base image ships none.

Produces `images/out/{vmlinuz, rootfs.img, manifest.json}` plus `bin/`,
`cache/`, and `context/` working directories. Flags (defaults in parentheses):
`-pins` (`images/pins.yaml`), `-out` (`images/out` or `$PLUTO_IMAGE_OUT`),
`-disk-mb` (2048), `-root` (`.`), `-images` (`images`).

## Reproducibility

The artifact is hash-locked: two clean builds from the same pins and source
produce identical `vmlinuz`, `rootfs.img`, and `manifest.json`. The recipe:

- **Every input is pinned** in [`pins.yaml`](pins.yaml): the base image by
  digest, the apt packages to a dated `snapshot.ubuntu.com` index, the Go
  toolchain that builds the guest helpers, the kernel by URL+sha256, and the
  Firecracker tarball by URL+sha256. Both downloads are verified before
  anything is copied into the artifact.
- **`SOURCE_DATE_EPOCH` is derived from the apt snapshot pin**, not the clock.
  It is passed to the Containerfile and to `mkfs.ext4`; assembly also clamps
  every file and symlink mtime to it. The mkfs step needs e2fsprogs ≥ 1.47.1
  (see the host prerequisites); older versions ignore `SOURCE_DATE_EPOCH` and
  write the build time into the filesystem metadata (superblock and inode
  timestamps).
- **ext4 assembly is deterministic.** The rootfs is extracted with numeric
  ownership (so `dev` is uid 1000 and system files uid 0), then handed to
  `mkfs.ext4 -d` with a fixed UUID, label, and directory hash seed. `mke2fs`
  walks the tree in sorted name order, so the podman-export tar's entry order
  does not leak in. (The host's e2fsprogs may be built without libarchive, as
  Fedora's is, so the tree is passed as a directory rather than a tarball;
  clamping mtimes explicitly replaces the reproducible-builds tar step.)
- **No wall-clock field is hashed.** `manifest.json` is schema 2:
  `source_date_epoch` replaced the old `built_at`, and it derives from the
  pinned snapshot date. The apt snapshot is also recorded as
  `rootfs.apt_snapshot`.
- **Volatile build-time content is removed.** Package postinsts append
  timestamped logs and stamp `/etc/shadow` with the build day; the Containerfile
  drops `/var/log` and pins every shadow day to the `SOURCE_DATE_EPOCH` day. It
  also deletes `ldconfig`'s `/var/cache/ldconfig/aux-cache`, which caches each
  library's build-clock mtime and so differs between otherwise identical builds
  (the cache is rebuilt on demand, so removing it is safe).
- **SSH host keys are not baked.** Random host keys would change the rootfs
  every rebuild, so the image ships none; `ssh.service` generates per-box keys
  on first start (see the `ssh.service.d` drop-in). The box's user key is
  injected by the runner into `authorized_keys`.
- **The guest helpers are built with the pinned toolchain**, `-trimpath`, and
  `-buildvcs=false`, so `pluto-agent` and `pluto-vsock` carry no host path or
  VCS stamp. `pins.yaml`'s `toolchain.go` is the version the builder forces via
  `GOTOOLCHAIN`; CI and the host fetch that exact toolchain, so rootfs.img's
  hash does not depend on whichever Go the host happens to have. Bump the pin
  by hand alongside `go.mod`'s `go` directive.

The double-build bar is asserted by a host-only test (not in CI — it needs
rootless podman and network):

```sh
go test -tags host ./internal/imagebuilder/ -run TestDoubleBuildDeterminism -v -timeout 30m
```

Measured 2026-10-06 on the M3 branch: two clean builds — separate output
directories and separate podman storage roots — were byte-identical for all
three artifact files.

CI runs the same build on `ubuntu-latest` (podman, no KVM) and then imports the
result:

```sh
go run ./cmd/pluto-image-builder
PLUTO_IMAGE_ARTIFACT=images/out \
  go test ./internal/runner -run TestImportBuiltArtifact -v
```

The import re-verifies every hash against `manifest.json`, so a broken artifact
fails the job; a bad pin fails the build first, because the builder checks each
download against `pins.yaml`. Download and podman-layer caches keep the job
practical. The workflow is
[.github/workflows/image-build.yml](../.github/workflows/image-build.yml).

## Boot and verify

```sh
images/boot.sh            # boot rootless, verify ssh + egress, print timing
images/boot.sh --shell    # ... then drop into an interactive ssh session
images/boot.sh --keep     # leave the VM running for inspection
```

Environment overrides: `PLUTO_IMAGE_OUT` (artifact dir, also `-out`),
`BOOT_ARGS` (kernel cmdline), `-disk-mb` (build-time rootfs size).

Expected result (boot timings measured 2026-10-04 on a 12-core host, three
runs, on the earlier **6.1.186** kernel pin; they have not been re-measured on
the current **6.18.51** pin, so treat the seconds as indicative. The kernel
line below reflects the current 6.18.51 pin):

```
==> guest sshd up in 2.51s: SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.19
==> ssh (first command after 0.34s): Linux 6.18.51+ dev Linger=yes active
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

## Known limitations

- SSH host keys are generated per box at first boot (the image ships none, so
  rebuilds stay byte-identical); the box's user key is injected by the runner.
- The static slirp address plan is a bring-up choice, not an architecture.
