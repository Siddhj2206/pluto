# Reproducible microVM image construction

Research note for #68 (parent: #66 M3 platform refresh), 2026-10-06. Primary sources
cited with URLs and access dates. The current pipeline is `images/build.sh` →
`podman build` → `podman export` → `mkfs.ext4` → `manifest.json`; rootless podman
stays the rootfs mechanism. This note compares construction paths, designs a pins
manifest and Go builder, and states a determinism bar.

---

## 1. Current pipeline and its non-determinism

`images/build.sh` (accessed 2026-10-06, repo state at `m3/platform-refresh`):

- Pins the base image by digest in `images/Containerfile`
  (`ubuntu:24.04@sha256:534baea6…`).
- Pins the kernel URL and `KERNEL_SHA256` as shell variables.
- Does **not** pin apt package versions: `apt-get update && apt-get install` runs
  against the live archive, so package versions float.
- Does **not** pin the Firecracker binary hash: `FC_VERSION` is set but the
  tarball is downloaded and extracted without a checksum verification.
- Records a wall-clock `built_at` timestamp in `manifest.json`.
- The ext4 UUID is fixed (`-U 0f15a7e1-…`) but the `mkfs.ext4` hash seed is
  random, and file mtimes inside the rootfs come from the podman export
  (build-time timestamps).

Consequence: two builds from the same inputs produce different `rootfs.img` bytes
and a different `manifest.json` (the `built_at` field alone guarantees this).

---

## 2. Comparable image-build systems

### 2.1 Firecracker CI

**How it works.** `resources/rebuild.sh` (accessed 2026-10-06,
[source](https://github.com/firecracker-microvm/firecracker/blob/main/resources/rebuild.sh))
builds the Ubuntu 24.04 CI rootfs via `build_ci_rootfs ubuntu:24.04
"rootfs/setup-ubuntu-ci.sh"`. The setup script
([source](https://github.com/firecracker-microvm/firecracker/blob/main/resources/rootfs/setup-ubuntu-ci.sh))
runs `apt update && apt install -y --no-install-recommends` with a fixed
package list but **no snapshot pin** — package versions float against the live
archive. The base image is `ubuntu:24.04` (tag, not digest). The rootfs is
built inside Docker (`prepare_docker` / `build_rootfs` in `tools/functions`),
exported, and turned into a squashfs artifact uploaded to S3.

**Determinism posture.** Firecracker CI does **not** claim reproducible builds.
The devtool header says "Yet another step on our way to reproducible builds"
([source](https://github.com/firecracker-microvm/firecracker/blob/main/tools/devtool)),
indicating it is aspirational, not achieved. The kernel is built from Amazon
Linux kernel sources with pinned git tags (`get_tag` in `rebuild.sh`), so the
kernel binary is source-pinned but not bit-reproducible (no `KBUILD_BUILD_TIMESTAMP`
or `SOURCE_DATE_EPOCH` handling visible in the build script).

**Relevance to pluto.** Firecracker's kernel policy
([source](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md),
accessed 2026-10-06) validates guest kernels 5.10, 6.1, and 6.18 on 4K page
size. The 6.1 guest support window closed 2026-09-02; 6.18 is supported from
v1.16.1 with end-of-support 2028-06-01. The CI kernel artifacts
(`vmlinux-6.18.51` etc.) are the right source for pluto's kernel pin.

### 2.2 Bluefin / projectbluefin (Fedora Silverblue)

**How it works.** Bluefin builds OCI images from a Containerfile with a
digest-pinned base image ARG managed by Renovate. The projectbluefin fork
([source](https://github.com/projectbluefin/bluefin/blob/main/THEPATTERN.md),
accessed 2026-10-06) builds directly on `quay.io/fedora-ostree-desktops/silverblue:43@sha256:…`
with the digest pin in a Containerfile ARG. Image versions are declared in
`image-versions.yml` (a structured YAML file with `image:`, `tag:`, `digest:`)
driven by a Renovate `custom.regex` manager.

**Renovate pattern.** The projectbluefin org config
([source](https://github.com/projectbluefin/.github/blob/main/org-inherited-config.json),
accessed 2026-10-06) uses:

```json
{
  "customManagers": [{
    "customType": "regex",
    "managerFilePatterns": ["/image-versions(\\.[^.]+)?\\.(yaml|yml)$/"],
    "matchStrings": [
      "image:\\s*(?<packageName>\\S+)\\s*tag:\\s*(?<currentValue>\\S+)\\s*digest:\\s*(?<currentDigest>sha256:[a-f0-9]+)"
    ],
    "datasourceTemplate": "docker"
  }]
}
```

The ublue-os renovate-sandbox
([source](https://github.com/ublue-os/renovate-sandbox/blob/main/renovate.json5),
accessed 2026-10-06) shows the same pattern for Justfile variables:

```json
{
  "customType": "regex",
  "managerFilePatterns": ["/^Justfile$/"],
  "matchStrings": [
    "(?<justName>.+?)\\s*:=\\s*(?<packageName>\\S+):(?<currentValue>\\S+)@(?<currentDigest>sha256:[a-f0-9]+?)\""
  ],
  "datasourceTemplate": "docker"
}
```

**Automerge posture.** Graduated: digest-only updates automerge; major/minor
and base-image pins require manual review. Image-build CI gates pin updates.

**Relevance to pluto.** The `image-versions.yml` + `custom.regex` pattern is
directly adaptable. The key difference: pluto's pins are not all Docker images
(kernel from S3, apt snapshot date, Firecracker from GitHub releases), so the
manifest needs multiple datasource types.

### 2.3 BuildStream / FSDK (deferred)

Per ticket #66, BuildStream/FSDK is rejected for now. The measured cost is
50 min–2 h cold and ~100 GB, FSDK's kernel ships virtio-blk as a module
(breaking initrd-less boot), and it depends on a small core team. Recorded in
`docs/DEFERRED.md` with a revive trigger. Not re-litigated here.

### 2.4 Flatcar Container Linux

**How it works.** Flatcar uses a containerized SDK (Gentoo-based,
~6–8 GB) with `build_packages` and `build_image` scripts
([source](https://github.com/flatcar/scripts), accessed 2026-10-06). The SDK
builds OS images via loop devices and requires privileged access to `/dev`.
Images are composed from Gentoo packages built in the SDK.

**Determinism posture.** Flatcar's release process
([source](https://www.flatcar.org/docs/latest/devguide/release-guide),
accessed 2026-10-06) takes 6–10 hours and produces cloud images for multiple
platforms. No reproducibility claims found in the docs. The SDK is a full
Gentoo toolchain, not a deterministic build system.

**Relevance to pluto.** Low. Flatcar's model (SDK + Gentoo packages + loop
devices) is heavier than pluto's podman-based approach and does not target
reproducibility. Cited as a comparable that pluto is deliberately not
following.

### 2.5 Kata Containers osbuilder

**How it works.** Kata's `osbuilder`
([source](https://github.com/kata-containers/kata-containers/blob/main/tools/osbuilder/README.md),
accessed 2026-10-06) has two build methods:

- **Distro method** (default): creates a rootfs using distro-specific commands
  (`debootstrap` for Debian, `yum` for CentOS), provisions Kata-specific
  components, then builds an ext4 image or initrd.
- **Dracut method**: uses `dracut` to merge an overlay with host-side
  filesystem components to generate an initrd, then extracts the rootfs from
  it.

Image creation: `make USE_DOCKER=true image` runs `image_builder.sh` which
creates an ext4 image from the rootfs directory.

**Determinism posture.** No explicit reproducibility claims. The distro method
inherits the non-determinism of `debootstrap`/`yum` (floating package
versions, build-time timestamps). The dracut method is more hermetic but
still does not pin package versions or clamp timestamps.

**Relevance to pluto.** Kata's two-method model (distro vs. dracut) is a
useful contrast. pluto's podman-based approach is closer to the distro
method but with rootless podman replacing `debootstrap`. The dracut method
is not applicable (pluto boots initrd-less with virtio-blk built into the
kernel).

---

## 3. Pinning apt via snapshot.ubuntu.com

### 3.1 Mechanism

The Ubuntu snapshot service ([source](https://snapshot.ubuntu.com/),
accessed 2026-10-06;
[docs](https://ubuntu.com/server/docs/how-to/software/snapshot-service),
accessed 2026-10-06) provides dated snapshots of the Ubuntu archive at
`snapshot.ubuntu.com`. Snapshot IDs are UTC timestamps in `YYYYMMDDTHHMMSSZ`
format. Snapshots are available for any date after 2023-03-01.

On Ubuntu 24.04+, `apt` auto-detects snapshot support. Two pinning methods:

**Method A — per-repository `Snapshot:` field** (deb822 format, recommended
for Containerfile):

```
Types: deb
URIs: http://archive.ubuntu.com/ubuntu
Suites: noble noble-updates noble-backports
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
Snapshot: 20261001T000000Z
```

When `Snapshot:` is set in the sources file, `apt` always uses that snapshot
and ignores the `--snapshot` CLI option.

**Method B — `--snapshot` CLI option** (per-command):

```sh
apt update --snapshot 20261001T000000Z
apt install -y --snapshot 20261001T000000Z <packages>
```

### 3.2 Application to pluto

The Containerfile's `RUN apt-get update && apt-get install` becomes:

```dockerfile
RUN sed -i 's|URIs: http://archive.ubuntu.com/ubuntu|URIs: http://snapshot.ubuntu.com/ubuntu|' \
      /etc/apt/sources.list.d/ubuntu.sources \
 && sed -i '/Signed-By:/a Snapshot: 20261001T000000Z' \
      /etc/apt/sources.list.d/ubuntu.sources \
 && apt-get update \
 && apt-get install -y --no-install-recommends <packages> \
 && rm -rf /var/lib/apt/lists/*
```

This pins every apt package to its state at `20261001T000000Z`. The snapshot
date becomes a pin in the pins manifest (see §6).

**Uncertainty.** The `sed` approach assumes the deb822 format of
`ubuntu.sources` on Ubuntu 24.04. If the base image changes the sources
format, the `sed` breaks. A more robust approach is to overwrite
`ubuntu.sources` entirely with a known-good template, but that couples the
build to the base image's apt configuration details. The `sed` approach is
simpler and works for the current Ubuntu 24.04 base; mark as a maintenance
risk.

---

## 4. SOURCE_DATE_EPOCH and deterministic tar/ext4/mkfs

### 4.1 SOURCE_DATE_EPOCH

`SOURCE_DATE_EPOCH` is a standardized environment variable
([spec](https://reproducible-builds.org/specs/source-date-epoch/),
[docs](https://reproducible-builds.org/docs/source-date-epoch/), accessed
2026-10-06) specifying the last modification of source code, in seconds
since the Unix epoch. Build tools consume it to produce reproducible output.

For system images, the reproducible-builds.org docs
([source](https://reproducible-builds.org/docs/system-images/), accessed
2026-10-06) state the general problems:

- Filesystems have creation and/or modification timestamps.
- Filesystems contain UUIDs or labels that are not set explicitly.
- Included files have timestamps.
- Files generated at build time may be non-deterministic.

### 4.2 Deterministic tar

GNU tar's reproducibility options
([source](https://www.gnu.org/software/tar/manual/html_node/Reproducibility.html),
accessed 2026-10-06):

```sh
SOURCE_DATE_EPOCH=$(git log -1 --pretty=%ct)
TARFLAGS="
  --sort=name --format=posix
  --pax-option=exthdr.name=%d/PaxHeaders/%f
  --pax-option=delete=atime,delete=ctime
  --clamp-mtime --mtime=$SOURCE_DATE_EPOCH
  --numeric-owner --owner=0 --group=0
  --mode=go+u,go-w
"
LC_ALL=C tar $TARFLAGS -cf root.tar -C rootdir .
```

This produces a byte-identical tar from the same input tree regardless of
build time, directory order, or file ownership.

### 4.3 Deterministic ext4 with mkfs.ext4

The reproducible-builds.org system-images doc
([source](https://reproducible-builds.org/docs/system-images/), accessed
2026-10-06) gives a recipe for bit-reproducible ext4:

```sh
export SOURCE_DATE_EPOCH=1234
tar --sort=name --mtime=@$SOURCE_DATE_EPOCH --clamp-mtime \
    --pax-option=exthdr.name=%d/PaxHeaders/%f,delete=atime,delete=ctime \
    -cf /tmp/root.tar -C $rootdir .
mkfs.ext2 -F -d /tmp/root.tar \
    -E hash_seed=035cb65d-0a86-404a-bad7-19c88d05e400 \
    -U 12341234-a4ec-4304-a70f-c549ea829da9 \
    -L root $rootdev
```

Key elements:

- **`mkfs.ext4 -d <tarball>`**: populates the filesystem from a tarball,
  letting tar normalize metadata (ownership, order, timestamps). This
  requires e2fsprogs ≥ 1.47.1 with libarchive support
  ([commit 7e3a4f0](https://git.kernel.org/pub/scm/fs/ext2/e2fsprogs.git/commit/?id=7e3a4f0a33e9859af2bf44e8e1e278c8b10313cc),
  accessed 2026-10-06).
- **`-E hash_seed=<UUID>`**: fixes the directory hash seed so the ext4
  directory layout is deterministic. Without this, `mke2fs` generates a
  random seed each time
  ([mkfs.ext4(8)](https://man.archlinux.org/man/mkfs.ext4.8), accessed
  2026-10-06: "Intended for use with reproducible builds").
- **`-U <UUID>`**: fixes the filesystem UUID.
- **`-L <label>`**: fixes the volume label.
- **`SOURCE_DATE_EPOCH`**: e2fsprogs 1.47.1+ clamps filesystem timestamps
  to `SOURCE_DATE_EPOCH`
  ([commit b6e2913](https://git.kernel.org/pub/scm/fs/ext2/e2fsprogs.git/commit/?id=b6e2913061577ad981464e435026d71a48fd5caf),
  [commit f353a1f](https://git.kernel.org/pub/scm/fs/ext2/e2fsprogs.git/commit/?id=f353a1f),
  accessed 2026-10-06).

The current `build.sh` already uses `-U` (fixed UUID) and `-L` (fixed label)
but does **not** set `hash_seed` and does **not** use `-d` with a tarball
(it uses `-d` with a directory, which does not normalize metadata).

### 4.4 What cannot be made deterministic

- **Package postinst scripts.** dpkg postinst scripts may generate caches
  and indices in a non-deterministic manner. The reproducible-builds.org
  docs note fixes in `/etc/kernel/postinst.d/apt-auto-removal`,
  `/etc/shadow`, fontconfig cache, gdk-pixbuf `loaders.cache`, etc.
  ([source](https://reproducible-builds.org/docs/system-images/), accessed
  2026-10-06). For pluto's package set (ca-certificates, curl, dbus, git,
  iproute2, libpam-systemd, openssh-server, systemd, systemd-sysv, tmux,
  udev, unzip), the main risk is `openssh-server`'s `ssh-keygen -A` (host
  keys) and systemd's first-boot files. The Containerfile already removes
  host keys (`rm -f /etc/ssh/ssh_host_*`) and empties `machine-id`.
- **Go binary build.** `pluto-agent` and `pluto-vsock` are built with
  `CGO_ENABLED=0 go build`. Go builds are reproducible when the source,
  toolchain, and build flags are identical, but the Go version must be
  pinned (via `go.mod` or a toolchain pin). The `go.mod` declares
  `go 1.27`; the actual toolchain used depends on the build host.
- **Podman layer metadata.** `podman export` produces a tar with layer
  metadata. The `SOURCE_DATE_EPOCH` + deterministic tar recipe (§4.2)
  normalizes this, but podman's internal layer creation may introduce
  non-determinism (e.g., layer digests). Using `podman export` and then
  re-tarring with the deterministic recipe mitigates this.

---

## 5. Go-driven builder vs Containerfile + podman

### 5.1 Option A: Containerfile + podman (current, improved)

Keep the Containerfile as the rootfs definition, but make it deterministic:

```dockerfile
FROM ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55

# Pin apt to a dated snapshot
RUN cp /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list.d/ubuntu.sources.bak \
 && printf 'Types: deb\nURIs: http://snapshot.ubuntu.com/ubuntu\nSuites: noble noble-updates noble-backports\nComponents: main universe restricted multiverse\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\nSnapshot: 20261001T000000Z\n\nTypes: deb\nURIs: http://security.ubuntu.com/ubuntu\nSuites: noble-security\nComponents: main universe restricted multiverse\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\nSnapshot: 20261001T000000Z\n' \
      > /etc/apt/sources.list.d/ubuntu.sources

ARG SOURCE_DATE_EPOCH
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH}
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl dbus git iproute2 libpam-systemd \
      openssh-server systemd systemd-sysv tmux udev unzip \
 && rm -rf /var/lib/apt/lists/*
# ... rest of Containerfile unchanged ...
```

The Go builder then:
1. Reads the pins manifest.
2. Runs `podman build` with `--build-arg SOURCE_DATE_EPOCH=<epoch>`.
3. Runs `podman export` and re-tarres with deterministic tar.
4. Runs `mkfs.ext4 -d <tarball>` with fixed `hash_seed`, `-U`, `-L`.
5. Verifies all hashes and writes `manifest.json`.

**Pros.** Minimal change from current pipeline. Containerfile stays the
declarative rootfs definition. Podman handles the rootfs assembly.

**Cons.** The Containerfile still contains the apt snapshot date and package
list inline. The pins manifest and the Containerfile can drift. The
`SOURCE_DATE_EPOCH` propagation through `podman build` → `RUN` → `apt-get`
does not automatically make apt install packages "as of" that date (apt
ignores `SOURCE_DATE_EPOCH`; the snapshot pin does the real work).

### 5.2 Option B: Go-driven builder (recommended)

A single Go program (`cmd/pluto-image-builder` or a new `internal/imagebuilder`
package) owns the entire build:

1. **Parse pins manifest** (`images/pins.yaml`).
2. **Download kernel** from `kernel.url`, verify `kernel.sha256`.
3. **Download Firecracker** from `firecracker.url`, verify
   `firecracker.sha256`.
4. **Build Go helpers** (`pluto-agent`, `pluto-vsock`) with pinned Go
   toolchain.
5. **Build rootfs** via `podman build` with the Containerfile, passing
   `--build-arg SOURCE_DATE_EPOCH` and `--build-arg APT_SNAPSHOT`.
6. **Export and normalize**: `podman export` → deterministic re-tar →
   `mkfs.ext4 -d <tarball>` with fixed `hash_seed`, `-U`, `-L`.
7. **Write manifest** with all hashes, the `SOURCE_DATE_EPOCH` value, and
   the pins used. No wall-clock timestamp.

**Pros.** One program owns assembly, pinning, and the manifest. The pins
manifest is the single source of truth. The build is testable (unit tests
for manifest parsing, deterministic assembly; host-only test for full
build). No heredoc shell to reason about.

**Cons.** More code than the bash script. The Go builder must handle
podman invocation, tar normalization, and mkfs.ext4 flags correctly.

### 5.3 Recommendation

**Option B (Go-driven builder)**, with the Containerfile retained as the
rootfs definition but with the apt snapshot date and `SOURCE_DATE_EPOCH`
passed as build args from the pins manifest. This matches ticket #66's
decision: "A Go image builder replacing the bash pipeline, driven by a
single pins manifest."

The builder should be a new `internal/imagebuilder` package with a
`cmd/pluto-image-builder` entry point, or a `pluto image build` subcommand.
The existing `pluto image import` command in `internal/runner/runner.go`
(accessed 2026-10-06) already handles manifest verification and import;
the builder produces what import consumes.

---

## 6. Pins manifest design

### 6.1 Format

YAML, at `images/pins.yaml`:

```yaml
# pins.yaml — single source of truth for all image pins.
# Renovate drives updates via custom.regex managers (see renovate.json).
# All fields are pinned; nothing floats.

# renovate: datasource=docker depName=ubuntu
base:
  image: ubuntu:24.04
  digest: sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55

# Apt packages are pinned to a dated snapshot.ubuntu.com index.
# No Renovate datasource; update manually or via a custom datasource.
apt:
  snapshot: "20261001T000000Z"
  packages:
    - ca-certificates
    - curl
    - dbus
    - git
    - iproute2
    - libpam-systemd
    - openssh-server
    - systemd
    - systemd-sysv
    - tmux
    - udev
    - unzip

# renovate: datasource=github-releases depName=firecracker-microvm/firecracker
firecracker:
  version: 1.17.0
  url: https://github.com/firecracker-microvm/firecracker/releases/download/v1.17.0/firecracker-v1.17.0-x86_64.tgz
  sha256: <firecracker-tgz-sha256>

# Kernel: pinned to a specific Firecracker CI artifact.
# No Renovate datasource for S3; update manually or via a custom datasource.
kernel:
  url: https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/x86_64/vmlinux-6.18.51
  sha256: <kernel-sha256>

# Guest agent: built from pluto source. No external pin.
agent:
  source: "."
```

### 6.2 Renovate config

```json
{
  "customManagers": [
    {
      "customType": "regex",
      "description": "Update base image digest",
      "managerFilePatterns": ["/^images/pins\\.yaml$/"],
      "matchStrings": [
        "# renovate: datasource=docker depName=ubuntu\\nbase:\\n\\s+image:\\s*\\S+\\n\\s+digest:\\s*(?<currentDigest>sha256:[a-f0-9]+)"
      ],
      "datasourceTemplate": "docker",
      "depNameTemplate": "ubuntu"
    },
    {
      "customType": "regex",
      "description": "Update Firecracker version",
      "managerFilePatterns": ["/^images/pins\\.yaml$/"],
      "matchStrings": [
        "# renovate: datasource=github-releases depName=firecracker-microvm/firecracker\\nfirecracker:\\n\\s+version:\\s*(?<currentValue>\\S+)"
      ],
      "datasourceTemplate": "github-releases",
      "depNameTemplate": "firecracker-microvm/firecracker"
    }
  ]
}
```

**Note on RE2 limitations.** Renovate uses RE2, which does not support
backreferences or lookahead assertions
([source](https://docs.renovatebot.com/modules/manager/regex/), accessed
2026-10-06). The `matchStrings` above use `\n` and `\s` which RE2 supports.
The regex matches per-file, not per-line, so `^` and `$` match file
boundaries. The patterns above rely on the comment line immediately
preceding the block, which is fragile if the file is reformatted. A more
robust approach uses `matchStringsStrategy: "combination"` with separate
patterns for the comment and the value, but this adds complexity.

**Uncertainty.** The comment-anchored regex approach is fragile. If the YAML
is reformatted (e.g., by a different editor or a Renovate PR that changes
whitespace), the regex may break. A more robust design uses a structured
format like `image-versions.yml` (a list of objects with `image:`, `tag:`,
`digest:` fields) which the regex can match without relying on comment
placement. However, pluto's pins are heterogeneous (not all Docker images),
so a single structured format is awkward. The comment-anchored approach is
the most practical for the current pin set; mark as a maintenance risk.

### 6.3 Pins without Renovate datasources

Two pins have no natural Renovate datasource:

- **Apt snapshot date** (`apt.snapshot`): This is a date, not a version.
  Renovate has no datasource for "snapshot.ubuntu.com dates." Options:
  1. Manual update (accept the toil).
  2. Custom datasource that queries the snapshot service for available
     dates and picks the latest.
  3. A GitHub Actions cron job that opens a PR to bump the date.
- **Kernel URL+hash** (`kernel.url`, `kernel.sha256`): The kernel is on
  S3, not GitHub releases. Options:
  1. Manual update when Firecracker CI publishes a new kernel.
  2. Custom datasource that queries the S3 bucket listing.
  3. A GitHub Actions cron job that checks the S3 bucket and opens a PR.

**Recommendation.** For M3, accept manual updates for `apt.snapshot` and
`kernel` with a documented procedure. The Renovate custom datasource or
cron job approach can be added later when the manual toil justifies it.
This matches ticket #66's graduated automerge posture: "image pins stay
manual until the image-build CI gate is trusted."

---

## 7. Determinism recipe

### 7.1 What makes a rebuild byte-identical

Given the same pins manifest and the same source tree, a rebuild produces
byte-identical `rootfs.img`, `vmlinuz`, and `firecracker` if:

1. **Base image digest is pinned** (`base.digest`). ✅ Already done.
2. **Apt snapshot is pinned** (`apt.snapshot`). The Containerfile rewrites
   `ubuntu.sources` to use `snapshot.ubuntu.com` with the pinned date.
3. **Kernel URL and hash are pinned** (`kernel.url`, `kernel.sha256`). ✅
   Already done in `build.sh`.
4. **Firecracker URL and hash are pinned** (`firecracker.url`,
   `firecracker.sha256`). New: the builder verifies the tarball hash.
5. **`SOURCE_DATE_EPOCH` is set** to a fixed value (e.g., the git commit
   time of the source tree, or a fixed constant). All filesystem
   timestamps clamp to this value.
6. **Deterministic tar** normalizes file ownership, order, and timestamps
   in the rootfs tarball.
7. **`mkfs.ext4 -d <tarball>`** populates the ext4 from the normalized
   tarball, with fixed `hash_seed`, `-U` (UUID), and `-L` (label).
8. **No wall-clock in manifest.** The `manifest.json` records
   `source_date_epoch` instead of `built_at`. The content-derived version
   (hash of kernel + rootfs hashes) is unchanged.
9. **Go toolchain is pinned.** `pluto-agent` and `pluto-vsock` are built
   with the same Go version (from `go.mod` or a toolchain pin).

### 7.2 What cannot be made deterministic

1. **Package postinst scripts.** dpkg postinst scripts may generate
   caches non-deterministically. Mitigation: choose packages with
   reproducible postinst scripts, or normalize postinst output. For
   pluto's package set, the risk is low but non-zero.
2. **Podman internal layer digests.** `podman export` produces a tar that
   may include non-deterministic layer metadata. Mitigation: re-tar with
   the deterministic recipe (§4.2).
3. **Go binary reproducibility.** Go builds are reproducible when the
   source, toolchain, and flags are identical, but the toolchain must be
   pinned. The `go.mod` declares `go 1.27`; the actual toolchain depends
   on the build host. Mitigation: pin the Go toolchain in the builder
   (e.g., `GOTOOLCHAIN=go1.27.0` or a containerized build).
4. **Apt snapshot drift.** The snapshot service may remove old snapshots
   (currently guaranteed for 2 years). If a snapshot is removed, the build
   fails. Mitigation: pin the snapshot date and monitor snapshot
   availability.

### 7.3 Determinism bar

**Tier 1 (byte-identical):** `vmlinuz`, `firecracker`, and `rootfs.img`
are byte-identical across rebuilds from the same pins manifest and source
tree. This is the goal.

**Tier 2 (explained diff):** If a rebuild produces different bytes, the
diff is explained by a change in the pins manifest (base digest, apt
snapshot, kernel hash, Firecracker hash) or the source tree. The builder
fails if the pins manifest or source tree is unchanged but the output
differs.

**Tier 3 (documented residual):** Any residual non-determinism (e.g., from
package postinst scripts) is documented and minimized. The builder warns
if a rebuild produces unexpected diffs.

---

## 8. Recommended builder design

### 8.1 Architecture

```
images/pins.yaml  ──→  internal/imagebuilder  ──→  images/out/
                           │
                           ├── parse pins manifest
                           ├── download + verify kernel
                           ├── download + verify firecracker
                           ├── build Go helpers (pluto-agent, pluto-vsock)
                           ├── podman build (Containerfile + build args)
                           ├── podman export → deterministic tar
                           ├── mkfs.ext4 -d <tarball> (fixed hash_seed, -U, -L)
                           └── write manifest.json (no wall-clock)
```

### 8.2 Manifest schema (builder output)

The builder writes `manifest.json` with the same schema as the current
`build.sh` but with `source_date_epoch` instead of `built_at`:

```json
{
  "schema": 2,
  "source_date_epoch": 1759276800,
  "kernel": { "url": "...", "sha256": "..." },
  "firecracker": { "version": "1.17.0", "url": "...", "sha256": "..." },
  "rootfs": {
    "file": "rootfs.img",
    "base": "ubuntu:24.04@sha256:...",
    "apt_snapshot": "20261001T000000Z",
    "sha256": "...",
    "size_bytes": ...,
    "disk_mb": 2048
  },
  "agent": { "sha256": "..." }
}
```

The `internal/runner/runner.go` `imageManifest` struct
(accessed 2026-10-06) would be updated to match. The `version()` method
(content-derived hash of kernel + rootfs hashes) is unchanged.

### 8.3 Testing

- **Unit tests** for pins manifest parsing, hash verification, and
  deterministic tar/mkfs flag construction.
- **Host-only test** that builds the artifact and verifies it imports
  (via `pluto image import`).
- **Double-build test** that builds twice and asserts byte-identical
  output (Tier 1 determinism bar).

---

## 9. Sources

- Firecracker `resources/rebuild.sh`:
  https://github.com/firecracker-microvm/firecracker/blob/main/resources/rebuild.sh
  (accessed 2026-10-06)
- Firecracker `resources/rootfs/setup-ubuntu-ci.sh`:
  https://github.com/firecracker-microvm/firecracker/blob/main/resources/rootfs/setup-ubuntu-ci.sh
  (accessed 2026-10-06)
- Firecracker `tools/devtool`:
  https://github.com/firecracker-microvm/firecracker/blob/main/tools/devtool
  (accessed 2026-10-06)
- Firecracker kernel policy:
  https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md
  (accessed 2026-10-06)
- Firecracker rootfs and kernel setup:
  https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md
  (accessed 2026-10-06)
- Ubuntu snapshot service:
  https://snapshot.ubuntu.com/ (accessed 2026-10-06)
- Ubuntu snapshot service docs:
  https://ubuntu.com/server/docs/how-to/software/snapshot-service
  (accessed 2026-10-06)
- Reproducible Builds — system images:
  https://reproducible-builds.org/docs/system-images/ (accessed 2026-10-06)
- Reproducible Builds — SOURCE_DATE_EPOCH:
  https://reproducible-builds.org/docs/source-date-epoch/ (accessed 2026-10-06)
- GNU tar reproducibility:
  https://www.gnu.org/software/tar/manual/html_node/Reproducibility.html
  (accessed 2026-10-06)
- mkfs.ext4(8) man page:
  https://man.archlinux.org/man/mkfs.ext4.8 (accessed 2026-10-06)
- e2fsprogs SOURCE_DATE_EPOCH support (commit b6e2913):
  https://git.kernel.org/pub/scm/fs/ext2/e2fsprogs.git/commit/?id=b6e2913061577ad981464e435026d71a48fd5caf
  (accessed 2026-10-06)
- e2fsprogs timestamp clamping (commit f353a1f):
  https://git.kernel.org/pub/scm/fs/ext2/e2fsprogs.git/commit/?id=f353a1f
  (accessed 2026-10-06)
- e2fsprogs tarball input for -d (commit 7e3a4f0):
  https://git.kernel.org/pub/scm/fs/ext2/e2fsprogs.git/commit/?id=7e3a4f0a33e9859af2bf44e8e1e278c8b10313cc
  (accessed 2026-10-06)
- Renovate regex manager docs:
  https://docs.renovatebot.com/modules/manager/regex/ (accessed 2026-10-06)
- Renovate customManagers config:
  https://docs.renovatebot.com/configuration-options/ (accessed 2026-10-06)
- projectbluefin org Renovate config:
  https://github.com/projectbluefin/.github/blob/main/org-inherited-config.json
  (accessed 2026-10-06)
- ublue-os renovate-sandbox (image-versions.yml + renovate.json5):
  https://github.com/ublue-os/renovate-sandbox/blob/main/renovate.json5
  (accessed 2026-10-06)
- projectbluefin THEPATTERN.md:
  https://github.com/projectbluefin/bluefin/blob/main/THEPATTERN.md
  (accessed 2026-10-06)
- Flatcar scripts repo:
  https://github.com/flatcar/scripts (accessed 2026-10-06)
- Flatcar release guide:
  https://www.flatcar.org/docs/latest/devguide/release-guide
  (accessed 2026-10-06)
- Kata Containers osbuilder README:
  https://github.com/kata-containers/kata-containers/blob/main/tools/osbuilder/README.md
  (accessed 2026-10-06)
- Linux kernel reproducible builds:
  https://kernel.org/doc/html/latest/kbuild/reproducible-builds.html
  (accessed 2026-10-06)
