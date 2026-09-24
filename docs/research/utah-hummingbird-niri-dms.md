# Utah / utah-packages research — transplanting Hummingbird + niri + DMS onto the finpilot layout

**Date:** 2026-09-22
**Author:** research subagent (DeepSeek V4.1 Flash via OpenCode)
**Subject:** how `projectbluefin/utah` and `projectbluefin/utah-packages` are built, and how
pluto can express a Hummingbird + niri + DankMaterialShell (DMS) desktop in the new
finpilot build layout.

**Method.** Cloned `projectbluefin/utah` and `projectbluefin/utah-packages` at `main`
into `/tmp/opencode/utah-research/`; extracted the preserved old pluto implementation
from `origin/archive/pluto-pre-rewrite` (`18d57a0`) into `/tmp/opencode/pluto-archive/`;
verified the live Hummingbird base digest with `skopeo`. All claims cite a file path and,
where useful, a line. Exact commands and URLs are in [Sources](#sources).

---

## TL;DR

1. **utah is a GNOME distro, not a niri distro.** It is Bluefin's desktop aimed at
   `projectbluefin/utah-packages` — a GitHub-Actions RPM factory that rebuilds the
   GNOME 51 desktop stack against Hummingbird. There is **no niri, DMS, greetd,
   quickshell or COPR** anywhere in either repository (verified by grep over both trees).
   Utah and utah-packages are a model for **how to build on Hummingbird**, not a source
   of pluto's desktop packages.
2. **niri + DMS must keep coming from Fedora repos + COPRs**, exactly as in the archived
   pluto (`build/packages/niri.toml`, `build/40-niri.sh`). `utah-packages` does not build
   them, and Hummingbird carries no desktop stack at all.
3. The finpilot layout is a good fit: `20-packages-and-services.sh` is the package seam
   and `copr-helpers.sh` is the COPR-removal mechanism, but **three Hummingbird-specific
   gaps must be closed before any of it works** (details in §9):
   - the Hummingbird base has no `dnf5-plugins` and no `rsync`, and `10-overlay.sh`
     needs `rsync`; `dnf5 config-manager` needs the plugin;
   - `dnf5 copr enable` auto-detects the wrong chroot on Hummingbird
     (`hummingbird-<build>`), so `copr_install_isolated` must be taught to pass
     `fedora-<major>-<arch>` like the archived `build/scripts/package-lib.sh` did;
   - `00-image-info.sh` derives `FEDORA_MAJOR_VERSION` from the base's `os-release`,
     whose `VERSION_ID` on Hummingbird is the **hum build number**, not a Fedora release.
     It must be passed explicitly as an ARG.
4. `just build` parses the `FROM` line and fails on `FROM ${BASE_IMAGE}`; **keep a literal
   pinned base `FROM` line** (not utah's `ARG BASE_IMAGE` indirection).

---

## 1. What utah is

`projectbluefin/utah` — *"Bluefin built with Fedora Hummingbird technology"* — is a
pre-alpha bootc image that composes a GNOME workstation on top of the Hummingbird
`bootc-os` base. From `utah:README.md:23-38`:

> Bluefin built on Fedora Hummingbird. […] Utah adds the desktop: Bluefin's package
> contract on top, and the GNOME 51 stack built from source because neither Hummingbird
> nor a Fedora release ships it.

Two repositories, mirroring the existing `common` / `brew` split (`utah:README.md:33-38`):

| Repository | Role |
|---|---|
| `projectbluefin/utah` | Composes the image (Containerfile + scripts + manifests). |
| `projectbluefin/utah-packages` | Builds GNOME 51 + desktop-stack RPMs from verified upstream sources; publishes them as an OCI image. |

Desktop stack: **GNOME 51** (gnome-shell, mutter, gnome-session, gtk4, libadwaita,
control-center, settings-daemon), served from the utah-packages factory
(`utah:packages/utah.toml:36-61`). Greeter is **GDM** (`utah:scripts/configure-services.sh:66`),
not greetd. There is no niri/DMS.

Relationship to `common`/`brew`: utah consumes `ghcr.io/projectbluefin/common` (both
`/system_files/shared` and `/system_files/bluefin`) and `ghcr.io/ublue-os/brew`
(`utah:Containerfile:15-18,76-78`). The finpilot template pluto currently consumes
`common`'s `/system_files` and `brew`'s `/system_files`, overlays only
`common/shared` (`pluto:build/10-overlay.sh:43`).

---

## 2. Base image, pinning, and Fedora tracking

`utah:Containerfile:1-18`:

```dockerfile
ARG BASE_IMAGE=quay.io/hummingbird-community/bootc-os:latest@sha256:db1007fdcda076f2d7fd0e2adfe998141dd1908b8f66c732654f762c6a8b2728
ARG PACKAGE_IMAGE=ghcr.io/projectbluefin/utah-packages
ARG PACKAGE_IMAGE_SHA=sha256:ca320b39b5f40bea9516f6f1c11e70d352c35f1c3d109b3aaf0816be007468f7
ARG PACKAGE_IMAGE_REF=${PACKAGE_IMAGE}@${PACKAGE_IMAGE_SHA}
ARG COMMON_IMAGE=ghcr.io/projectbluefin/common
ARG COMMON_IMAGE_SHA=sha256:507abcb5be69af93dcf351f69b03f4fbc08bba2eb8f58db62f8e5d0060b69b95
ARG BREW_IMAGE=ghcr.io/ublue-os/brew
ARG BREW_IMAGE_SHA=sha256:60ada2d65891d8797beef49d8b43f2108519cbbaf04c9c7363e1a008677fcd35

FROM ${COMMON_IMAGE}@${COMMON_IMAGE_SHA} AS common
FROM ${BREW_IMAGE}@${BREW_IMAGE_SHA} AS brew
FROM ${PACKAGE_IMAGE_REF} AS packages
FROM ${BASE_IMAGE}
```

- **Registry/repo:** `quay.io/hummingbird-community/bootc-os`, rolling tag `:latest`,
  digest-pinned (the digest is the **index** digest for a multi-arch image).
- **Live digest, verified 2026-09-22** with
  `skopeo inspect docker://quay.io/hummingbird-community/bootc-os:latest`:
  `sha256:9d69f6f33f5af87c76b0d7f49387bc4b969271a8eb788970396d6eab2b5af8a2`.
  Utah's pinned `db1007fd…` is an older index digest; the tag has moved since. **Refresh
  at write time.** (This is the same digest the archived pluto pinned:
  `pluto-archive:Containerfile:55`.)
- **Fedora major** is *not* read from the base. Hummingbird pairs with **Fedora 44**
  (`utah-packages:docs/targeting-hummingbird.md`, "Paired with Fedora 44"; buildroot
  `utah:packages/fedora-44.repo`). Hummingbird's `os-release` `VERSION_ID` is the hum
  build number, so a consumer must carry the Fedora stream itself (the archived pluto's
  `FEDORA_MAJOR_VERSION="44"` ARG, `pluto-archive:Containerfile:70-71`).
- **Pinning/updates:** Renovate. `utah:renovate.json` extends
  `local>projectbluefin/renovate-config`; the base ARG is a docker datasource that
  Renovate bumps by digest. `utah-packages` shows the same pattern in its commit history
  (`chore(deps): update quay.io/fedora/fedora:44 docker digest to daa3c4f (#223)`).
- **utah only builds x86_64** for bootc images (`utah:.github/workflows/build.yml:157`),
  though the base is multi-arch.
- **Containerfile.kernel** builds an OGC/NVIDIA kernel-cache image from the *same*
  `ARG BASE_IMAGE` (`utah:Containerfile.kernel:19`); irrelevant to pluto unless NVIDIA
  flavors are wanted.

---

## 3. Repository layout

### 3.1 `projectbluefin/utah`

`git ls-files` summary (`utah@a567795`):

- `Containerfile` — 4-stage compose (common/brew/packages/base), one `RUN` per phase.
- `Containerfile.kernel` — the kernel-cache image for flavored builds.
- `Justfile` — local build, VM, ISO, kernel-cache, contract-check recipes.
- `packages/` — `bluefin.toml` (verbatim copy of Bluefin's `base.toml`), `utah.toml`
  (the overlay), and repo files: `hummingbird.repo`, `utah-packages.repo`,
  `fedora-44.repo`, `nvidia-container.repo`, plus `RPM-GPG-KEY-redhat-release-2`.
- `contracts/bluefin-desktop.toml` + `scripts/verify-desktop-contract.py` — post-install
  assertions about branding, defaults, services.
- `scripts/` — the phase logic: `install-packages.py`, `configure-services.sh`,
  `configure-branding.sh`, `clean-stage.sh`, `verify-rpm-contract.py`,
  `build-gnome-extensions.sh`, `install-ogc-kernel.sh`, `install-nvidia.sh`,
  `mirror-shim.sh`, `check-repo-availability.py`, `flavors.py`.
- `system_files/shared/` — systemd presets/units, dconf, GNOME extensions, hooks.
- `config/flavors.json` — the single source for the build/promote/release matrix.
- `iso/live/` + `iso/scripts/` — live ISO build and LUKS E2E.
- `tests/` — Python unittest suite.
- `.github/workflows/` — `build.yml`, `execute-release.yml`, `post-testing-e2e.yml`,
  `promote-testing-to-main.yml`, `sync-main-to-testing.yml`, `pages.yml`.
- `docs/skills/` — the deep docs, including `containerfile.md`, `package-contract.md`,
  `flavors.md`, `desktop-contract.md`.

### 3.2 `projectbluefin/utah-packages`

- `packages/<name>/` — **352 recipe directories**, each with `<name>.spec`, patches,
  a `sources` file, a `changelog`, and `.hummingbird-upstream.json` recording the Fedora
  dist-git origin (repo, commit, tree). Examples: `gnome-shell`, `mutter`, `gtk4`,
  `mesa`, `pipewire`, `flatpak`, `distrobox`, `ffmpeg`.
- `config/` — `upstream-sources.json` (verified direct-source allowlist), `bluefin-packages.toml`
  (Bluefin parity manifest), `runtime-contract.toml` (consumer transaction), `hummingbird.repo`,
  `factory-contract.json`.
- `.packit.yaml` — generated monorepo Packit config (1,849 lines, one entry per recipe).
- `.github/workflows/` — `rebuild-rpms.yml` (the pipeline), `build-stage.yml` (one
  dependency wave), `source-pipeline.yml`, `import-rawhide-package.yml`,
  `packit-srpm-pilot.yml`, `packit-srpm-chunk.yml`, `recalculate-hummingbird-gaps.yml`,
  `validate.yml`.
- `docs/` — `architecture.md`, `targeting-hummingbird.md`, `contributing.md`.
- `Justfile` — `check` = `factory-check` + `validate`; `test` = pytest.
- `.agents/skills/` — `hummingbird`, `build-failure-triage`.

---

## 4. Build-time mechanics (utah)

This is the part worth transplanting.

### 4.1 Manifests and repo files are copied in early, then the package image is bind-mounted

`utah:Containerfile:42-47`:

```dockerfile
COPY packages/bluefin.toml packages/utah.toml contracts/bluefin-desktop.toml /usr/share/utah/
COPY packages/hummingbird.repo packages/nvidia-container.repo packages/utah-packages.repo /etc/yum.repos.d/
COPY packages/RPM-GPG-KEY-redhat-release-2 /etc/pki/rpm-gpg/
```

The factory image is **never copied in** — it is an RPM repository mounted only for the
install steps (`utah:Containerfile:126,190`):

```dockerfile
RUN --mount=type=bind,from=packages,source=/repository,target=/etc/utah-packages,ro \
    /usr/local/libexec/utah-install-packages /usr/share/utah/bluefin.toml /usr/share/utah/utah.toml
```

It used to be `COPY --from=packages`, which shipped ~4 GB into every image
(`utah:Containerfile:48-56`). The `Justfile` even asserts that a `COPY --from=packages`
never returns (`utah:Justfile:85-88`).

### 4.2 Repositories are selected by annotation, not hardcoded

`packages/utah-packages.repo` carries `# utah-install: true` above the section
(`utah:packages/utah-packages.repo:4-15`), and `install-packages.py` (`utah:scripts/install-packages.py:20-81`)
parses every `*.repo` for that marker, orders them by `priority` ascending, and installs
with:

```python
dnf, "-y", "--disablerepo=*", *(f"--enablerepo={r}" for r in repos),
"-x", "PackageKit*", "install", *packages, *build_deps
```
(`utah:scripts/install-packages.py:241-245`)

- `utah-packages.repo` has `priority=1`; Hummingbird's own `hummingbird.repo` has
  `priority=10` (`utah:packages/utah-packages.repo:15`, `utah:packages/hummingbird.repo:18`).
  So factory rebuilds win over equal-versioned Hummingbird packages.
- `hummingbird.repo` runs with `gpgcheck=1` against the committed Red Hat release key
  (`utah:packages/hummingbird.repo:10-18`, `utah:packages/RPM-GPG-KEY-redhat-release-2`).
- **Fedora repositories are never enabled at runtime.** `fedora-44.repo` says so explicitly
  and is only used in `Containerfile.kernel`'s builder stage (`utah:packages/fedora-44.repo:1-4`).
- All contract packages are `dnf mark user`, so `[excluded]` removals cannot drag them
  back out (`utah:scripts/install-packages.py:249-255`).
- A missing package is a **build failure**, not a skip. The only skips are names in
  `[unavailable]`, each with a tracking issue (`utah:packages/utah.toml:178-250`).

### 4.3 Repository teardown and image hygiene

After the last install (the flavor/NVIDIA step), utah flips the package repo off because
its `file://` baseurl no longer exists at runtime (`utah:Containerfile:202-207`):

```dockerfile
sed -i 's/^enabled=1$/enabled=0/' /etc/yum.repos.d/utah-packages.repo \
  && grep -q '^enabled=0$' /etc/yum.repos.d/utah-packages.repo
```

Then `clean-stage.sh` and `bootc container lint --fatal-warnings --skip nonempty-boot`
run in the same layer (`utah:Containerfile:214-215`). `clean-stage.sh`
(`utah:scripts/clean-stage.sh`):

- deletes everything under `/var` except `cache`, and everything under `/var/cache`
  except `rpm-ostree` (the `libdnf5` keyring left by third-party GPG imports was a lint
  failure, `utah:scripts/clean-stage.sh:36-40`);
- empties `/run` and `/tmp` **without replacing the directories** (a bind-mounted
  `/run/.containerenv` makes `rm -rf /run` fail, `utah:scripts/clean-stage.sh:42-53`).

`configure-services.sh` is the desktop enablement step (`utah:scripts/configure-services.sh`):
Hummingbird ships a server preset, so it explicitly enables gdm, bluetooth, firewalld,
fwupd(.service + refresh.timer), dconf-update, tailscaled, uupd.timer,
ublue-system-setup, systemd-resolved, bootc-unified-storage; masks
`bootc-fetch-apply-updates.*` in `/usr/lib` and `/etc`; patches logind's lid/sleep
defaults; fetches the Flathub remote descriptor; and removes the extension build
toolchain afterwards (`dnf remove --no-autoremove dbus-devel glib2-devel meson sassc`).

**utah has no COPR layer at all**, so it has no equivalent of the finpilot COPR teardown.
For pluto, the finpilot `copr_install_isolated` + `90-cleanup.sh` already is that
mechanism.

---

## 5. utah-packages: the factory

### 5.1 What it builds

GNOME 51 and the desktop dependency closure Hummingbird lacks: gnome-shell, mutter,
gtk4, libadwaita, gnome-session, gnome-control-center, pipewire, mesa, flatpak, distrobox,
ffmpeg, fonts, and the various libraries Bluefin's contract needs. Every recipe is a
Fedora Rawhide spec/patches seed with the **source fetched directly from upstream** and
verified (SHA-512 + optional signature), per `utah-packages:README.md:73-91` and
`docs/targeting-hummingbird.md`.

- Disttag convention: `.hum1.bfin` (vendor release + `hum1` + factory suffix), e.g.
  `gnome-shell-…-3.hum1.bfin` (`utah-packages:README.md:52-55`,
  `docs/architecture.md`).
- Buildroot pairs Hummingbird's Pulp overlay with Fedora 44 (`config/hummingbird.repo`
  has `gpgcheck=0` for the *buildroot only*, unlike utah's runtime copy; plus
  `excludepkgs=ruby3.3-default-gems,ruby3.4-default-gems`).

### 5.2 How it is published

As an **OCI image**, not a Copr and not a Pages repo
(`utah-packages:README.md:32-42`):

```text
ghcr.io/OWNER/utah-packages — an image whose only content is the createrepo_c output,
consumed with COPY --from= pinned by digest.
```

`main` publishes `:latest`; every other branch publishes under its own branch name so a
consumer can build against unmerged packages. The image is signed keylessly with cosign
on GitHub OIDC (`utah-packages:.github/workflows/rebuild-rpms.yml:868`). utah consumes it
at `ghcr.io/projectbluefin/utah-packages@sha256:ca320b39…`
(`utah:Containerfile:5-6`). A GitHub Pages mirror existed and was removed.

### 5.3 Packit

- `.packit.yaml` is a **generated monorepo config** (`tools/render_packit_config.py`),
  one package block per recipe, generated create-archive action.
- Packit is scoped to **SRPM validation only**: `packit srpm --preserve-spec` in
  `packit-srpm-pilot.yml` / `packit-srpm-chunk.yml`, which is manual-dispatch and
  verification-only, feeding nothing (`utah-packages:docs/architecture.md`, "What Packit
  is for here").
- **Packit-as-a-Service, Copr, Koji, Bodhi, Testing Farm, Kubernetes and self-hosted
  runners are explicitly not dependencies** (`docs/architecture.md`). `copr_build` was
  considered as a future binary lane and is not pursued (issues #43).
- The binary lane actually builds with bare `rpmbuild` in `quay.io/fedora/fedora:44`
  containers, staged in waves 0–4, and publishes the overlay OCI image.
  (This is a documented "unfinished implementation", relevant to the other research
  stream: utah-packages uses Packit for SRPMs, not for binary publication.)

---

## 6. niri and DMS: exact coordinates

**Neither utah nor utah-packages contains niri, greetd, quickshell, DMS or danklinux
recipes.** Verified:

```text
$ grep -ril "niri\|greetd\|quickshell\|DankMaterial\|dms" utah-packages/packages utah-packages/config
(no meaningful hits; only incidental matches inside unrelated specs)
$ ls utah-packages/packages | grep -iE 'niri|greetd|quickshell|dms|dank'
NONE
$ grep -ril "niri\|greetd\|quickshell" utah
(no hits)
```

So pluto's desktop comes from Fedora repos + COPRs, taken from the archived pluto
implementation. Coordinates (all from `pluto-archive:build/packages/niri.toml` and
`pluto-archive:docs/research/copr-inventory.md`):

### 6.1 niri

- **Fedora package:** `niri` (in Fedora proper; archived pluto used Fedora's, *not* a
  COPR). It ships `/usr/bin/niri`, `niri-session`,
  `/usr/share/wayland-sessions/niri.desktop`, and `/usr/lib/systemd/user/niri.service`.
- **Hard Requires:** `xwayland-satellite` (`pluto-archive:build/packages/niri.toml:20`).
- **Ecosystem utilities:** `kanshi`, `wtype`, `wl-mirror`, `wev`, `udiskie`,
  `gnome-disk-utility`, `accountsservice`, `nm-connection-editor`.
- **Qt theming:** `qt6ct`, `kf6-kimageformats`, `plasma-breeze`, `kf6-kirigami`,
  `kf6-qqc2-desktop-style`.
- **DMS calendar backend:** `khal`.
- There is no `xdg-desktop-portal-niri`; niri's spec points at
  `xdg-desktop-portal-gnome` (`pluto-archive:docs/research/niri-layer-design.md:278-279`).

### 6.2 DankMaterialShell (DMS) and the greeter

- **COPR `avengemedia/dms`** (`fedora-44-x86_64` exists): `dms`, `dms-cli`.
  Stable 1.5.3. It has a **coprdep on `avengemedia/danklinux`**
  (`pluto-archive:docs/research/copr-inventory.md:9-23`).
- **COPR `avengemedia/danklinux`**: `dms-greeter`, `quickshell-git` (or stable
  `quickshell`), `matugen`, `danksearch`, `dgop`, `material-symbols-fonts`
  (`pluto-archive:build/packages/niri.toml:57-65`).
- `dms` Requires `(quickshell or quickshell-git)`, `accountsservice`, `dms-cli`, `dgop`;
  Recommends cava, danksearch, matugen, NetworkManager, qt6-qtmultimedia.
  `dms-greeter` Requires `(quickshell-git or quickshell)` + `greetd` (so greetd is pulled
  automatically).
- **greetd** is a Fedora package (`greetd`, `greetd-selinux`); the greeter is
  `dms-greeter`, which runs niri as the greeter compositor.
- **Session start:** `/etc/greetd/config.toml` (shipped as a file,
  `pluto-archive:custom/files/etc/greetd/config.toml`):

  ```toml
  [terminal]
  vt = 1

  [default_session]
  user = "greeter"
  command = "/usr/bin/dms-greeter --command niri --cache-dir /var/cache/dms-greeter -C /etc/greetd/niri/config.kdl"
  ```

  with a minimal `/etc/greetd/niri/config.kdl`
  (`pluto-archive:custom/files/etc/greetd/niri/config.kdl`, just
  `hotkey-overlay { skip-at-startup }`).
- **DMS autostart:** a **user preset** `90-pluto-dms.preset` containing `enable dms.service`
  (`pluto-archive:custom/files/usr/lib/systemd/user-preset/90-pluto-dms.preset`), applied
  with `systemctl --global preset-all`. Do **not** also add
  `spawn-at-startup "dms" "run"` to the niri config — double-start trap
  (`pluto-archive:build/40-niri.sh:51-58`).
- `greetd.service` enabled; `systemctl set-default graphical.target`.
- `/etc/pam.d/greetd` was **not** shipped: stock already carries pam_selinux/pam_loginuid,
  and the hand-written file broke the session bus (`pluto-archive:build/40-niri.sh:36-38`).
- `/usr/share/xdg-terminal-exec/xdg-terminals.list` points at
  `com.mitchellh.ghostty.desktop` (`pluto-archive:custom/files/usr/share/xdg-terminal-exec/xdg-terminals.list`).
- **ghostty** comes from COPR `scottames/ghostty` (not in Fedora)
  (`pluto-archive:build/packages/base.toml:142-143`).
- Config lives in `/etc/skel`: `.config/niri/config.kdl` + `dms/*.kdl`,
  `.config/environment.d/90-dms.conf`, `.config/ghostty/{config,themes/dankcolors}`
  (`pluto-archive:custom/config/...`). A one-time login hook (`25-skel-config.sh`) applies
  skel to existing users (`pluto-archive:custom/files/usr/share/ublue-os/user-setup.hooks.d/25-skel-config.sh`).

---

## 7. Supporting desktop stack used by the archived pluto

From `pluto-archive:build/packages/{base,firmware,multimedia,dx}.toml` and the numbered
scripts. pluto can stay leaner, but Hummingbird supplies **none** of this:

- **Core desktop foundation** (`base.toml`): `pipewire`, `wireplumber`,
  `pipewire-pulseaudio`, `pavucontrol`, `alsa-lib`; `mesa-dri-drivers`,
  `mesa-vulkan-drivers`, `vulkan-loader`; `fontconfig`, `default-fonts`,
  `adwaita-fonts-all`, noto fonts, `jetbrains-mono-fonts-all`; portals
  (`xdg-desktop-portal`, `-gnome`, `-gtk`), `xdg-user-dirs`, `xdg-utils`,
  `xdg-terminal-exec`; `gnome-keyring`, `gnome-keyring-pam`, `polkit-kde`;
  `NetworkManager-wifi`, `wpa_supplicant`, `firewall-config`, `tailscale`,
  `wireguard-tools`, `fwupd`, `microcode_ctl`, `brightnessctl`, `playerctl`,
  `NetworkManager-tui`, `ddcutil`, `input-remapper`, `lm_sensors`, `powertop`,
  `smartmontools`, `evtest`, `libva-utils`, `igt-gpu-tools`, `libratbag-ratbagd`,
  `ifuse`, `libimobiledevice`, `usbmuxd`, `solaar-udev`, `openrgb-udev-rules`;
  `wl-clipboard`, `waypipe`, `nautilus`, `distrobox`, `cava`, `qt6-qtmultimedia`,
  `dconf`, `glib-networking`, `flatpak`, `cups-pk-helper`, `langpacks-en`, `git`,
  `git-credential-libsecret`, `jq`, `rsync`, `fastfetch`, `greetd`, `greetd-selinux`;
  `adw-gtk3-theme`, appindicator libs, `7zip-standalone`, `borgbackup`;
  `power-profiles-daemon`, `zram-generator`; `fish`, `zsh`; `gum`, `just`, `tmux`.
- **YubiKey/FIDO2:** `pam-u2f`, `pamu2fcfg`, `pam_yubico`, `gnupg2-scdaemon`,
  `ykpers`, `ykclient`, `yubikey-manager`, `python3-yubikey-manager`.
- **Firmware** (`firmware.toml`, ~245 MB): `linux-firmware` plus every client vendor split
  (mt7xxx, mediatek, amd-gpu, amd-ucode, iwlwifi-*, intel-gpu, realtek, brcmfmac,
  atheros, alsa-sof-firmware, …). F44 has no `linux-firmware` metapackage, so the splits
  must be named.
- **Multimedia** (`multimedia.toml`): negativo17 `fedora-multimedia` repo, kept enabled
  at runtime, priority 90; mesa/VA overrides versionlocked; ffmpeg/libfdk-aac/gstreamer
  full codecs; vendor-asserted `negativo17.org`.
- **DX** (`dx.toml`): `android-tools`, `libvirt-client` + `libvirt-daemon-qemu` + `qemu-kvm`,
  and `docker-ce` from Docker's official Fedora repo removed after install.
  Socket-activated daemons; group membership via the ublue setup hooks.
- **Power/memory:** `zram-generator` config at `/usr/lib/systemd/zram-generator.conf`;
  `power-profiles-daemon` (not tuned); `fwupd-refresh.timer`.
- **auto-updates:** `uupd` + `oversteer-udev` from COPR `ublue-os/packages`
  (`pluto-archive:build/packages/base.toml:145-149`), which is exactly what the current
  finpilot `20-packages-and-services.sh:40` already installs (uupd only).
- **theme:** `zz0-pluto-theme.gschema.override` (`prefer-dark`, adw-gtk3, Adwaita icons),
  compiled with `glib-compile-schemas`.
- **flatpak theming:** a first-boot `flatpak-theming.service` masks the adw-gtk3 GTK3
  theme flatpaks and sets Wayland overrides for Firefox/Zen.

---

## 8. Archived pluto ↔ utah comparison

| Concern | Archived pluto (`archive/pluto-pre-rewrite`, `18d57a0`) | utah (`a567795`) | Recommendation for new pluto |
|---|---|---|---|
| Base | `quay.io/hummingbird-community/bootc-os:latest@sha256:9d69f6f…` direct `FROM` | `ARG BASE_IMAGE=… @sha256:db1007fd…`, `FROM ${BASE_IMAGE}` | Keep a **literal pinned `FROM`** (finpilot `just build` parses it; `FROM ${VAR}` fails). Use live digest. |
| Fedora stream | `ARG FEDORA_MAJOR_VERSION="44"` → `/etc/dnf/vars/releasever`; adds Fedora repos at build | No Fedora at runtime; only `Containerfile.kernel` builder uses Fedora 44 | Carry `ARG FEDORA_MAJOR_VERSION="44"`, pass it to `00-image-info.sh`, add Fedora repos as build-time sources. |
| Package source | Fedora repos + COPRs (niri, ghostty, uupd) + negativo17 multimedia | `utah-packages` factory + Hummingbird only | Fedora + COPRs, since niri/DMS are not in the factory. |
| Manifest format | `build/packages/*.toml` sections incl. `["copr:owner/project"]` | `packages/{bluefin,utah}.toml` sections; repo selection by `# utah-install: true` annotation | Keep TOML manifests; one section per source, install in `20-packages-and-services.sh`. |
| Package install | `read-packages` + `package-lib.sh` (`install_fedora_section`, `install_copr_sections` with explicit `copr_chroot`), all COPRs enabled then one transaction | `install-packages.py` with `--disablerepo=*` + annotated repos, `mark user`, `[excluded]` removal | Reuse the **COPR-enable-all-then-one-transaction** pattern and the explicit chroot; the finpilot isolated helper needs both fixes. |
| Desktop | niri + DMS + greetd | GNOME 51 + GDM | niri + DMS. |
| Services | `systemctl enable greetd`, `set-default graphical.target`, user preset for `dms.service` | `configure-services.sh` equivalent (`gdm`, masks bootc-fetch-apply-updates, etc.) | New `NN-niri.sh` phase or extend `20`; mask `bootc-fetch-apply-updates.*` like utah does. |
| Repo teardown | `clean-stage.sh` removes `/etc/yum.repos.d/_copr*.repo` | `sed enabled=0` on the factory repo; factory bind-mounted, never copied | finpilot `90-cleanup.sh` already disables COPRs/rpmfusion/tailscale/multimedia. Confirm any new repo (fedora) policy. |
| Hygiene | `clean-stage.sh` prunes `/var`, `/run`, `/tmp`, `/boot` | `clean-stage.sh` same shape | Reuse finpilot `90-cleanup.sh`. |
| COPR chroot | `copr_chroot()` = `fedora-$(cat /etc/dnf/vars/releasever)-$(uname -m)` | n/a | **Required** on Hummingbird; the finpilot helper lacks it. |
| DX | docker-ce (removed), libvirt/qemu, adb | nvidia-container, containerd, distrobox | Optional; keep out of a minimal pluto unless needed. |

---

## 9. Transplant plan

Goal: Hummingbird base + niri + DMS expressed in the finpilot layout, keeping the
finpilot phase discipline (packages in the package phase, no overlay edits invalidating
the package layer, cleanup last).

### 9.1 `Containerfile`

1. **Base swap** — replace the Silverblue line (`pluto:Containerfile:53`) with the live
   Hummingbird pin. Verify the digest with `skopeo inspect` first; Renovate will keep it
   moving:

   ```dockerfile
   # Base Image - Fedora Hummingbird bootc-os (minimal bootc OS, no desktop at all).
   # Rolling :latest; Renovate batches digest bumps. Fedora major tracked by
   # FEDORA_MAJOR_VERSION below (the base's os-release VERSION_ID is the hum build number).
   FROM quay.io/hummingbird-community/bootc-os:latest@sha256:9d69f6f33f5af87c76b0d7f49387bc4b969271a8eb788970396d6eab2b5af8a2
   ```

   Do **not** introduce `ARG BASE_IMAGE` + `FROM ${BASE_IMAGE}`: `just build` greps the
   first unaliased `FROM` and aborts when it cannot extract a tag
   (`pluto:Justfile:150-157`).

2. **Identity ARGs** — add the Fedora stream next to the existing identity ARGs
   (`pluto:Containerfile:57-62`). Keep `IMAGE_VENDOR` (currently `projectbluefin`; the old
   pluto used `siddhj2206` — decide) and set the base name:

   ```dockerfile
   ARG FEDORA_MAJOR_VERSION="44"
   ```

   `just build` supplies `BASE_IMAGE_NAME=bootc-os` from the FROM line automatically
   (`pluto:Justfile:206`), but it does **not** supply `FEDORA_MAJOR_VERSION`; the ARG
   default is what `00-image-info.sh` sees.

3. **Early bootstrap RUN** — the template's config RUN (`pluto:Containerfile:80-83`) cannot
   run on Hummingbird as written: `dnf5 config-manager` needs `dnf5-plugins`, which the
   base lacks, and `10-overlay.sh` needs `rsync` before the package phase. Replace it with
   a Hummingbird bootstrap modelled on `pluto-archive:Containerfile:104-113`:

   ```dockerfile
   # Hummingbird ships dnf5 but not its plugins, not rsync, and its os-release
   # VERSION_ID is a hum build number. Feed dnf the Fedora stream, add Fedora as a
   # build-time source (fedora.repo/fedora-updates.repo are priority 99, below
   # Hummingbird's 10), and bootstrap fedora-gpg-keys in the same transaction.
   COPY custom/files/etc/yum.repos.d/fedora.repo \
        custom/files/etc/yum.repos.d/fedora-updates.repo /etc/yum.repos.d/
   RUN mkdir -p /etc/dnf/vars \
    && printf '%s\n' "${FEDORA_MAJOR_VERSION}" > /etc/dnf/vars/releasever \
    && cp /etc/dnf/dnf.conf /etc/dnf/dnf.conf.tmp \
    && mv /etc/dnf/dnf.conf.tmp /etc/dnf/dnf.conf \
    && dnf5 install -y --nogpgcheck --setopt=install_weak_deps=0 \
         dnf5-plugins rsync fedora-gpg-keys \
    && dnf5 config-manager setopt keepcache=1 install_weak_deps=0
   ```

   (The `COPY` must precede the `RUN` because the RUN has no ctx mount. The files are also
   re-copied idempotently by `10-overlay.sh`.)

4. **Optional: mimic utah's `ARG PACKAGE_IMAGE`/bind-mount** only if pluto later grows its
   own RPM factory; not needed for niri/DMS today.

### 9.2 `build/00-image-info.sh`

No structural change, but **`FEDORA_MAJOR_VERSION` must be non-empty and correct**. The
script prefers an explicit value and otherwise parses `os-release` `VERSION_ID`
(`pluto:build/00-image-info.sh:38-41`). On Hummingbird that parse yields the hum build
number. The Containerfile ARG from 9.1 is sufficient; optionally harden the script:

```bash
# Hummingbird's VERSION_ID is a hum build number, not a Fedora release; the ARG wins.
: "${FEDORA_MAJOR_VERSION:?set FEDORA_MAJOR_VERSION in the Containerfile}"
```

Also expect `base-image-name` to read `bootc-os` and the `just build` version string to
read `latest.<date>` / `stable-latest.<date>` because the base tag is `latest`. Cosmetic;
change only if a nicer version is wanted.

### 9.3 `build/10-overlay.sh` (config files; no packages)

The finpilot seams map cleanly onto the archived pluto's:

- **`custom/config/` → `/etc/skel/.config/`** (`pluto:build/10-overlay.sh:71-74`). So the
  archived `custom/config/.config/...` tree must be re-rooted one level shallower:
  | Archived path | New path |
  |---|---|
  | `custom/config/.config/niri/config.kdl` | `custom/config/niri/config.kdl` |
  | `custom/config/.config/niri/dms/*.kdl` | `custom/config/niri/dms/*.kdl` |
  | `custom/config/.config/environment.d/90-dms.conf` | `custom/config/environment.d/90-dms.conf` |
  | `custom/config/.config/ghostty/{config,themes/dankcolors}` | `custom/config/ghostty/{config,themes/dankcolors}` |
- **`custom/files/` → `/`** (`pluto:build/10-overlay.sh:61`). Re-home the archived system
  files unchanged: `etc/greetd/config.toml`, `etc/greetd/niri/config.kdl`,
  `usr/lib/systemd/user-preset/90-pluto-dms.preset`,
  `usr/lib/systemd/system/flatpak-theming.service`,
  `usr/share/glib-2.0/schemas/zz0-pluto-theme.gschema.override`,
  `usr/lib/sysusers.d/docker.conf` (only if docker is kept),
  `usr/share/xdg-terminal-exec/xdg-terminals.list`, and the ublue setup hooks.
- The template overlays only `common/shared`, not `common/bluefin`
  (`pluto:build/10-overlay.sh:43`). Utah copies **both** `shared` and `bluefin`
  (`utah:Containerfile:76-77`). For pluto's Bazaar/flatpak/branding behaviour, decide
  whether to add the `bluefin` profile the way the archived `10-build.sh` cherry-picked it
  (`pluto-archive:build/10-build.sh:18-41`).
- `10-overlay.sh` enables `flatpak-preinstall.service`; Hummingbird has **no flatpak**, so
  flatpak must be installed in the package phase before this matters (it does not fail at
  enable time; it fails at first boot). Keep the existing enable lines or move them after
  the package phase.
- Add `systemctl enable greetd.service` and `systemctl set-default graphical.target`; the
  niri/session wiring is desktop-specific and belongs in the new phase below.

### 9.4 `build/20-packages-and-services.sh` (packages + COPRs)

Extend the existing script. Concrete package set (from `pluto-archive:build/packages/niri.toml`
and `base.toml`). The `[parity]`-style foundation is required because Hummingbird ships no
fonts, mesa, audio, firmware or NetworkManager-wifi:

```bash
# Foundation Hummingbird does not ship (archived base.toml, trimmed to what niri/DMS needs)
dnf5 install -y \
  pipewire wireplumber pipewire-pulseaudio pavucontrol alsa-lib \
  mesa-dri-drivers mesa-vulkan-drivers vulkan-loader \
  fontconfig default-fonts adwaita-fonts-all \
  google-noto-color-emoji-fonts google-noto-emoji-fonts google-noto-sans-cjk-vf-fonts \
  xdg-desktop-portal xdg-desktop-portal-gnome xdg-desktop-portal-gtk \
  xdg-user-dirs xdg-utils xdg-terminal-exec \
  gnome-keyring gnome-keyring-pam polkit-kde \
  NetworkManager-wifi wpa_supplicant firewall-config \
  flatpak dconf glib-networking nautilus cava qt6-qtmultimedia \
  langpacks-en git git-credential-libsecret jq rsync fastfetch \
  greetd greetd-selinux \
  power-profiles-daemon zram-generator \
  adw-gtk3-theme libappindicator-gtk3 libayatana-appindicator-gtk3 \
  wl-clipboard nautilus

# niri stack (all Fedora)
dnf5 install -y \
  niri xwayland-satellite kanshi wtype wl-mirror wev \
  udiskie gnome-disk-utility accountsservice nm-connection-editor \
  qt6ct kf6-kimageformats plasma-breeze kf6-kirigami kf6-qqc2-desktop-style khal
```

COPRs (the archived `niri.toml`/`base.toml` coordinates):

| COPR | Packages |
|---|---|
| `avengemedia/dms` | `dms`, `dms-cli` |
| `avengemedia/danklinux` | `dms-greeter`, `quickshell-git`, `matugen`, `danksearch`, `dgop`, `material-symbols-fonts` |
| `scottames/ghostty` | `ghostty` |
| `ublue-os/packages` | `uupd` (already installed here), `oversteer-udev` |

**Three blockers to fix in `build/copr-helpers.sh` (all verified against the source):**

1. **Wrong chroot.** `dnf5 copr enable "$copr_name"` on Hummingbird auto-detects the
   chroot from `os-release`, producing `hummingbird-<build>`, which COPR rejects
   (`pluto-archive:build/scripts/package-lib.sh:9-16`). The finpilot helper omits the
   chroot argument (`pluto:build/copr-helpers.sh:25-27`). Fix:

   ```bash
   copr_chroot() {
       printf 'fedora-%s-%s' "$(cat /etc/dnf/vars/releasever)" "$(uname -m)"
   }
   ...
   dnf5 -y copr enable "$copr_name" "$(copr_chroot)"
   ```

2. **coprdep handling.** `avengemedia/dms` automatically enables `avengemedia/danklinux`
   (`pluto-archive:docs/research/copr-inventory.md:9-15`). The isolated helper disables the
   COPR before installing with `--enablerepo=<one repo id>`; a package whose dependency
   lives in the coprdep may then fail to resolve. Either enable all COPRs first and install
   in one transaction (the archived `install_copr_sections`,
   `pluto-archive:build/scripts/package-lib.sh:40-77`), or make the helper accept multiple
   repo ids and keep both `avengemedia` repos enabled for the DMS transaction. Repo ids:

   ```
   copr:copr.fedorainfracloud.org:avengemedia:dms
   copr:copr.fedorainfracloud.org:avengemedia:danklinux
   copr:copr.fedorainfracloud.org:scottames:ghostty
   copr:copr.fedorainfracloud.org:ublue-os:packages
   ```

3. **Assert the packages landed.** The archived helper asserts presence and, for
   multimedia/docker, RPM vendor. Copy that gate so a wrong COPR name fails the build
   (`pluto-archive:build/scripts/package-lib.sh:79-115`).

### 9.5 New phase `build/30-niri.sh` (or fold into `20`)

The session wiring does not belong in the package layer's cache story, and it must run
after the COPR packages exist. Mirror the archived `40-niri.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

# greetd is the display manager; dms-greeter runs niri as the greeter compositor.
systemctl enable greetd.service
systemctl set-default graphical.target
# Prefer the display-manager alias check (tunaOS forces it; utah enables gdm directly).
systemctl is-enabled greetd.service

# DMS autostart via user preset shipped in custom/files (enable dms.service).
systemctl --global preset-all 2>/dev/null || true

# Compile the shipped theme override (DMS/matugen owns runtime theming).
glib-compile-schemas /usr/share/glib-2.0/schemas
```

Add to the Containerfile a `RUN` block after `20-packages-and-services.sh` and before
`90-cleanup.sh`, copying the block from `build/README.md`.

Also reproduce the utah `configure-services.sh` policy items that apply to a
Hummingbird-based desktop:

- enable `bootc-unified-storage.service`, `fwupd-refresh.timer`, `tailscaled.service`;
- **mask `bootc-fetch-apply-updates.service` and `.timer` in `/usr/lib/systemd/system/`
  and `/etc/systemd/system/`** so uupd owns updates and a cross-vendor `/etc` merge cannot
  resurrect the timer (`utah:scripts/configure-services.sh:88-98`);
- patch logind's lid/sleep defaults (`utah:scripts/configure-services.sh:9-15`);
- remove build-only tooling before cleanup, as utah does with its extension toolchain.

### 9.6 `build/90-cleanup.sh`

The finpilot cleanup already disables every COPR (`_copr:*`, `_copr_*`), rpmfusion, and
the named third-party repos (`pluto:build/90-cleanup.sh:31-45`) and prunes `/var`, `/run`,
`/tmp`, `/boot`. No change is strictly required. Two decisions:

- **Fedora repos:** the archived pluto shipped `fedora.repo`/`fedora-updates.repo`
  enabled at runtime (priority 99); utah never enables Fedora at runtime. If pluto chooses
  the utah model, add `fedora` and `updates` to the disabled list in `90-cleanup.sh` —
  but be aware that any runtime `dnf` (there should be none; updates go through uupd/bootc)
  would then have no source.
- **negativo17 multimedia:** if the multimedia layer is ported, it was intentionally left
  enabled; `90-cleanup.sh` currently disables `fedora-multimedia.repo` (line 34), which
  would contradict that. Pick one and document it.
- Keep the `bootc container lint --fatal-warnings` line at the end of the Containerfile.

### 9.7 Tests and skills

- Add contract tests under `tests/template/` for the new phase (e.g. that
  `/etc/greetd/config.toml` ships and that `dms.service` is in a user preset), following
  `tests/template/90-cleanup_test.bats`.
- Per `AGENTS.md`, update the owning skill (`.agents/skills/build/SKILL.md`,
  `.agents/skills/customize/SKILL.md`) with the swapped base and the Hummingbird quirks in
  the same change.
- Do not commit anything before `just lint`, `just check`, `just test-unit`.

---

## 10. Uncertainties and human decisions

1. **niri/DMS sourcing.** Both are absent from utah-packages, so pluto will ship build-time
   Fedora + third-party COPRs — a deliberate divergence from utah's "no Fedora at runtime"
   and "OCIR factory only" model. The alternative (a `pluto-packages` factory that builds
   niri/DMS/quickshell for Hummingbird) is a much larger project. **Decision: COPRs now, or
   invest in a factory?**
2. **DMS channel:** stable `avengemedia/dms` vs `avengemedia/dms-git`; and `quickshell`
   vs `quickshell-git`. The archived pluto chose stable dms + `quickshell-git`. Both
   chroots exist; `dms` picks the highest EVR by default (`quickshell-git`).
   (`pluto-archive:docs/research/copr-inventory.md:223`)
3. **Vendor/identity:** `IMAGE_VENDOR` is `projectbluefin` in the current Containerfile but
   the archived pluto used `siddhj2206`, and the README/Justfile/artifacthub literals must
   agree. `BASE_IMAGE_NAME` will be `bootc-os` (derived from FROM) rather than the archived
   `hummingbird`. **Confirm the intended vendor.**
4. **`common` profile:** overlay only `shared` (current template) or add `bluefin` (utah)?
   Affects Bazaar, flatpaks, branding, and dconf defaults.
5. **Component set:** pluto may want a minimal set (drop DX/docker/libvirt, YubiKey,
   multimedia negativo17, firmware vendor splits). The archived `base.toml` is not minimal.
   **Decide what "only niri and DMS" means for the supporting stack.**
6. **Fedora repos at runtime:** enabled (archived pluto) or disabled (utah). Affects
   `90-cleanup.sh`.
7. **Base digest freshness:** the live index digest differs from utah's pin; re-verify at
   write time and let Renovate own it thereafter.
8. **Version string:** `just build` will produce `latest.<date>` because the base tag is
   `latest`. Cosmetic, but it leaks into `os-release`/labels.
9. **`00-image-info.sh` Fedora major:** confirm `FEDORA_MAJOR_VERSION` reaches the script
   (ARG above the RUN) and does not silently fall back to the hum build number.
10. **`build/60-desktop-swap.sh.example`** is the template's own desktop-replacement seam;
    the transplant could either replace it (delete the GNOME base altogether) or become a
    new numbered phase. The report assumes a new `30-niri.sh` + extended `20`.

---

## Sources

### Repositories and commands

- `gh api repos/projectbluefin/utah --jq '{full_name,description,default_branch,visibility,archived}'`
- `gh api repos/projectbluefin/utah-packages --jq '{full_name,description,default_branch,visibility,archived}'`
- `gh search repos utah --owner projectbluefin --json name,description,visibility,updatedAt`
- `git clone --depth 1 https://github.com/projectbluefin/utah` → `/tmp/opencode/utah-research/utah` (`a567795`)
- `git clone --depth 1 https://github.com/projectbluefin/utah-packages` → `/tmp/opencode/utah-research/utah-packages` (`e2c4f43`)
- `git -C /var/home/sid/Documents/Projects/pluto archive origin/archive/pluto-pre-rewrite | tar -x -C /tmp/opencode/pluto-archive` (`18d57a0`)
- `skopeo inspect docker://quay.io/hummingbird-community/bootc-os:latest` → `sha256:9d69f6f33f5af87c76b0d7f49387bc4b969271a8eb788970396d6eab2b5af8a2`
- `skopeo inspect --raw docker://quay.io/hummingbird-community/bootc-os:latest` (per-arch digests; amd64 `sha256:f06fdf8d…`)

### `projectbluefin/utah` files cited

- `Containerfile`, `Containerfile.kernel`, `Justfile`, `README.md`, `renovate.json`
- `packages/utah.toml`, `packages/bluefin.toml`, `packages/hummingbird.repo`,
  `packages/utah-packages.repo`, `packages/fedora-44.repo`
- `scripts/install-packages.py`, `scripts/configure-services.sh`, `scripts/clean-stage.sh`
- `config/flavors.json`, `contracts/bluefin-desktop.toml`
- `.github/workflows/build.yml`

### `projectbluefin/utah-packages` files cited

- `README.md`, `Justfile`, `.packit.yaml`, `AGENTS.md`
- `docs/architecture.md`, `docs/targeting-hummingbird.md`
- `config/hummingbird.repo`, `config/runtime-contract.toml`, `config/bluefin-packages.toml`,
  `config/factory-contract.json`
- `packages/gnome-shell/gnome-shell.spec` (sampled), `packages/` (352 recipe directories)
- `.github/workflows/rebuild-rpms.yml`, `.github/workflows/build-stage.yml`

### Archived pluto files cited

- `Containerfile`, `AGENTS.md`, `docs/fedora-hummingbird-rebase.md`
- `build/packages/{base,firmware,multimedia,niri,dx}.toml`
- `build/{10-build,20-base,25-multimedia,40-niri,45-dx}.sh`, `build/scripts/{package-lib.sh,read-packages}`,
  `build/clean-stage.sh`
- `custom/files/etc/greetd/config.toml`, `custom/files/etc/greetd/niri/config.kdl`,
  `custom/files/etc/yum.repos.d/{fedora,fedora-updates}.repo`,
  `custom/files/usr/lib/systemd/user-preset/90-pluto-dms.preset`,
  `custom/files/usr/lib/systemd/system/flatpak-theming.service`,
  `custom/files/usr/share/glib-2.0/schemas/zz0-pluto-theme.gschema.override`,
  `custom/files/usr/share/ublue-os/{privileged,user}-setup.hooks.d/*`
- `custom/config/.config/niri/config.kdl`, `custom/config/.config/niri/dms/*.kdl`,
  `custom/config/.config/environment.d/90-dms.conf`
- `docs/research/niri-packages.md`, `docs/research/niri-layer-design.md`,
  `docs/research/copr-inventory.md`

### Current pluto (finpilot layout) files cited

- `Containerfile`, `Justfile`, `.github/renovate.json`
- `build/00-image-info.sh`, `build/10-overlay.sh`, `build/20-packages-and-services.sh`,
  `build/90-cleanup.sh`, `build/copr-helpers.sh`, `build/README.md`,
  `build/60-desktop-swap.sh.example`, `build/30-tailscale.sh.example`
- `custom/config/README.md`, `custom/files/README.md`, `custom/flatpaks/default.preinstall`
- `.agents/skills/build/SKILL.md`, `tests/contract/`, `tests/template/`

### Upstream references (for the desktop stack)

- niri: https://github.com/YaLTeR/niri
- DankMaterialShell: https://github.com/AvengeMedia/DankMaterialShell
- dank-greeter: https://github.com/AvengeMedia/dank-greeter
- DMS docs: https://danklinux.com/docs/dankmaterialshell/installation
- COPRs: `https://copr.fedorainfracloud.org/coprs/avengemedia/dms`,
  `.../avengemedia/danklinux`, `.../scottames/ghostty`, `.../ublue-os/packages`
- Hummingbird packages: https://packages.redhat.com
- Reference niri+DMS bootc distro: https://github.com/tuna-os/tunaOS
  (`manifests/desktops/niri.yaml`, `build_scripts/desktop/niri.sh`)
