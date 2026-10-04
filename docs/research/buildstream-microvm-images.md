# BuildStream to microVM images

Research for pluto ticket [#7](https://github.com/Siddhj2206/pluto/issues/7):
how the Bluefin / freedesktop-sdk BuildStream ecosystem produces artifacts, what
a minimal pluto box shape should contain, which BuildStream 2 mechanics matter
for building and distributing it, what microVM kernels need, and what it costs.
Written 2026-10-04. Audience: the image-pipeline and M0 decisions.

## Conclusions up front

1. **pluto does not need dakota, gnome-build-meta, bootc, or an OCI registry.**
   freedesktop-sdk 26.08 ships a maintained minimal-VM stack
   (`elements/vm/minimal/*`) whose kernel is explicitly "configured for use in
   virtual machines" and whose config already enables virtio, vsock, 9p,
   virtiofs, erofs and the systemd baseline. pluto can junction FSDK, add
   `openssh` + `git` + its agent, and assemble a direct-kernel-boot bundle.
2. **The smallest useful pluto shape is: kernel (`vmlinuz`) + dracut initramfs +
   a raw rootfs disk image + a cmdline contract**, content-addressed and stored
   beside disk state. QEMU direct boot (`-kernel/-initrd/-append`) needs no
   firmware, no UKI, no bootloader, no sysupdate.
3. **First shape: `base` (headless server).** Systemd, sshd, git, the pluto
   agent. The container shape is the same stack without the kernel; the desktop
   shape is not needed at all.
4. **Junction the whole FSDK, reference its elements, and override nothing.**
   FSDK elements keep FSDK's cache keys and can pull from public read-only
   artifact caches; only pluto's own assembly/config elements re-key and build.
   Copying FSDK elements into pluto (as dakota does for its kernel) forces
   source builds — FSDK's kernel compiles Rust, so that cost is real.
5. **Builders use a remote CAS; hosts do not need BuildStream.** The documented
   self-hostable cache server is Buildbarn (`bb-storage` + `bb-remote-asset`);
   `buildbox-casd` is the client-side local CAS manager, not a server. For
   distribution to hosts, raw content-addressed files in pluto's existing
   self-hosted S3 are the cheapest M0 path; a registry or sysupdate are later
   options, not prerequisites.
6. **Kernel costs are the open risk.** FSDK's public caches may already hold the
   kernel/systemd artifacts; if they do not, a cold build compiles Linux (and
   its Rust toolchain deps) locally. Verify with a pull/checkout before
   committing to a full build.

## Method

Primary sources read directly, at these states:

| Source | State | Where |
| --- | --- | --- |
| dakota | local clone `3d549f4` (2026-09-19), upstream `projectbluefin/dakota` | `/var/home/sid/Projects/dakota` |
| projectbluefin/server | local clone `5ebfeae` (2026-09-14) | `/var/home/sid/Projects/server` |
| fsdk-containers | local clone `c2f584a` (2026-09-18) | `/var/home/sid/Projects/fsdk-containers` |
| freedesktop-sdk | pinned ref `freedesktop-sdk-26.08.0-0-gdb97cce32cecadc7a3e98f06d557ebfa6ba9ad46` (the ref server/fsdk-containers pin) | GitLab raw/API |
| gnome-build-meta | branch `gnome-51` | gitlab.gnome.org |
| dakota-kernel-ubuntu | branch `development` (pushed 2026-10-04) | GitHub |
| BuildStream docs / source | master docs (2.8 line) | docs.buildstream.build |
| QEMU, Firecracker, systemd | current master/main docs | upstream |

Prior frameless notes were used as maps and are cited as such, not as
authority: `frameless/docs/research/{02-buildstream,03-projectbluefin-buildstream,07-dakota-alpha-6,12-core-versus-desktop,13-owning-the-boot-spine}.md`.
Nothing was modified outside the pluto worktree; no builds were run.

---

## 1. How the Bluefin ecosystem produces artifacts

### 1.1 dakota — bootc OCI desktop image

`dakota` is a BuildStream 2 project on FSDK + gnome-build-meta junctions that
publishes four bootc OCI variants. Its artifact pipeline is the canonical
Bluefin OCI shape:

- `elements/oci/layers/bluefin-stack.bst` (`kind: stack`) aggregates the OS;
  `elements/oci/layers/bluefin.bst` is a `kind: compose` that excludes
  `devel`/`debug`/`static-blocklist`.
- `elements/oci/bluefin.bst` (`kind: script`) stages the layer at `/layer`, runs
  `prepare-image.sh`, `systemd-sysusers`, `glib-compile-schemas`, `ldconfig -r`,
  then calls the FSDK `build-oci` tool with a YAML heredoc that sets labels
  including `'containers.bootc': '1'` and the index annotation
  `'org.opencontainers.image.ref.name': 'ghcr.io/projectbluefin/dakota:latest'`
  (`elements/oci/bluefin.bst`). The tool is
  `freedesktop-sdk.bst:components/oci-builder.bst`, a `pyproject` element built
  from FSDK's `files/oci/` Python package (`oci_builder/cmd.py` reads
  `images:`/`annotations` YAML from stdin and writes an OCI layout; see §3.3).
- Kernel: `elements/core/linux-fdsdk.bst` reproduces FSDK 26.08's
  `components/linux.bst` recipe with one downstream delta — `CRYPTO_ZSTD` forced
  `=y` for zswap — and `core/linux-ogc.bst` is the gaming variant. The kernel
  configuration helpers are vendored in `files/linux/` (`fdsdk-config.sh`,
  `config-utils.sh`) and applied on top of `make defconfig` + `olddefconfig`.
- The initramfs and bootc come from gnome-build-meta
  (`gnomeos-deps/bootc.bst`, `oci/initramfs/*`), per frameless research note 13.

dakota's output is an OCI image consumed by `bootc`, not a bootable microVM
artifact. Converting it would mean `bootc install to-disk` or a rootfs-to-disk
assembly. Its relevance to pluto is the OCI assembly pattern and the kernel
override mechanics, not the product.

CI cost data is in dakota's own docs: `docs/build.md` requires "~100 GB disk,
~16 GB RAM" and states "warm cache: 2–5 min; cold: 60–90 min" for `just build`,
plus `just validate` ~5 min. `docs/ci.md` names remote execution through
`cache.projectbluefin.io:11002`; `.github/actions/generate-bst-ci-config/action.yml`
writes the mTLS config for `artifacts`, `source-caches`, `storage-service`,
`remote-execution`, and `action-cache-service` all pointing at that endpoint,
with read-only mirrors `gbm.gnome.org:11003` and
`cache.freedesktop-sdk.io:11001`. Fail-closed semantics are explicit in
`build.yml` ("asserts the remote-execution banner appeared", per frameless 03).

### 1.2 projectbluefin/server — DDI, UKI, installer, sysext

server is the closest thing in the ecosystem to a non-bootc OS, and it produces
several artifact types a microVM could use:

| Output | Element | Mechanism |
| --- | --- | --- |
| XFS DDI (`bluefin-server-ddi-<ver>.raw.zst`) | `elements/oci/bluefin-server-ddi.bst` (`kind: script`) | strip debug, `depmod`, pre-size a file with `truncate`, `mkfs.xfs -f -L bluefin-root`, `zstd -19`, `SHA256SUMS`. The header says "The output .raw is a filesystem image consumed by installer repart config". |
| Target UKI (`bluefin-server-<ver>.efi`) | inside `elements/oci/bluefin-server-installer.bst` | `dracut --no-hostonly --add-drivers "virtio virtio_blk virtio_pci virtio_scsi nvme nvme_core xfs erofs overlay zfs spl" --filesystems "xfs vfat zfs"` then `ukify build --linux --initrd --cmdline "rw console=ttyS0,115200 console=tty0 quiet loglevel=3 audit=0"`. The installer UKI is unsigned ("acceptable for USB installer media", per the element comment). |
| Installer disk | same element | cpio initrd (`find /layer \| cpio --format=newc \| gzip`), installer UKI, `systemd-repart --empty=create` with `CopyBlocks=` to embed the DDI, `zstd`, `SHA256SUMS`. |
| EROFS sysext (`k0s-<ver>.raw[.zst]`) | `elements/oci/k0s-sysext.bst` (`kind: manual`) | `mkfs.erofs -d0`, `zstd -19`. |
| PXE kernel + initrd | installer element | copies `vmlinuz` and `/installer.cpio.gz` next to the UKI. |

Distribution is not a registry: `files/os/sysupdate.d/50-root.transfer` pulls
`bluefin-server-ddi-@v.raw.zst` from
`https://github.com/projectbluefin/server/releases/latest/download/` into a
`systemd-repart` partition (`Type=partition`), and `60-uki.transfer` pulls
`bluefin-server-@v.efi` into `/efi/EFI/Linux`. CI (`.github/workflows/build.yml`)
uploads raw assets and a GPG-signed `SHA256SUMS` to GitHub Releases; the build
job's `timeout-minutes` is 180.

Two details matter for pluto: server boots its guest with `console=ttyS0,115200`
and a dracut initramfs that explicitly adds the virtio drivers; and
`bluefin-server-installer.bst` proves a plain cpio rootfs + `/init -> systemd` is a working
initrd for systemd (comment: "systemd PID 1 works correctly when the initrd is
a plain rootfs cpio with /init -> systemd. This is the approach the systemd
project uses for its own test images"). server uses a Flatcar LTS kernel
(`elements/flatcar/flatcar-kernel.bst`) with ZFS, not FSDK's.

### 1.3 fsdk-containers — OCI images plus a full-OS QEMU guest disk

fsdk-containers carves distroless OCI images out of raw FSDK components, but its
`elements/podman-vm/` lane is the direct proof that FSDK's VM stack composes
into a bootable disk:

- `podman-vm-deps.bst` (`kind: stack`) = `freedesktop-sdk.bst:vm/minimal/deps.bst`
  + `freedesktop-sdk.bst:components/git.bst` + a Go worker + config. The
  comment says FSDK's VM stack "supplies the systemd/init, kernel, initramfs,
  network/DNS, certificates, and base filesystem contract".
- `podman-vm-filesystem.bst` (`kind: compose`) excludes
  `debug/devel/doc/tests/shells`.
- `podman-vm-efi.bst` (`kind: script`) stages the filesystem at `/sysroot` and
  FSDK's `vm/boot/efi.bst` at `/sysroot/efi`, re-aligns the baked `root=UUID=`
  in the UKI's `.cmdline` (FSDK 26.08) or loader entries (25.08), then runs the
  genimage-based assembler (`vm/deploy-tools.bst` + `vm/prepare-image.bst`) to
  emit a raw GPT disk; the artifact is `donate-clanker-vm-<ver>-<arch>.raw`,
  "which QEMU boots directly". Note the comment that FSDK 26.08 removed the
  shell from `runtime-minimal`, so script elements must declare
  `public-stacks/runtime-gnu.bst` explicitly.

This is a maintained downstream precedent for exactly the assembly pluto wants,
minus pluto's sshd/git/agent payload and with a UKI instead of direct kernel
boot.

### 1.4 gnome-build-meta — UKIs, erofs, sysupdate

GBM is no longer only an OCI/ostree producer. On `gnome-51`:

- `elements/gnomeos/signed-boot.bst` builds UKIs with `ukify` from FSDK's
  `components/linux.bst`, GBM's initramfs image and usr image, with a cmdline
  including `mount.usrflags=ro`, `mount.usrfstype=erofs`,
  `systemd.verity_usr_options=panic-on-corruption`, `rootflags=...` (it reads
  the verity roothash from `mini.repart.json`).
- `elements/gnomeos/sysupdate-config.bst` installs sysupdate configs;
  `elements/gnomeos/update-images.bst` and friends assemble the release asset
  set and `SHA256SUMS`.
- `elements/oci/initramfs/{deps,filesystem,image}.bst` is the OCI-layout
  initramfs (cpio + zstd, microcode prepended on x86_64), per frameless 13.

For pluto, GBM is only a reference: UKI + erofs + sysupdate is a coherent design
for *updatable* images, but pluto's boxes are provisioned and replaced, not
self-updated, and direct kernel boot does not need a UKI. GBM's initramfs and
signed-boot elements show how to feed an erofs `/usr` into a verity-protected
boot; none of that is M0.

### 1.5 dakota-kernel-ubuntu — kernel as a reusable OCI artifact

`github.com/projectbluefin/dakota-kernel-ubuntu` (default branch `development`)
is a BuildStream 2 producer that publishes **kernel-only OCI images** to
`ghcr.io/projectbluefin/dakota-kernel-ubuntu` for Dakota. From its README:

- x86_64 only; Ubuntu 26.10 source tag `Ubuntu-7.3.0-8.8`; release-rc kernel
  `7.3.0-rc5-8-generic-dakota`.
- Config starts from Ubuntu's generic annotations (`debian.master/config/annotations`)
  then applies Dakota's helper fragments from a pinned dakota git source; it
  requires `CONFIG_RUST=y` and built-in `CONFIG_CRYPTO_ZSTD=y`; SELinux support
  is compiled but not in the default LSM list.
- Output is "a scratch filesystem artifact, not a runnable container or
  bootable OS": `/usr/lib/modules/<release>/{vmlinuz,config,System.map,vmlinux,
  kernel/...}`, `/usr/src/linux-<release>/` headers, `build` symlink. Modules
  are unsigned.
- Consumers pull it with the `docker` source as `kind: import`, pinned by the
  bare 64-hex digest; immutable tags are `sha-<producer commit>`, and the
  rolling `latest`/`ubuntu-26.10` tags are updated only by the default branch.
  `cosign verify` is documented; the BST docker source only validates content
  digests, not signatures.
- Builds run on GitHub Actions with BuildStream's cache restored/saved; "cold
  builds use the SDK's upstream artifact caches before compiling missing
  elements"; OCI-only changes do not recompile the kernel. There is no boot
  test: "A successful build/publication is not a boot test."

Relevance: this is a working pattern for distributing a kernel alone, and a
fallback kernel source if FSDK's kernel ever fails pluto. It is not needed for
M0 because FSDK's kernel already covers the microVM requirements (§4).

### 1.6 Which outputs matter for a directly-booted microVM

| Output | Producer | Use in a microVM | Verdict for M0 |
| --- | --- | --- | --- |
| `vmlinuz` + `/usr/lib/modules/<release>/` | FSDK `components/linux.bst`; GBM; dakota; dakota-kernel-ubuntu | the guest kernel and its modules | **Yes — direct boot input** |
| dracut initramfs (cpio) | FSDK `vm/boot/{efi,virt}.bst`; server (cpio-native) | needed because virtio-blk and most filesystems are modules in FSDK's config | **Yes — generated by a pluto script** |
| raw rootfs disk (ext4/xfs) | server DDI (`mkfs.xfs`); FSDK `vm/minimal/efi.bst` genimage (`mkfs.ext4`) | the box's persistent disk | **Yes — the box state substrate** |
| OCI image | dakota, fsdk-containers | container shape; a host for rootfs content, not bootable without conversion | No (M0), later for a `container` shape |
| UKI (`.efi`) | server, GBM | kernel+initrd+cmdline as one PE file for UEFI | Optional; not needed for `-kernel` boot |
| erofs image | server sysext; GBM `/usr` | read-only verity-protected filesystem | No; only if image-verity updates become a goal |
| XFS DDI + sysupdate transfers | server | atomic OS payload update over HTTP | No; pluto reprovisions instead of self-updating |
| kernel-only OCI | dakota-kernel-ubuntu | digest-pinned kernel distribution | Fallback only |

---

## 2. The minimal pluto shape

### 2.1 What a direct-boot microVM actually needs

QEMU's "Direct Linux Boot" documents the contract: `-kernel bzImage`,
`-drive file=rootdisk.img,format=raw`, `-append "root=/dev/sda"`, optional
`-initrd`, `-nographic` with `console=ttyS0` (QEMU docs). Firecracker takes the
same conceptual inputs through its API: VirtIO block and net devices backed by
host files and TAPs, a VirtIO vsock device, a serial console, and a guest kernel;
"the backing files need to be pre-formatted with a filesystem that the guest
kernel supports" (Firecracker `docs/design.md`). Neither requires UEFI, a
bootloader, a UKI, or a discovery protocol.

So the boot contract pluto must own is:

```
vmlinuz            # kernel image
initramfs.gz       # dracut output with virtio/storage modules
rootfs.img         # raw, pre-formatted ext4 (or xfs) filesystem
cmdline            # "root=/dev/vda rw console=ttyS0 ..."
```

per box (or per image hash), plus a manifest that records the four content
hashes and the kernel release string. Everything else — sshd, git, the agent —
is userspace inside `rootfs.img`.

### 2.2 FSDK already ships the spine

FSDK 26.08 at the pinned ref has `elements/vm/`:

- `vm/minimal/deps.bst` (`kind: stack`): `runtime-gnu`, `bash-completion`,
  `bash-config`, `dbus`, `dracut`, `hosts`, `iproute2`, `linux`, `os-release`,
  `shadow`, `systemd`, `tzdata`, `usbutils`, `util-linux-full`,
  `wireless-regdb-bin`, `vm/base-filesystem.bst`, and the `vm/config/*`
  journald/networkd/resolved/firstboot/useradd elements.
- `vm/minimal/filesystem.bst` (`kind: compose`): the same stack, excluding
  `debug`, `devel`, `doc`, `tests`, `shells` (and `locale` unless requested).
- `vm/boot/virt.bst` (`kind: script`): produces **`/boot/vmlinuz` and
  `/boot/initramfs.gz`** — `dracut --reproducible --fstab --no-machineid
  --kernel-image ... --kver ... --add virtfs --gzip` — for a VM whose root is
  `9p`/`virtfs`.
- `vm/boot/efi.bst` (`kind: script`): builds a dracut UKI (`dracut --uefi`)
  with `--kernel-cmdline "rw console=tty0 console=ttyS0 quiet root=UUID=..."`.
- `vm/minimal/efi.bst`: genimage assembly of a GPT disk with an EFI partition
  and an **ext4 root** (`image root.img { ext4 { label = "root" ... } }`),
  using `vm/deploy-tools.bst` + `vm/prepare-image.bst`.
- `vm/minimal-secure/*`: the sysupdate + signed UKI + verity path (analogous to
  server/GBM), for when updates matter.

`components/linux.bst` is described as "Linux kernel configured for use in
virtual machines"; its config script (`files/linux/fdsdk-config.sh`) is the
evidence for the symbol set in §4.2.

The pluto `base` shape is therefore **not a from-scratch OS build**: it is a
small number of new elements around FSDK's VM stack.

### 2.3 Proposed base shape

```
elements/freedesktop-sdk.bst        # junction, pinned to an FSDK 26.08 point release
elements/pluto/deps.bst             # kind: stack
elements/pluto/rootfs.bst           # kind: compose   (the box filesystem)
elements/pluto/vmlinuz.bst          # kind: script    (kernel + initramfs, adapted from vm/boot/virt.bst)
elements/pluto/disk.bst             # kind: script    (ext4 raw image, genimage/mkfs)
elements/pluto/export.bst           # kind: script    (bundle + manifest hashes)
```

`deps.bst` depends on:

- `freedesktop-sdk.bst:vm/minimal/deps.bst` — the spine (systemd, kernel,
  networkd/resolved, dracut, base filesystem);
- `freedesktop-sdk.bst:components/openssh-systemd.bst` — sshd with socket
  activation (used by server's `os-stack.bst`);
- `freedesktop-sdk.bst:components/git.bst` — git (used by fsdk-containers'
  guest);
- pluto's agent element (a static binary or a small runtime) and its systemd
  units/config.

`export.bst` is the target that emits the bundle: `vmlinuz`, `initramfs.gz`,
`rootfs.img`, and a JSON manifest. It can use FSDK's `vm/deploy-tools.bst`
(genimage, e2fsprogs, mtools, dosfstools) exactly as `vm/minimal/efi.bst` and
fsdk-containers' `podman-vm-efi.bst` do; pluto omits the EFI partition when
booting with `-kernel`.

Adaptation from `vm/boot/virt.bst`: change the root contract from
`--rootfstype 9p --rootsource virtfs` to an ext4 root on a virtio disk
(`root=LABEL=pluto-root` or a per-box `root=UUID=...`), keep dracut, and keep
the `console=ttyS0` cmdline. One scripting element, well under a hundred lines.

### 2.4 What a "shape" contains, and which to build first

Borrowing frameless's vocabulary (and matching the map's "guest environment:
base shape contents"): a **shape** is a *spine* (kernel, modules, initramfs,
rootfs assembly, cmdline), a *payload* (services inside the rootfs: systemd,
sshd, git, the pluto agent), and a *target* (the element that assembles the
bundle). pluto's shapes:

| Shape | Spine | Payload | Target |
| --- | --- | --- | --- |
| `base` (headless) — **build first** | kernel + initramfs + raw ext4 root | systemd, sshd, git, agent | `pluto/export.bst` |
| `container` | none | the same userspace stack | OCI assembly with FSDK `build-oci` |
| `*` project-specific | `base` | per-project packages | derived rootfs |

`base` first because it is the smallest thing that can boot, it is a
prerequisite for every other shape, and it exercises the whole pipeline
(kernel pull, dracut, disk assembly, QEMU boot, vsock SSH) end to end. There is
no desktop shape on pluto's map, and no updater requirement: boxes are
provisioned from an image and replaced, so bootc/sysupdate are explicitly not
part of M0.

### 2.5 How much FSDK to junction

Junction the whole FSDK project once, pinned to a 26.08 point release, and
reference elements. Reasons, all evidence-backed:

- **Keys and caches.** Strong cache keys are computed inside the *owning
  project*; a junction reference to an FSDK element resolves under FSDK's
  project configuration and can hit FSDK-built artifacts. A copied element
  re-keys under pluto's project and must be built (frameless 13 documents this
  for bootc: 416 crates rebuilt; the same applies to FSDK's kernel, which
  build-depends on `components/rust.bst`, `rust-bindgen`, `rust-src`, `pahole`).
- **Public read-only caches.** server and fsdk-containers recommend
  `gbm.gnome.org:11003` and `cache.projectbluefin.io:11001` for both artifacts
  and sources; dakota's CI config adds `cache.freedesktop-sdk.io:11001`. A cold
  pluto checkout can pull instead of build, subject to §6 question 1.
- **No gnome-build-meta.** GBM's unique value to frameless/dakota is bootc +
  the initramfs + the signed module cert. pluto needs none of those: it uses
  FSDK's kernel directly and generates its own initramfs with FSDK's dracut.
  Excluding GBM removes the biggest external junction and its override mirror
  (the long-term tax frameless 13 names).
- **No overrides.** Overriding FSDK elements (as dakota and frameless do)
  changes keys and forces rebuilds. pluto's first build should override
  nothing. The one strict dependency to be aware of: FSDK's kernel
  build-depends on `components/linux-module-cert.bst` (strictly); FSDK ships it
  deliberately empty and documents it as downstream-overridable. pluto, with no
  Secure Boot, should simply not override it.

How much of FSDK is *used*: the `deps.bst` closure is roughly the `vm/minimal`
stack (a few dozen elements) plus sshd/git/agent. How much is *authored*:
zero FSDK elements; only pluto's ~6 assembly/config elements.

### 2.6 First-host plan

1. `bst show --deps all pluto/export.bst` to load the graph (no builds).
2. Pre-pull the risky artifacts (`components/linux.bst`, `components/systemd.bst`,
   `vm/minimal/deps.bst`) with `bst artifact pull` / `bst artifact checkout` to
   see whether the public caches serve them at the pinned ref.
3. If they pull, `bst build pluto/export.bst` should mostly compose and script;
   if not, budget a local kernel build (the `components/rust.bst` closure
   included) or switch the kernel to dakota-kernel-ubuntu's OCI.
4. Boot the bundle under QEMU with `-kernel`, `-initrd`, `-drive if=virtio`,
   `-device vhost-vsock-pci`, `-nographic`; assert systemd reaches a
   multi-user target and sshd answers over vsock (§4.4).
5. Export the bundle + manifest to the existing S3 bucket by content hash; a host
   fetches by hash (see §3.4). The box's disk derives from `rootfs.img`; the
   image itself is never mutated.

---

## 3. BuildStream 2 mechanics that matter

### 3.1 Junctions and cache keys

- A junction element is a window into another project; dependencies cross it as
  `name.bst:path/element.bst`. Junction elements produce no artifacts
  (BuildStream docs; frameless 02).
- Cache keys are SHA256 over a JSON document containing environment, element
  configuration, sources, dependencies, and public data; the **strong** key
  includes build dependencies' strong keys, so a dependency change re-keys
  reverse dependencies. Non-strict plans use weak keys (docs `arch_cachekeys.html`).
- The owning project's `fatal-warnings` and resolved environment are part of the
  key (frameless 13, citing BuildStream 2.8 `element.py`/`_project.py`). This is
  why "copy the element" is not "reuse the artifact".
- Consequently: FSDK elements referenced through the junction keep FSDK keys;
  pluto's compose/script elements get pluto keys and rebuild on every relevant
  change; pluto's `project.conf` should keep `environment` and
  `fatal-warnings` minimal and deliberate because they enter those keys.

### 3.2 Remote artifact cache / CAS on a personal host

- BuildStream exchanges data over the **REAPI ContentAddressableStorage**
  protocol plus the **remote asset** protocol for symbolic names; any conforming
  server works (docs `using_configuring_cache_server.html`).
- A cache deployment splits into **index** and **storage** roles; the docs name
  **Buildbarn** (`bb-storage` + `bb-remote-asset`) as the tested open-source
  implementation and publish a Docker Compose example that spins up an
  unauthenticated cache on `localhost:7981` (index/assets) and `localhost:7982`
  (storage), with the matching BuildStream config snippet (docs, same page).
  Authentication is documented at the Buildbarn project.
- `buildbox-casd` is the **client-side** component: BuildStream depends on
  "`buildbox-casd` (to manage local and remote content-addressed storage)",
  `buildbox-fuse` (check out content from the local CAS) and
  `buildbox-run-bubblewrap` (run elements in a sandbox); static binaries are
  available, and running bst in a container needs `--privileged` for the nested
  sandbox (docs `main_install.html`). There is no documented mode in which casd
  serves other clients; the docs point to Buildbarn for the server.
- BuildStream itself does not ship a cache server; `bst artifact push/pull`
  are client commands (docs `using_commands.html`; frameless 02).
- Practical M0: a **local-only cache** is enough to iterate (bst's local CAS via
  casd). When builds become worth sharing (second builder, CI), run the
  documented Buildbarn compose on the host or on a tailnet peer. With pluto's
  "no trust model", the unauthenticated compose on a tailnet-only interface is
  acceptable for M0; TLS/auth exists if wanted.

### 3.3 OCI export

FSDK's `build-oci` is a small Python package (`elements/components/oci-builder.bst`
is a `pyproject` element sourcing `files/oci/`). `oci_builder/cmd.py` reads YAML
from stdin: optional `compression`/`compression-level`, a list of `images` with
parents/layers/config, and index `annotations`; `image_builder.py` writes the
OCI layout. dakota's script wraps it with a heredoc and adds labels
(`containers.bootc`, `org.opencontainers.image.*`) and the index annotation
`org.opencontainers.image.ref.name`; fsdk-containers does the same per catalog
record. The community `oci` element exists as an alternative but is not what the
Bluefin repos use (frameless 02 §7).

For pluto, OCI export is only needed for the future `container` shape. The same
`kind: script` + tool-call pattern is what the bundle export uses — only the
output format differs.

### 3.4 How a new host gets an image

| Option | Server needed | Host command | Evidence | Fit for pluto |
| --- | --- | --- | --- | --- |
| **Content-addressed files in pluto's S3** | the bucket already planned for disk state | host agent downloads by manifest hash | this is the same infrastructure as disk state (map #1); server's raw-asset release model shows the shape | **Recommended M0**: no new daemon beyond the bucket endpoint, no format conversion, hashes give integrity |
| BuildStream remote artifact cache | Buildbarn (index+storage) + bst on the host | `bst artifact pull/checkout` | BuildStream docs; dakota/fsdk CI practice | Wrong tool for hosts: drags BuildStream and a cache config onto every host. Right tool for *builders* |
| Self-hosted OCI registry | `distribution` registry or zot, rootless podman | `skopeo`/`bootc switch`/`podman` | standard registry docs; dakota publishes to GHCR; bootc consumes OCI | Only worth it if bootc-style update semantics are wanted; adds a daemon and a conversion step |
| systemd-sysupdate over HTTP | static file server/releases | `systemd-sysupdate` with `url-file` transfers | server's `files/os/sysupdate.d/{50-root,60-uki}.transfer` | Overkill for M0; the model for fleet OS updates later |

The M0 answer, then: `bst build` on the builder → `export.bst` checks out the
bundle → upload `{vmlinuz, initramfs.gz, rootfs.img, manifest.json}` to S3 under
a content hash → a host (the same machine first) fetches and boots. No registry,
no sysupdate, no BuildStream on the host. If a second host cannot reach the S3
endpoint directly, the same bundle can ride a tailnet HTTP file server.

---

## 4. Kernel requirements for microVMs

### 4.1 Direct boot

QEMU (docs "Direct Linux Boot"):

```
qemu-system-x86_64 -kernel bzImage -initrd initrd \
  -drive file=rootdisk.img,format=raw \
  -append "root=/dev/sda console=ttyS0" -nographic
```

Firecracker (design doc): one microVM per process; VirtIO **net**, **block** and
**vsock** devices; serial console; block backends are host files that "need to be
pre-formatted with a filesystem that the guest kernel supports"; guests also see
PIC/IOAPIC/PIT. Firecracker's kernel support policy validates host/guest kernels
starting at v5.10, recommending v6.18 for current releases (guest v6.18 from
Firecracker v1.16.1); "other versions and other kernel configs might work, they
are not periodically validated". FSDK 26.08 ships Linux **v7.2.2**
(`elements/include/linux.yml`), newer than the validated set — a portability
question to test, not a blocker (firecracker is not chosen yet).

### 4.2 Required config, and what FSDK already enables

FSDK's `files/linux/fdsdk-config.sh` (the config script applied on top of
`make defconfig`) is the evidence. Quoting symbols relevant to a microVM
(`enable X` = built in, `module X` = =m):

| Need | Symbols in FSDK's config |
| --- | --- |
| systemd baseline | `DEVTMPFS`, `CGROUPS`, `INOTIFY_USER`, `SIGNALFD`, `TIMERFD`, `EPOLL`, `SYSFS`, `PROC_FS`, `FHANDLE`, `SECCOMP`, `SECCOMP_FILTER`, `NET_NS`, `USER_NS`, `CGROUP_SCHED`, `CFS_BANDWIDTH`, `BLK_CGROUP`, `MEMCG`, `CGROUP_PIDS`, `CPUSETS`, `AUTOFS_FS`, `TMPFS_XATTR` |
| virtio devices | `VIRTIO_MENU`, `VIRTIO_PCI`, `VIRTIO_MMIO`, `VIRTIO_BLK`(m), `VIRTIO_NET`(m), `VIRTIO_CONSOLE`(m), `VIRTIO_BALLOON`(m), `VIRTIO_INPUT`(m), `VIRTIO_IOMMU`, `SCSI_VIRTIO`, `VIRTIO_MEM`(m, behind hotplug), `VIRTIO_FS`(m), `HW_RANDOM_VIRTIO` |
| vsock | `VSOCKETS`(m), `VIRTIO_VSOCKETS`(m), `VIRTIO_VSOCKETS_COMMON`(m), `VHOST_VSOCK`(m) |
| 9p / virtiofs | `NET_9P`, `NET_9P_VIRTIO`, `9P_FS`, `9P_FS_POSIX_ACL`, `9P_FS_SECURITY`, `FUSE_FS`(m), `VIRTIO_FS`(m) |
| filesystems | `XFS_FS`(m), `EXT4` (built in via defconfig), `BTRFS_FS`(m), `F2FS_FS`(m), `VFAT_FS`(m), `OVERLAY_FS`(m), `EROFS_FS`(m) with `EROFS_FS_ZIP_ZSTD`, `SQUASHFS`(m) |
| serial console | 8250 serial (from defconfig; the config script only adds `SERIAL_8250_DW`/`_CS` variants) plus the `console=ttyS0` cmdline |
| block layer / disks | `BLK_DEV_NVME`(m), `BLK_DEV_BSG` |
| KVM host side (nested / future) | `KVM`(m), `KVM_INTEL`(m), `KVM_AMD`(m), `VHOST_NET`(m), `VHOST_VSOCK`(m) |
| initramfs support | `BLK_DEV_INITRD`, `RD_{GZIP,BZIP2,LZMA,XZ,LZO,LZ4,ZSTD}` |

The same script also enables EFI stub bits, `DM_VERITY`, `FS_VERITY`,
`FANOTIFY`, `IKCONFIG`/`/proc/config.gz`, and many VM display/sound drivers; the
devices pluto cares about are all present. dakota vendors this exact file
(`/var/home/sid/Projects/dakota/files/linux/fdsdk-config.sh`, SPDX header
"Freedesktop-SDK Developers") with small downstream additions, which is
independent confirmation that these symbols are the intended VM set.

Caveats: `VIRTIO_BLK` is =m, so **root on a virtio disk requires an initramfs**
that carries it (or a config override to make it =y, which re-keys and rebuilds
the kernel). Keep the initramfs; it is cheap. `VIRTIO_MEM` is behind the memory
hotplug conditions; not required for M0.

### 4.3 How the kernels in this ecosystem handle config

- **FSDK `components/linux.bst`**: `make defconfig` → `. ./fdsdk-config.sh` →
  `make olddefconfig` → a check that reads `expected-configs` and **fails the
  build if any required config is missing** (hard failure on x86_64/aarch64).
  Install places `vmlinuz`, `vmlinux`, `System.map`, `config` under
  `/usr/lib/modules/<release>/` and modules under it, stripping modules
  (`INSTALL_MOD_STRIP=1`), plus `/usr/src/linux-<release>/` headers.
- **dakota `core/linux-fdsdk.bst`**: reproduces that recipe (junction-prefixed
  build-deps) with `CRYPTO_ZSTD=y`; `linux-ogc.bst` for gaming. Its header
  documents the exit condition for dropping the fork. The venv'd scripts in
  `files/linux/` must be re-synced on every FSDK bump.
- **server `flatcar-kernel.bst`**: Flatcar LTS kernel from upstream Flatcar
  sources with a patch queue (reproducible builds, secure boot, partition
  UUIDs). A second full kernel recipe to maintain; only worth it for LTS/ZFS
  needs.
- **dakota-kernel-ubuntu**: Ubuntu generic annotations + Dakota helper
  fragments; deterministic module signing/trust handling; OCI output.
- **GBM**: uses FSDK's kernel through its junction (`signed-boot.bst` depends
  strictly on `freedesktop-sdk.bst:components/linux.bst`).

The pattern: every downstream that needs a kernel *delta* owns a full copy of
the recipe and re-keys the kernel. FSDK's default config already covers
microVMs, so pluto's correct move is to have **no kernel delta** in M0.

### 4.4 vsock and SSH

`systemd-ssh-generator` (systemd v256+): "If invoked in a VM with `AF_VSOCK`
support, a socket-activated SSH per-connection service is bound to `AF_VSOCK`
port 22", only when an `sshd` binary is installed. It supports
`systemd.ssh_listen=` and a `ssh.ephemeral-authorized_keys-all` credential for
the host to inject public keys without persistent guest auth. The host side
connects via `systemd-ssh-proxy` (linked from the same man page). FSDK 26.08's
systemd is new enough (server's installer asserts "systemd must be version 261
or later"), so this works with no custom unit.

This gives pluto an authenticating, host-controlled SSH channel over vsock that
does not depend on guest networking. Because pluto has no trust model, the
`ssh.ephemeral-authorized_keys-all` credential is exactly the right identity
injection: the host generates a keypair per box incarnation and passes the
public key at boot. (Box identity provisioning beyond SSH is a later ticket;
`machine-id`, host keys, and persistence across pause/resume need their own
shape decision.)

### 4.5 Minimal recipe, concretely

To produce a bootable bundle, pluto builds only assembly elements; the heavy
leaves come from FSDK:

- kernel + modules: `freedesktop-sdk.bst:components/linux.bst` (pulled from
  cache if FSDK's public cache has it, else built);
- initramfs: a pluto `kind: script` using `components/dracut.bst` (already in
  `vm/minimal/deps.bst`) with `--no-hostonly` and the virtio/storage driver
  additions server uses as the reference list;
- rootfs: `kind: compose` over `vm/minimal/deps.bst` + `openssh-systemd` +
  `git` + agent;
- disk: genimage/ext4 via `vm/deploy-tools.bst`, or plain `mkfs.ext4`;
- no UKI, no bootloader, no ELF stub, no Secure Boot, no module signing.

---

## 5. Build-time costs and how Bluefin CI manages them

Concrete numbers found:

| Fact | Evidence |
| --- | --- |
| dakota warm build 2–5 min; cold 60–90 min; validate ~5 min; needs ~100 GB disk, ~16 GB RAM | dakota `docs/build.md` |
| server build job timeout 180 min; installer test 30 min | server `.github/workflows/build.yml` |
| Heavy builds go to remote execution; `BST_LOCAL=1` forces local; an unreachable cluster "fails loudly" | fsdk-containers `Justfile` (frameless 03 §2.7) |
| Remote execution + writable CAS over mTLS: `storage-service`, `remote-execution`, `action-cache-service` at `cache.projectbluefin.io:11002`; read-only mirrors `gbm.gnome.org:11003`, `cache.freedesktop-sdk.io:11001` | dakota `.github/actions/generate-bst-ci-config/action.yml` |
| Build must prove it used remote execution (banner asserted), otherwise the job fails | dakota `build.yml` via frameless 03 §1.8 |
| Kernel-only producer restores/saves BST cache; cold builds start from SDK upstream caches; OCI-only change does not recompile the kernel | dakota-kernel-ubuntu README |
| Kernel build pulls a Rust toolchain closure (`components/rust.bst`, `rust-bindgen`, `rust-src`), because FSDK enables `CONFIG_RUST` | FSDK `components/linux.bst`; `files/linux/fdsdk-config.sh` ("enable RUST") |
| qemu boot testing is cheap enough to run per image (`just boot-test`, `show-me-the-future`); CI alone never knew images boot ("nothing in CI starts a kernel") | dakota `docs/build.md`; frameless `PLAN.md` |

Management techniques worth copying, all evidenced above: remote execution for
heavy elements; read-only public mirrors so cold builds start warm; pushing
built artifacts back to a writable CAS so `publish` can export without
rebuilding; fail-closed checks that the executor and cache were actually used;
and a boot smoke test that is not optional.

For pluto's M0 costs: if the kernel, systemd, and other FSDK leaves pull from
public caches, `base` should build in single-digit minutes on the 12-core host
(the new work is compose + scripts + disk assembly, plus writing the ext4 image,
which is I/O-bound but small). If the kernel must build locally, expect a long
first build (tens of minutes to hours) dominated by the kernel and its Rust
dependencies; pre-pulling or falling back to `dakota-kernel-ubuntu`'s OCI are
the mitigations. The number to measure first is the pull coverage at the pinned
FSDK ref.

---

## 6. Uncertainty and open questions

1. **FSDK public cache coverage at the pinned ref** — not verified here (no
   builds, per constraints). Does `cache.freedesktop-sdk.io:11001` /
   `gbm.gnome.org:11003` hold `components/linux.bst`, `components/systemd.bst`
   and the `vm/*` elements for
   `freedesktop-sdk-26.08.0-0-gdb97cce...`? This single fact decides whether the
   first build is minutes or hours.
2. **Firecracker vs QEMU** for M0 — out of scope here (VMM ticket). The bundle
   contract is compatible with both (virtio block/net/vsock, serial console),
   but Firecracker validates only kernels from its policy list (v6.18 current)
   and FSDK ships v7.2.2; needs a boot test if firecracker is chosen.
3. **Root filesystem for boxes** — ext4 is the FSDK genimage default and the
   simplest; xfs (server's DDI) or btrfs would interact with snapshot/copy
   strategies for pause and disk-state dedup. That intersects the storage
   ticket, not this one.
4. **Module signing / linux-module-cert override** — FSDK's kernel strictly
   build-depends on `components/linux-module-cert.bst`; frameless overrides it
   with GBM's keyed certificate. pluto should use FSDK's (empty) element and
   unsigned modules, but this needs a one-line confirmation when the graph is
   first loaded.
5. **Per-incarnation identity** — machine-id, sshd host keys, authorized keys,
   and what survives pause/resume. `systemd-ssh-generator`'s credential
   injection is the SSH half; the rest is a shape-lifecycle question.
6. **Build cache scope for pluto** — local casd only for M0, or a Buildbarn
   instance on the host? The map does not yet decide builder caching; the disk
   budget (~100 GB for dakota) suggests sizing before the first full build.

## Sources

Upstream repos and files (primary):

- dakota: `elements/oci/bluefin.bst`, `elements/oci/layers/*.bst`,
  `elements/core/linux-fdsdk.bst`, `files/linux/fdsdk-config.sh`,
  `docs/build.md`, `docs/ci.md`,
  `.github/actions/generate-bst-ci-config/action.yml` —
  <https://github.com/projectbluefin/dakota> (local clone `3d549f4`)
- server: `elements/oci/bluefin-server-ddi.bst`,
  `elements/oci/bluefin-server-installer.bst`,
  `elements/oci/k0s-sysext.bst`, `elements/flatcar/flatcar-kernel.bst`,
  `elements/bluefin-server/os-stack.bst`, `files/os/sysupdate.d/*.transfer`,
  `project.conf`, `.github/workflows/build.yml` —
  <https://github.com/projectbluefin/server> (local clone `5ebfeae`)
- fsdk-containers: `elements/podman-vm/*.bst`, `elements/targets.json`,
  `project.conf` — <https://github.com/projectbluefin/fsdk-containers>
  (local clone `c2f584a`)
- freedesktop-sdk at `freedesktop-sdk-26.08.0-0-gdb97cce32cecadc7a3e98f06d557ebfa6ba9ad46`:
  `elements/vm/minimal/{deps,filesystem,efi,virt}.bst`,
  `elements/vm/boot/{efi,virt}.bst`, `elements/vm/deploy-tools.bst`,
  `elements/vm/prepare-image.bst`, `elements/components/linux.bst`,
  `elements/include/linux.yml`, `elements/components/oci-builder.bst`,
  `files/linux/fdsdk-config.sh`, `files/oci/oci_builder/*` —
  <https://gitlab.com/freedesktop-sdk/freedesktop-sdk>
- gnome-build-meta `gnome-51`: `elements/gnomeos/signed-boot.bst`,
  `elements/gnomeos/sysupdate-config.bst`, `elements/gnomeos/update-images.bst`,
  `elements/oci/initramfs/*` —
  <https://gitlab.gnome.org/GNOME/gnome-build-meta>
- dakota-kernel-ubuntu `development`: README, `elements/kernel.bst`,
  `elements/kernel-image.bst` — <https://github.com/projectbluefin/dakota-kernel-ubuntu>

Documentation and specs:

- BuildStream: cache keys —
  <https://docs.buildstream.build/master/arch_cachekeys.html>; caches —
  <https://docs.buildstream.build/master/arch_caches.html>; installation and
  BuildBox components — <https://docs.buildstream.build/master/main_install.html>;
  configuring cache servers (REAPI, Buildbarn compose) —
  <https://docs.buildstream.build/master/using_configuring_cache_server.html>;
  commands — <https://docs.buildstream.build/master/using_commands.html>
- REAPI — <https://github.com/bazelbuild/remote-apis>; Buildbarn —
  <https://github.com/buildbarn>; Buildbox —
  <https://gitlab.com/BuildGrid/buildbox>
- QEMU "Direct Linux Boot" —
  <https://www.qemu.org/docs/master/system/linuxboot.html>
- Firecracker design — <https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/design.md>;
  kernel support policy —
  <https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/kernel-policy.md>
- systemd `systemd-ssh-generator` man page (v256+) —
  <https://raw.githubusercontent.com/systemd/systemd/main/man/systemd-ssh-generator.xml>
  (rendered: `man systemd-ssh-generator`)

Secondary/local research notes used as maps (not authority):

- frameless `docs/research/02-buildstream.md`, `03-projectbluefin-buildstream.md`,
  `07-dakota-alpha-6.md`, `12-core-versus-desktop.md`,
  `13-owning-the-boot-spine.md`, `PLAN.md` —
  `/var/home/sid/Documents/Projects/frameless`
- pluto map #1: <https://github.com/Siddhj2206/pluto/issues/1>
