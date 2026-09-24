# Package inventory — what utah builds, and what pluto must own

**Date:** 2026-09-23
**Author:** research subagent (DeepSeek V4.1 Flash via OpenCode)
**Scope:** (A) every RPM recipe `projectbluefin/utah-packages` builds, and
(B) the forward-looking list of packages pluto will need to build or source in
its own Hummingbird-based, niri + DankMaterialShell (DMS) image.

**Prior reports this builds on (read first):**
`docs/research/utah-hummingbird-niri-dms.md`, `docs/research/packit-monorepo-packages.md`,
`docs/research/kestrel-monorepo.md`. This report does not restate them; it adds a
complete inventory and re-verifies their package claims against live sources.

**Method.** Cloned `projectbluefin/utah-packages@e2c4f43` (`main`, 2026-09) and the
archived pluto tree (`origin/archive/pluto-pre-rewrite`, `18d57a0`) into
`/tmp/opencode/pkg-inventory/`. Parsed the root `.packit.yaml`, every
`packages/*/*.spec`, `config/upstream-sources.json`, and every
`.hummingbird-upstream.json`. Verified Fedora 44 availability with the host's
`dnf5 repoquery` (host is Fedora 44). Verified current COPR package sets and
versions through the COPR `api_3` package/build endpoints, and verified the
Hummingbird base repo by parsing its Pulp `primary.xml` (3,515 binary names).
Exact commands and URLs are in [Sources](#sources).

---

## TL;DR

1. **utah-packages builds 352 RPM recipes**, all GNOME-51-and-desktop-stack
   packages, all rebuilt against a Fedora 44 + Hummingbird buildroot and
   published as the OCI repo image `ghcr.io/projectbluefin/utah-packages`. The
   docs still say 193/345 recipes; the measured tree is 352 on every count
   (directories, `.packit.yaml` entries, `upstream-sources.json` entries,
   `.hummingbird-upstream.json` provenance files).
2. **Packit is SRPM validation only.** `.packit.yaml` has **no `jobs:` section**;
   `packit srpm` runs manually in a pinned image and feeds nothing. The binary
   lane is bare `rpmbuild` in `quay.io/fedora/fedora:44` with the Hummingbird
   Pulp overlay, staged in five dependency waves.
3. **Hummingbird carries no desktop:** 3,515 packages, and only four of utah's
   352 base names (`git`, `icu`, `protobuf`, `re2`) exist there. That is the
   whole reason the factory exists.
4. **For pluto, most of the niri/DMS stack is now in Fedora 44** — including
   `niri`, `xwayland-satellite`, `greetd`, `quickshell`, `matugen`, `danksearch`,
   `dgop`, `cliphist`, `qt6ct`, `kanshi`. Only `dms`, `dms-cli`, `dms-greeter`,
   `quickshell-git`, `material-symbols-fonts`, `dankcalendar`, `ghostty`,
   `uupd`/`oversteer`, `breakpad`, `cpptrace`, `cli11`, `qt6ct-kde` are absent
   from Fedora 44. Several Fedora versions lag the COPR badly (quickshell 0.2.1
   vs 0.3.x; matugen 3.1 vs 4.2; danksearch 0.1.2 vs 1.6.0; dgop 0.2.1 vs 1.6.2).
5. **Recommended first slice: own `dms` + `dms-cli`** (one MIT recipe, two
   subpackages, leaf, Go + QML) in `ghcr.io/siddhj2206/pluto-packages`, then
   `dms-greeter`, then the `quickshell`/`dgop`/`matugen`/`danksearch`/
   `material-symbols-fonts` set. `niri`, `xwayland-satellite` and the whole
   desktop foundation stay on Fedora 44 at build time; own niri only for currency.
   The factory scaffold already exists in this repo (`bd66585`:
   `packages/config/factory-contract.json` pins registry
   `ghcr.io/siddhj2206/pluto-packages`, suffix `.hum1.pluto`, buildroot
   `quay.io/fedora/fedora:44` + Hummingbird overlay), so slice 1 is "add the
   first recipe to the existing scaffold", not "build the factory".
6. **Biggest unknowns:** whether Fedora 44's `quickshell` 0.2.1 and `dgop` 0.2.1
   satisfy DMS 1.6.2's private-API/version expectations (if not, they move into
   slice 1); the redistribution status of the `avengemedia`/`danklinux` specs
   (MIT repo, but confirm before importing); and the Hummingbird-vs-Fedora ABI
   question for any shared library (Qt6, libEGL) quickshell links.

---

# Part A — everything `projectbluefin/utah-packages` builds

## A.1 What the factory is and how it publishes

`projectbluefin/utah-packages` is a GitHub-Actions RPM factory — *"GitHub
Actions' replacement for Copr"* (`docs/architecture.md`). It pairs with
`projectbluefin/utah`, which composes the bootc image; the seam between them is
a digest-pinned `COPY --from` of the factory's `createrepo_c` output
(`README.md`). `main` publishes `:latest`; other branches publish under their
own name; the image is signed keylessly with cosign on GitHub OIDC.

- **Disttag:** `.hum1.bfin` (vendor release + `hum1` + factory suffix), e.g.
  `gnome-shell-51.beta-…-3.hum1.bfin` (`README.md:52-55`).
- **Build root:** Fedora 44 plus the Hummingbird Pulp overlay
  (`config/hummingbird.repo`, `gpgcheck=0`, `priority=10`,
  `excludepkgs=ruby3.3-default-gems,ruby3.4-default-gems`).
- **Binary lane:** `.github/workflows/rebuild-rpms.yml` → five staged waves
  (`build-stage.yml`, stages 0–4), raw `rpmbuild -br`/`-ba` in
  `quay.io/fedora/fedora:44`. A `precedence` job checks each RPM outranks what
  Fedora 44/Hummingbird offer.
- **Inputs:** Fedora dist-git `rawhide` seeds the spec/patches; the *payload* is
  fetched from upstream and SHA-512-verified by `tools/source_pipeline.py`,
  failing closed. A recipe with no `upstream-sources.json` entry cannot build.
- **Incremental:** the published OCI image is also the factory's memory;
  `prepare` skips recipes already published at the same version/release and
  drags along dependents.

## A.2 Packit's actual role (verified)

- Root `.packit.yaml` is generated (`tools/render_packit_config.py`), has a
  `packages:` map and one `create-archive` action, and **zero `jobs:` entries**
  (`grep -cE '^\s*jobs:' .packit.yaml` → `0`). Packit-as-a-Service therefore does
  nothing on this repo.
- The only Packit use is `packit srpm --preserve-spec` in
  `packit-srpm-pilot.yml` / `packit-srpm-chunk.yml`: **manual dispatch,
  verification-only, output not published**. `docs/architecture.md` states
  Packit "does not do source acquisition either" and that Copr/Koji/Bodhi/
  Testing Farm/self-hosted runners "are not dependencies".
- Conclusion: utah's working config is `config/upstream-sources.json` +
  `config/runtime-contract.toml` + the workflow YAML, **not** `.packit.yaml`.

## A.3 Why utah builds each package

Three distinct reasons, all visible in `README.md` and `config/runtime-contract.toml`:

1. **Hummingbird has no desktop.** Hummingbird is an overlay (~3,515 packages:
   base OS only, no GNOME, no mesa, no pipewire, no portals). The desktop
   closure must come from somewhere.
2. **ABI coherence with the Hummingbird buildroot.** The factory exists to
   rebuild against Hummingbird "so that sonames match the image it feeds"
   (`docs/architecture.md`). A library that another rebuilt package links
   against cannot come from Fedora unchanged. This is why all 352 recipes exist,
   not just the handful Fedora lacks.
3. **Specific gaps Fedora/Hummingbird cannot fill**, each issue-tracked in
   `runtime-contract.toml`: `wpa_supplicant`/`wireless-regdb`/`iw` (Hummingbird
   ships `NetworkManager-wifi` but neither supplicant nor regulatory DB),
   `fprintd`, `libdaemon` (avahi links it), `libgexiv2`/`libportal` (nautilus
   deps in no repo), plus GNOME 51 itself (a version Fedora does not ship yet).

The categories below are mine, not utah's; they are a reading aid. The version,
build system and source-kind columns are parsed from each spec and
`config/upstream-sources.json`.

## A.4 Master table — all 352 recipes

Legend for **Source**: *Fedora lookaside* = upstream tarball URL mirrored under
`src.fedoraproject.org/repo/pkgs/…`; *direct* = upstream release tarball;
*tag archive* = upstream git release-tag tarball; *commit* = pinned to a git
commit/SHA; *crates.io* = crates.io crate; *source files* = no tarball, spec
sources are loose files.

### GNOME 51 shell & session (34)

| Package | Version | Build | Source |
|---|---|---|---|
| `at-spi2-core` | 2.61.1 | meson,python | upstream tarball (Fedora lookaside mirror) |
| `dconf` | 51~beta | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `evolution-data-server` | 3.61.3 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `gcr` | 4.4.0.1 | meson | upstream tarball (Fedora lookaside mirror) |
| `gcr3` | 3.41.1 | meson | upstream tarball (Fedora lookaside mirror) |
| `gdm` | 51~beta | meson | upstream release tarball |
| `geoclue2` | 2.8.2 | make,meson | upstream tarball (Fedora lookaside mirror) |
| `geocode-glib` | 3.26.4 | meson | upstream tarball (Fedora lookaside mirror) |
| `gjs` | 1.89.2 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `gnome-autoar` | 0.5.0 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `gnome-bluetooth` | 47.2 | meson | upstream tarball (Fedora lookaside mirror) |
| `gnome-control-center` | 51~beta | c/c++,meson | upstream release tarball |
| `gnome-desktop3` | 51~alpha | c/c++,meson | upstream release tarball |
| `gnome-keyring` | 50.0 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `gnome-online-accounts` | 3.58.1 | meson | upstream tarball (Fedora lookaside mirror) |
| `gnome-ponytail-daemon` | 0.0.11 | c/c++,meson | git archive (release tag) |
| `gnome-session` | 51~beta | c/c++,make,meson | upstream release tarball |
| `gnome-settings-daemon` | 51~beta | c/c++,meson | upstream release tarball |
| `gnome-shell` | 51~beta | c/c++,meson | upstream release tarball |
| `gnome-shell-extension-gsconnect` | 72 | c/c++,meson | git archive (release tag) |
| `gnome-tweaks` | 49.0 | meson,python | upstream release tarball |
| `gsettings-desktop-schemas` | 51~beta | meson | upstream release tarball |
| `gvfs` | 1.61.91 | c/c++,meson | upstream release tarball |
| `gweather-locations` | 2026.2 | meson | upstream tarball (Fedora lookaside mirror) |
| `libgda` | 6.0.0 | c/c++,meson | upstream release tarball |
| `libgtop2` | 2.41.3 | make | upstream tarball (Fedora lookaside mirror) |
| `libgweather` | 4.6.0 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libsecret` | 0.21.7 | meson,python | upstream tarball (Fedora lookaside mirror) |
| `localsearch` | 3.12~beta | autotools,c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `malcontent` | 0.14.0 | c/c++,cmake,meson | upstream release tarball |
| `malcontent-bootstrap` | 0.14.0 | c/c++,cmake,meson | upstream release tarball |
| `mutter` | 51~beta | make,meson | upstream release tarball |
| `nautilus-python` | 4.1.0 | c/c++,meson,python | upstream tarball (Fedora lookaside mirror) |
| `tecla` | 51~beta | c/c++,meson | upstream tarball (Fedora lookaside mirror) |

### GTK / Adwaita / graphics stack (49)

| Package | Version | Build | Source |
|---|---|---|---|
| `adwaita-icon-theme` | 51~beta | meson | upstream tarball (Fedora lookaside mirror) |
| `adwaita-icon-theme-legacy` | 46.2 | meson | upstream tarball (Fedora lookaside mirror) |
| `cairo` | 1.18.4 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `gdk-pixbuf2` | 2.44.8 | meson | upstream tarball (Fedora lookaside mirror) |
| `glycin` | 2.2~beta | meson,rust | upstream tarball (Fedora lookaside mirror) |
| `graphene` | 1.10.8 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `gtk2` | 2.24.33 | autotools,c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `gtk3` | 3.24.52 | make,meson | upstream tarball (Fedora lookaside mirror) |
| `gtk4` | 4.23.3 | c/c++,make,meson | upstream release tarball |
| `igt-gpu-tools` | 2.5 | c/c++,meson | git archive (release tag) |
| `intel-gmmlib` | 22.10.1 | c/c++,cmake,make | git archive (release tag) |
| `intel-media-driver-free` | 26.2.4 | c/c++,cmake,make,python | source file(s) only, no tarball |
| `intel-mediasdk` | 23.2.2 | c/c++,cmake,make | git archive (release tag) |
| `intel-vpl-gpu-rt` | 26.1.6 | c/c++,cmake,make | git archive (release tag) |
| `lcms2` | 2.16 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libadwaita` | 1.10~beta.1 | c/c++,meson | upstream release tarball |
| `libcloudproviders` | 0.4.1 | c/c++,meson | upstream release tarball |
| `libdecor` | 0.2.5 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libdisplay-info` | 0.3.0 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libdrm` | 2.4.134 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libei` | 1.6.0 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libepoxy` | 1.5.10 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `libglvnd` | 1.7.0 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libinput` | 1.31.3 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libpciaccess` | 0.19 | autotools,c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libva` | 2.24.1 | c/c++,meson | git archive (release tag) |
| `libva-utils` | 2.24.0 | c/c++,meson | git archive (release tag) |
| `libvdpau` | 1.5 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libvpl` | 2.17.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `libxcvt` | 0.1.2 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libxshmfence` | 1.3.2 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `mesa` | 26.2.1 | c/c++,meson,python,rust | upstream tarball (Fedora lookaside mirror) |
| `mesa-demos` | 9.0.0 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `mesa-libGLU` | 9.0.3 | autotools,c/c++,meson | upstream release tarball |
| `mtdev` | 1.1.6 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `pango` | 1.58.2 | c/c++,meson | upstream release tarball |
| `pixman` | 0.46.4 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `spirv-tools` | 2026.3 | c/c++,cmake,make,python | upstream tarball (Fedora lookaside mirror) |
| `startup-notification` | 0.12 | c/c++,make | upstream release tarball |
| `vulkan-headers` | 1.4.350.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `vulkan-loader` | 1.4.350.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `wayland` | 1.26.0 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `wayland-protocols` | 1.49 | c/c++,meson | upstream release tarball |
| `xcb-util` | 0.4.1 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xcb-util-image` | 0.4.1 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xcb-util-keysyms` | 0.4.1 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xcb-util-renderutil` | 0.3.10 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xcb-util-wm` | 0.4.2 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xkbcomp` | 1.5.0 | c/c++,make | upstream tarball (Fedora lookaside mirror) |

### X11 / Xwayland libraries (20)

| Package | Version | Build | Source |
|---|---|---|---|
| `libICE` | 1.1.2 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libSM` | 1.2.5 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXcursor` | 1.2.3 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXdamage` | 1.1.6 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXdmcp` | 1.1.5 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXfixes` | 6.0.1 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXfont2` | 2.0.9 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXinerama` | 1.1.5 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXmu` | 1.2.1 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXrandr` | 1.5.4 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXt` | 1.3.1 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXv` | 1.0.13 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libXxf86vm` | 1.1.6 | autotools,c/c++,make | git archive (release tag) |
| `libxkbfile` | 1.1.3 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xdg-desktop-portal` | 1.22.1 | c/c++,meson | upstream release tarball |
| `xdg-desktop-portal-gnome` | 51~alpha | c/c++,meson | upstream release tarball |
| `xdg-desktop-portal-gtk` | 1.15.3 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `xorg-x11-server-Xwayland` | 26.0.99.901 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `xorg-x11-xauth` | 1.1.5 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xorg-x11-xinit` | 1.4.3 | c/c++,make | upstream tarball (Fedora lookaside mirror) |

### Audio (29)

| Package | Version | Build | Source |
|---|---|---|---|
| `alsa-firmware` | 1.2.4 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `alsa-sof-firmware` | %{sof_ver} | make | upstream tarball (Fedora lookaside mirror) |
| `alsa-tools` | 1.2.15 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `alsa-utils` | %{baseversion}%{?fixversion} | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `codec2` | 1.2.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `flac` | 1.5.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `gsound` | 1.0.3 | meson | upstream tarball (Fedora lookaside mirror) |
| `ilbc` | 3.0.4 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `lame` | 4.0 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libcanberra` | 0.30 | c/c++,make | upstream release tarball |
| `libebur128` | 1.2.6 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `libfreeaptx` | 0.2.2 | c/c++,make | git archive (release tag) |
| `liblc3` | 1.1.3 | c/c++,meson,python | upstream tarball (Fedora lookaside mirror) |
| `liblc3plus` | 1.7.1 | c/c++,make | git archive (release tag) |
| `libldac` | %{sonamebase}.0.2.6 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `libogg` | 1.3.6 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `libvorbis` | 1.3.7 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `lpcnetfreedv` | 0.5 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `mpg123` | 1.33.6 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `opus` | 1.6 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `pipewire` | %{majorversion}.%{minorversion}.%{microversion} | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `pipewire-libs-extra` | 1.6.8 | c/c++,meson | git archive (release tag) |
| `pulseaudio` | %{pa_major}%{?pa_minor:.%{pa_minor}} | autotools,c/c++,cmake,make,meson,qt | upstream tarball (Fedora lookaside mirror) |
| `sbc` | 2.1 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `soxr` | 0.1.3 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `twolame` | 0.4.0 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `wavpack` | 5.9.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `webrtc-audio-processing` | 2.1 | c/c++,meson | upstream release tarball |
| `wireplumber` | 0.5.14 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |

### Multimedia / codecs (28)

| Package | Version | Build | Source |
|---|---|---|---|
| `aribb24` | 1.0.3%{!?tag:^%{date}git%{shortcommit0}} | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `fdk-aac-free` | 2.0.3 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `ffmpeg` | 9.0.1 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `gstreamer1` | 1.28.6 | autotools,c/c++,cmake,make,meson | upstream tarball (Fedora lookaside mirror) |
| `gstreamer1-plugins-bad-free` | 1.28.6 | autotools,c/c++,make,meson,qt | upstream tarball (Fedora lookaside mirror) |
| `gstreamer1-plugins-base` | 1.28.6 | autotools,c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `gstreamer1-plugins-good` | 1.28.6 | autotools,c/c++,make,meson,qt | upstream tarball (Fedora lookaside mirror) |
| `jpegxl` | 0.11.2 | c/c++,cmake,make,python,qt | upstream tarball (Fedora lookaside mirror) |
| `libaribcaption` | 1.1.1 | c/c++,cmake | upstream tarball (Fedora lookaside mirror) |
| `libheif` | 1.23.1 | c/c++,cmake,make | git archive (release tag) |
| `libshout` | 2.4.6 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libsndfile` | 1.2.2 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libtheora` | 1.1.1 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libvisual` | 0.4.2 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libvpx` | 1.17.0 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `noopenh264` | 2.6.0 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `openapv` | 0.3.0.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `opencore-amr` | 0.1.6 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `openjpeg` | 2.5.4 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `orc` | 0.4.41 | autotools,c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `spandsp` | 0.0.6 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `speex` | 1.2.0 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `taglib` | 2.3 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `vo-amrwbenc` | 0.1.3 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xevd` | 0.7.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `xeve` | 0.7.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `xvidcore` | 1.3.7 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `zvbi` | 0.2.45 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |

### Fonts (13)

| Package | Version | Build | Source |
|---|---|---|---|
| `adwaita-fonts` | 51.0 | meson | upstream release tarball |
| `enchant2` | 2.8.19 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `fribidi` | 1.0.16 | autotools,c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `google-noto-color-emoji-fonts` | 2.051 | c/c++,make,python | git archive (commit-pinned) |
| `google-noto-sans-cjk-vf-fonts` | 2.004 | no-compile | upstream release tarball |
| `hicolor-icon-theme` | 0.18 | meson | upstream tarball (Fedora lookaside mirror) |
| `hunspell` | 1.7.3 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `hunspell-en` | 0.%{upstreamid} | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `hyphen` | 2.8.8 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `iso-codes` | 4.20.1 | meson | upstream tarball (Fedora lookaside mirror) |
| `shared-mime-info` | 2.5.1 | c/c++,meson | git archive (release tag) |
| `snowball` | 3.1.1 | c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `stix-fonts` | 2.13b171 | make | git archive (release tag) |

### Firmware & hardware (30)

| Package | Version | Build | Source |
|---|---|---|---|
| `bluez` | 5.87 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `ddcutil` | 2.2.1 | autotools,c/c++,cmake,make | git archive (release tag) |
| `evtest` | 1.36 | autotools,c/c++,make | git archive (release tag) |
| `fprintd` | 1.94.5 | c/c++,meson | git archive (release tag) |
| `fxload` | 2008_10_13 | c/c++,make | upstream release tarball |
| `hidapi` | 0.15.0 | c/c++,cmake | upstream tarball (Fedora lookaside mirror) |
| `hwdata` | 0.410 | make | upstream tarball (Fedora lookaside mirror) |
| `hwinfo` | 25.5 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `i2c-tools` | 4.4 | c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `iio-sensor-proxy` | 3.9 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `iw` | 6.17 | c/c++ | upstream release tarball |
| `libatasmart` | 0.19 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libcamera` | 0.7.2 | c/c++,meson,python,qt | git archive (release tag) |
| `libfprint` | 1.94.100 | c/c++,meson | git archive (release tag) |
| `libgudev` | 238 | meson | upstream tarball (Fedora lookaside mirror) |
| `libimobiledevice` | 1.4.0 | c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `libimobiledevice-glue` | 1.3.2 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libmanette` | 0.2.13 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libplist` | 2.7.0 | c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `libratbag` | 0.18 | c/c++,meson,python | git archive (release tag) |
| `librsvg2` | 2.62.3 | c/c++,make,meson,rust | upstream tarball (Fedora lookaside mirror) |
| `libusbmuxd` | 2.1.1 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `linux-firmware` | 20260810 | make | upstream tarball (Fedora lookaside mirror) |
| `lm_sensors` | 3.6.0 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `microcode_ctl` | %{intel_ucode_version} | c/c++,make | git archive (release tag) |
| `ntfs-3g` | 2026.7.7 | c/c++,make | upstream release tarball |
| `openrgb` | 1.0~rc2 | c/c++,cmake,make,qt | git archive (commit-pinned) |
| `switcheroo-control` | 3.0 | c/c++,meson | upstream release tarball |
| `upower` | 1.91.3 | meson | upstream tarball (Fedora lookaside mirror) |
| `v4l-utils` | 1.32.0 | c/c++,make,meson,qt | upstream tarball (Fedora lookaside mirror) |

### Storage & filesystems (18)

| Package | Version | Build | Source |
|---|---|---|---|
| `cdparanoia` | 10.2 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `device-mapper-persistent-data` | 1.3.3 | c/c++,make,rust | upstream tarball (Fedora lookaside mirror) |
| `fuse` | 2.9.9 | autotools,c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `libburn` | 1.5.8 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libbytesize` | 2.12 | autotools,c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `libcdio` | 2.3.0 | c/c++,make | upstream release tarball |
| `libcdio-paranoia` | 10.2+2.0.2 | c/c++,make | upstream release tarball |
| `libisoburn` | 1.5.8 | autotools,c/c++,make | upstream release tarball |
| `libisofs` | 1.5.8 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libnfs` | 7.0.0 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libnvme` | 1.16.2 | c/c++,make,meson,python | upstream tarball (Fedora lookaside mirror) |
| `libtalloc` | 2.5.0 | c/c++,make,python | upstream release tarball |
| `libtdb` | 1.4.15 | c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `libtevent` | 0.17.2 | c/c++,make,python | upstream release tarball |
| `parted` | 3.7 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `samba` | %{samba_version} | c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `squashfs-tools` | 4.7.4 | c/c++,make | git archive (release tag) |
| `volume_key` | 0.3.12 | autotools,c/c++,make,python | upstream tarball (Fedora lookaside mirror) |

### Networking (17)

| Package | Version | Build | Source |
|---|---|---|---|
| `ModemManager` | 1.24.2 | make,meson | upstream tarball (Fedora lookaside mirror) |
| `gssdp` | 1.6.6 | meson | upstream tarball (Fedora lookaside mirror) |
| `gupnp` | 1.6.10 | meson | upstream tarball (Fedora lookaside mirror) |
| `gupnp-igd` | 1.6.0 | meson | upstream tarball (Fedora lookaside mirror) |
| `libnice` | 0.1.23 | autotools,c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `libnma` | 1.10.6 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `liboping` | 1.10.0 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libpcap` | 1.10.6 | c/c++,make | upstream release tarball |
| `libproxy` | 0.5.12 | c/c++,cmake,make,meson,python | upstream tarball (Fedora lookaside mirror) |
| `linux-atm` | 2.5.1 | c/c++,make | upstream release tarball |
| `mobile-broadband-provider-info` | 20240407 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `ppp` | 2.5.3 | autotools,c/c++,make | git archive (release tag) |
| `tailscale` | 1.98.8 | no-compile | upstream release tarball |
| `wireguard-tools` | 1.0.20260223 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `wireless-regdb` | 2026.09.03 | make | upstream release tarball |
| `wpa_supplicant` | 2.11 | c/c++,make,qt | upstream release tarball |
| `wsdd` | 0.8 | no-compile | upstream tarball (Fedora lookaside mirror) |

### Flatpak & containers (11)

| Package | Version | Build | Source |
|---|---|---|---|
| `containerd` | 2.3.4 | go | upstream tarball (Fedora lookaside mirror) |
| `distrobox` | 1.8.2.5 | no-compile | git archive (release tag) |
| `flatpak` | 1.19.0 | meson | upstream tarball (Fedora lookaside mirror) |
| `flatpak-xdg-utils` | 1.0.6 | c/c++,meson | upstream release tarball |
| `libportal` | 0.10.0 | c/c++,meson,qt | upstream release tarball |
| `python-dasbus` | 1.7 | make,python | upstream tarball (Fedora lookaside mirror) |
| `runc` | 1.5.1 | go | upstream tarball (Fedora lookaside mirror) |
| `xdg-dbus-proxy` | 0.1.8 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `xdg-terminal-exec` | 0.14.2 | make | git archive (release tag) |
| `xdg-user-dirs` | 0.20 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `xdg-user-dirs-gtk` | 0.16 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |

### CLI / build tooling & runtime (21)

| Package | Version | Build | Source |
|---|---|---|---|
| `fastfetch` | 2.66.0 | c/c++,cmake,make | git archive (release tag) |
| `fish` | %{version_base}%{?version_pre:~%{version_pre}}%{?gitnum:^git%{gitnum}.%{githashshort}} | c/c++,cmake,make,python,rust | upstream release tarball |
| `fzf` | 0.74.3 | go | upstream tarball (Fedora lookaside mirror) |
| `git` | 2.55.0 | c/c++,make,python | upstream release tarball |
| `gobject-introspection` | 1.86.0 | c/c++,meson,python | upstream tarball (Fedora lookaside mirror) |
| `gum` | 2.0.0 | go | git archive (release tag) |
| `libsass` | 3.6.6 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `pycairo` | 1.28.0 | c/c++,meson,python | upstream tarball (Fedora lookaside mirror) |
| `python-annotated-types` | 0.8.0 | python | upstream tarball (Fedora lookaside mirror) |
| `python-argcomplete` | 3.6.3 | make,python | upstream tarball (Fedora lookaside mirror) |
| `python-dbus-next` | 0.2.3 | python | upstream tarball (Fedora lookaside mirror) |
| `python-distro` | 1.9.0 | python | upstream tarball (Fedora lookaside mirror) |
| `python-evdev` | 1.9.3 | c/c++,python | upstream tarball (Fedora lookaside mirror) |
| `python-pam` | 2.0.2 | python | upstream tarball (Fedora lookaside mirror) |
| `python-psutil` | 7.2.2 | c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `python-pydantic` | 2.13.5 | python | upstream tarball (Fedora lookaside mirror) |
| `python-pydantic-core` | 2.46.5 | python,rust | upstream tarball (Fedora lookaside mirror) |
| `python-typing-inspection` | 0.4.4 | python | upstream tarball (Fedora lookaside mirror) |
| `rust-just` | 1.57.0 | rust | crates.io crate |
| `sassc` | 3.6.2 | autotools,c/c++,make | git archive (release tag) |
| `zsh` | 5.9.2 | c/c++,make | upstream release tarball |

### Boot / OS plumbing (7)

| Package | Version | Build | Source |
|---|---|---|---|
| `grub2` | 2.12 | autotools,c/c++,make | upstream release tarball |
| `iputils` | 20250605 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `isomd5sum` | 1.2.5 | c/c++,make,python | git archive (release tag) |
| `libx86emu` | 3.7 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `livesys-scripts` | 0.9.7 | make | upstream tarball (Fedora lookaside mirror) |
| `mdadm` | 4.6 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `setools` | 4.7.1 | c/c++,make,python,qt | upstream tarball (Fedora lookaside mirror) |

### Desktop services & system integration (11)

| Package | Version | Build | Source |
|---|---|---|---|
| `accountsservice` | 26.27.3 | meson | git archive (release tag) |
| `cups-pk-helper` | 0.2.7 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `desktop-file-utils` | 0.28 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `fwupd` | 2.1.7 | meson | upstream tarball (Fedora lookaside mirror) |
| `input-remapper` | 2.2.1 | make,python | git archive (release tag) |
| `nautilus` | 51~beta | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `passim` | 0.1.12 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `pciutils` | 3.15.0 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `rtkit` | 0.14 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `sound-theme-freedesktop` | 0.8 | c/c++ | upstream tarball (Fedora lookaside mirror) |
| `udisks2` | 2.11.2 | autotools,make | upstream tarball (Fedora lookaside mirror) |

### Input methods (6)

| Package | Version | Build | Source |
|---|---|---|---|
| `ibus` | 1.5.35~beta2 | autotools,c/c++,make,meson,python | upstream tarball (Fedora lookaside mirror) |
| `ibus-unikey` | 0.7.0~beta1 | c/c++,cmake | git archive (release tag) |
| `inih` | 62 | c/c++,meson | git archive (release tag) |
| `libphonenumber` | 8.13.55 | c/c++,cmake,make | git archive (release tag) |
| `mozc` | 2.29.5111.102 | c/c++,make,qt | upstream tarball (Fedora lookaside mirror) |
| `msgraph` | 0.3.5 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |

### KDE / Qt theming & desktop (5)

| Package | Version | Build | Source |
|---|---|---|---|
| `adw-gtk3-theme` | 6.4 | no-compile | upstream release tarball |
| `color-filesystem` | 1 | no-compile | source file(s) only, no tarball |
| `kde-filesystem` | 5 | c/c++,cmake,make | other |
| `kde-settings` | 44.0 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `kf5` | 5.116.0 | c/c++,cmake,make,qt | other |

### Wayland utilities (8)

| Package | Version | Build | Source |
|---|---|---|---|
| `setxkbmap` | 1.3.5 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `waypipe` | 0.11.2 | c/c++,meson,rust | git archive (release tag) |
| `wl-clipboard` | 2.3.0 | c/c++,meson | git archive (release tag) |
| `xhost` | 1.0.10 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xmodmap` | 1.0.12 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `xprop` | 1.2.8 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `xrdb` | 1.2.3 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `xrefresh` | 1.1.1 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |

### Base libraries / misc (45)

| Package | Version | Build | Source |
|---|---|---|---|
| `SDL3` | 3.4.16 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `abseil-cpp` | 20260526.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `appstream` | 1.1.3 | c/c++,cmake,make,meson,qt | upstream tarball (Fedora lookaside mirror) |
| `colord` | 1.4.8 | meson | upstream tarball (Fedora lookaside mirror) |
| `colord-gtk` | 0.3.1 | meson | upstream tarball (Fedora lookaside mirror) |
| `double-conversion` | 3.4.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `exiv2` | 0.28.9 | c/c++,cmake,make | git archive (release tag) |
| `flite` | 2.2 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `glib-networking` | 2.90~alpha | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `gsl` | 2.8 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `gsm` | 1.0.24 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `gtksourceview4` | 4.8.4 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `highway` | 1.3.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `icu` | 78.3 | c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `json-glib` | 1.10.8 | meson | upstream tarball (Fedora lookaside mirror) |
| `libappindicator` | 12.10.1 | autotools,make | upstream release tarball |
| `libasyncns` | 0.8 | c/c++ | upstream tarball (Fedora lookaside mirror) |
| `libayatana-appindicator` | 0.5.94 | c/c++,cmake,make | git archive (release tag) |
| `libayatana-ido` | 0.10.4 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `libayatana-indicator` | 0.9.4 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `libblockdev` | 3.5.0 | autotools,make,python | upstream release tarball |
| `libdaemon` | 0.14 | c/c++,make | upstream release tarball |
| `libdatrie` | 0.2.14 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libdbusmenu` | %{ubuntu_release}.0 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libevdev` | 1.13.7 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libfyaml` | 0.8 | autotools,c/c++,make | upstream tarball (Fedora lookaside mirror) |
| `libgexiv2` | 0.16.2 | c/c++,meson,python | upstream release tarball |
| `libgusb` | 0.4.9 | meson | upstream tarball (Fedora lookaside mirror) |
| `libical` | 3.0.20 | c/c++,cmake | upstream tarball (Fedora lookaside mirror) |
| `libnotify` | 0.8.8 | meson | upstream tarball (Fedora lookaside mirror) |
| `libsoup3` | 3.7.2 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `libthai` | 0.1.30 | c/c++ | upstream tarball (Fedora lookaside mirror) |
| `libwacom` | 2.19.0 | c/c++,make,meson | upstream tarball (Fedora lookaside mirror) |
| `libxmlb` | 0.3.29 | c/c++,meson | upstream tarball (Fedora lookaside mirror) |
| `lttng-ust` | 2.16.0 | autotools,c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `mozjs140` | 140.13.0 | c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `protobuf` | 33.5 | autotools,c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `protobuf3` | 3.19.6 | autotools,c/c++,make,python | upstream tarball (Fedora lookaside mirror) |
| `re2` | %{base_version} | c/c++,cmake,make,python | git archive (release tag) |
| `rest` | 0.10.2 | make,meson | upstream tarball (Fedora lookaside mirror) |
| `sdl2-compat` | 2.32.70 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `webkitgtk` | 2.53.91 | autotools,c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `xmlrpc-c` | 1.60.04 | c/c++,cmake,make,meson | upstream tarball (Fedora lookaside mirror) |
| `yyjson` | 0.12.0 | c/c++,cmake,make | upstream tarball (Fedora lookaside mirror) |
| `zenity` | 4.2.2 | c/c++,meson | upstream release tarball |

## A.5 Source-provenance and recipe statistics (measured)

| Metric | Value |
|---|---|
| Recipe directories under `packages/` | **352** |
| Entries under `.packit.yaml:packages` | **352** (+ `create-archive` action) |
| Entries under `config/upstream-sources.json:packages` | **352** |
| `.hummingbird-upstream.json` provenance files | **352** |
| Seeded from Fedora dist-git `rawhide` | **349** |
| Seeded from GitHub (negativo17 specs) | **3** — `libfreeaptx`, `liblc3plus`, `pipewire-libs-extra` |
| Recipes with patches (`*.patch`) | **154** (821 patch files total) |
| `sources` checksum files | **348** |
| Source kind: upstream release tarball | ~304 |
| Source kind: git release-tag archive | ~38 (github/gitlab/x.org archives) |
| Source kind: commit-pinned git | 2 — `google-noto-color-emoji-fonts` (`8998f5d`), `openrgb` (`74cbdcce`) |
| Source kind: no tarball / loose files | 5 — `color-filesystem`, `intel-media-driver-free`, `kde-filesystem`, `kf5`, `rust-just` (crate) |
| Build systems | make/C 214, meson 145, autotools 72, cmake 57, python 50, Qt 13, Rust 8, Go 4, no-compile (data/fonts) several |
| Base names also provided by Hummingbird | **4 of 352** — `git`, `icu`, `protobuf`, `re2` |
| utah SRPM names resolving to a same-named Fedora 44 binary | ~316 of 349 measured |

Caveat on the Fedora cross-check: utah's recipe `Name:` is often an SRPM/base
name while Fedora ships differently-named binary subpackages (e.g. `mesa`,
`wayland`, `webkitgtk`, `pycairo`, `rust-just`, `python-*`, `libratbag`,
`libayatana-appindicator`, `xorg-x11-%{pkgname}`), and some `Name:` fields are
macros. Those are name-mismatch artifacts, not Fedora gaps. The genuine
"Fedora 44 does not carry this" set is small and is listed in Part B.

## A.6 ABI / rebuild implications (the part that matters for pluto)

- utah rebuilds the **whole shared-library closure**, not just what Fedora
  lacks: gtk2/3/4, libadwaita, pango, cairo, gdk-pixbuf2, glib-networking,
  mesa, libdrm, libepoxy, libglvnd, pipewire, wireplumber, gstreamer1,
  ffmpeg, libheif, libva, vulkan-loader, and the X libraries. Each is a
  potential soname mismatch if taken from Fedora while its consumers are
  Hummingbird-built.
- Leaf applications and tools (`git`, `fish`, `fastfetch`, `gum`, `fzf`,
  `zenity`, fonts, firmware) carry no such coupling; they are rebuilt mostly
  for version currency and a coherent precedence story.
- The `precedence` job is the guard: a rebuilt RPM must outrank Fedora 44 and
  Hummingbird, and the factory reports any name Hummingbird also provides. This
  matters because pluto will consume a Hummingbird base where Fedora 44 repos
  are also enabled at build time.
- **No GPG verification of RPMs** in the OCI model: the cosign-signed repo
  metadata and the image digest are the trust anchor (same as Kestrel).

## A.7 Documentation drift to ignore

- `README.md` says "193 imported recipes"; `docs/architecture.md` says 345.
  **Both are stale — the tree has 352.**
- `docs/architecture.md` itself flags the `AGENTS.md` digest-pinned
  `quay.io/packit/packit` rule as a "live contradiction": only the
  verification-only SRPM pilot honors it; every real build uses the mutable
  `quay.io/fedora/fedora:44` tag.
- `config/runtime-contract.toml` lists `firefox`, `slitherer`, `anaconda-live`,
  `fish`, `zsh` under `[unavailable]`; `firefox` is *not* in the 352 recipes
  (utah ships the Flathub Firefox), so do not read the recipe list as a full
  desktop contract.

---

# Part B — what pluto will need to own

## B.0 How to read this part

"Current source" is the state on 2026-09-23, verified live. "In F44?" is a
`dnf repoquery --available` result on a Fedora 44 host.

**Priority vocabulary**

| Priority | Meaning |
|---|---|
| **Own now** | Not provided by Hummingbird or Fedora 44 (or too old to use); pluto's factory must produce it. |
| **Own later** | Provided or workable today, but worth owning for currency, control, or when it blocks a version. |
| **Leave (Fedora)** | Take from Fedora 44 at build time; do not rebuild unless ABI forces it. |
| **Leave (COPR)** | Keep the third-party COPR until the factory covers it. |
| **Drop** | Not needed by a niri/DMS image. |

## B.1 The niri + DMS stack, in full

### B.1.1 Master table

| Package | Upstream repo | Current source (2026-09) | In F44? | License | Build | Priority | Must-own vs currency |
|---|---|---|---|---|---|---|---|
| `niri` | `niri-wm/niri` (moved from `YaLTeR/niri`) | Fedora 44 **26.04-1.fc44**; `yalter/niri-git` COPR has git 2926 | **Yes** | GPL-3.0-or-later | Rust | **Own later** | Currency only |
| `xwayland-satellite` | `Supreeeme/xwayland-satellite` | Fedora 44 **0.8.2-1.fc44** | **Yes** | MPL-2.0 | Rust | **Own later** | Currency only; niri's hard Requires |
| `greetd` | `kennylevinsen/greetd` (sr.ht mirror) | Fedora 44 **0.10.3-6.fc44** | **Yes** | mixed GPL/Apache/MIT/Unlicense | Rust | **Leave (Fedora)** | Provided |
| `greetd-selinux` | same | Fedora 44 **0.10.3-6.fc44** | **Yes** | same | selinux | **Leave (Fedora)** | Provided |
| `dms` | `AvengeMedia/DankMaterialShell` | **`avengemedia/dms` COPR 1.6.2-1** (stable); `avengemedia/dms-git` has `2:0.0.git.4850` | **No** | MIT | Go + QML | **Own now** | Nothing provides it |
| `dms-cli` | subpackage of the `dms` spec | Same COPR, subpackage of `dms` | **No** | MIT | Go | **Own now** | Nothing provides it |
| `dms-greeter` | `AvengeMedia/dank-greeter` | **`avengemedia/danklinux` COPR `1:1.6.2-1`**; git build `2:1.6.2+git50` | **No** | MIT | Go | **Own now** | Nothing provides it |
| `quickshell` (stable) | `quickshell-mirror/quickshell` (upstream also at `git.outfoxxed.me/quickshell`) | Fedora 44 **0.2.1^git20260209**; `avengemedia/danklinux` has **0.3.1-5** | **Yes** (old) | LGPL-3.0-only AND GPL-3.0-only | C++/Qt6 | **Own later** | Currency + likely required version |
| `quickshell-git` | same | **`avengemedia/danklinux` COPR 0.3.2^861.gitfae96f1** | **No** | LGPL-3.0-only AND GPL-3.0-only | C++/Qt6 | **Own later** | Nothing provides it (rolling channel) |
| `matugen` | `InioX/matugen` | Fedora 44 **3.1.0-1.fc44**; COPR has **4.2.0-1** | **Yes** (old) | GPL-2.0-or-later | Rust | **Own later** | Currency |
| `danksearch` | `AvengeMedia/danksearch` | Fedora 44 **0.1.2-1.fc44**; COPR has **1.6.0-1** | **Yes** (old) | MIT | Go | **Own later** | Currency (DMS Recommends) |
| `dgop` | `AvengeMedia/dgop` | Fedora 44 **0.2.1-1.fc44**; COPR has **1:1.6.2-1** | **Yes** (old) | MIT | Go | **Own later** | Currency; **hard Requires of dms** |
| `cliphist` | `sentriz/cliphist` | Fedora 44 **0.7.0-1.fc44**; COPR same | **Yes** | GPL-3.0-or-later | Go | **Leave (Fedora)** | Provided; DMS has its own history |
| `material-symbols-fonts` | `google/material-design-icons` | **`avengemedia/danklinux` COPR 1.0-1** | **No** | Apache-2.0 | data/font | **Own now** | Nothing provides it |
| `dankcalendar-git` | `AvengeMedia/dankcalendar` | **`avengemedia/danklinux` COPR 1.6.2+git205** | **No** | MIT | Go | **Own later** | Optional DMS calendar |
| `qt6ct` | Fedora `qt6ct` (upstream opencode.net/trialuser/qt6ct) | Fedora 44 **0.11-17…fc44** | **Yes** | BSD-2-Clause | C++/Qt6 | **Leave (Fedora)** | Provided |
| `qt6ct-kde` | COPR fork of qt6ct | **`avengemedia/danklinux` COPR 0.11-13** | **No** | BSD-2-Clause | C++/Qt6 | **Own later / maybe drop** | Fedora `qt6ct` likely suffices |
| `breakpad` | `chromium.googlesource.com/breakpad` | **`avengemedia/danklinux` COPR 2024.02.16-1** | **No** | BSD-3-Clause | C++ | **Leave (COPR) for now** | Build dep of dankcalendar/quickshell tooling only |
| `cpptrace` | `jeremy-rifkin/cpptrace` | **`avengemedia/danklinux` COPR 1.0.4-4** | **No** | MIT | C++ | **Leave (COPR) for now** | Build dep only |
| `cli11` | `CLIUtils/CLI11` | **`avengemedia/danklinux` COPR 2.6.1-1** | **No** | BSD-3-Clause | C++ header | **Leave (COPR) for now** | Build dep only |

### B.1.2 Per-package notes

- **`dms` / `dms-cli`.** One spec, two binary packages. MIT. Go daemon/CLI
  (`dms-cli`) plus a Quickshell QML tree. Hard `Requires:
  (quickshell or quickshell-git)`, `accountsservice`, `dms-cli = version-release`,
  `dgop`. Recommends `cava`, `danksearch`, `matugen`, `NetworkManager`,
  `qt6-qtmultimedia`; Suggests `cups-pk-helper`, `qt6ct`. The COPR stable is
  1.6.2-1; `avengemedia/dms-git` carries `2:0.0.git.4850` (note the epoch — the
  git channel outranks stable). **Where a spec comes from:**
  `AvengeMedia/DankMaterialShell/distro/fedora/dms.spec` (MIT). Its git spec
  downloads the Go toolchain from `go.dev/dl/…` at build time (supply-chain flag).
- **`dms-greeter`.** MIT. `Requires: greetd`, `(quickshell-git or quickshell)`,
  `policycoreutils-python-utils`. Launches the compositor itself
  (`dms-greeter --command niri -C /etc/greetd/niri/config.kdl`). Spec:
  `AvengeMedia/dank-greeter/distro/fedora/dms-greeter.spec`.
- **`quickshell`.** The shell toolkit DMS is built on; links Qt6 private APIs,
  so version pairing with DMS matters. Fedora 44's 0.2.1 git snapshot is months
  behind the COPR's 0.3.x. This is the most likely reason Fedora's `quickshell`
  will **not** satisfy DMS 1.6.2, which would promote quickshell into slice 1.
  License is dual: `LGPL-3.0-only AND GPL-3.0-only` (the GPL part matters if you
  redistribute — record it).
- **`niri` / `xwayland-satellite`.** Both fully in Fedora 44 at current
  versions (niri 26.04, satellite 0.8.2). The only reason to own them is the
  `yalter/niri-git` currency channel. niri is GPL-3.0-or-later, Rust; satellite
  is MPL-2.0, Rust. Both need `%cargo_prep`/`%generate_buildrequires` handling
  (the utah/Kestrel Rust special-case).
- **Build-dep-only packages (`breakpad`, `cpptrace`, `cli11`).** These are in
  `danklinux` because a `danklinux` package builds against them, not because a
  user installs them. Only bring them into the factory if pluto builds the
  package that needs them (likely `dankcalendar`).

### B.1.3 Source-of-truth for the `avengemedia` specs

All `danklinux` recipes live in one MIT repo, `AvengeMedia/danklinux`, under
`distro/fedora/<name>/<name>.spec`, using `rpkg`. `dms` lives separately in
`AvengeMedia/DankMaterialShell/distro/fedora/`. Both repos are MIT (GitHub
license API), so importing the specs into pluto's MIT/Apache repository is
license-compatible **provided attribution is kept**. I did not find an explicit
redistribution grant beyond the repo license; treat the specs as MIT-licensed
and keep the header.

## B.2 Other COPR-sourced items in the archived pluto

| Package | Upstream | Current source | In F44? | License | Build | Priority | Recommendation |
|---|---|---|---|---|---|---|---|
| `ghostty` | `ghostty-org/ghostty` | `scottames/ghostty` COPR **1.3.1-4**; also `avengemedia/danklinux` 1.3.1-1 | **No** | MIT | Zig | **Own later** | Own when you want a pluto-vendored terminal; keep COPR until then |
| `gtk4-layer-shell` | `NixOS/gtk4-layer-shell` | `scottames/ghostty` COPR 1.3.0-1 | **Yes** (**1.3.0-1.fc44**) | MIT | C | **Leave (Fedora)** | Provided |
| `zig015` | `ziglang/zig` 0.15 | `scottames/ghostty` COPR 0.15.2-1 | Fedora has `zig` 0.15.2-3 and 0.16.0-1 | MIT | binary | **Leave (Fedora)** | Provided; `zig015` is the COPR's pinned build dep |
| `uupd` | `ublue-os/uupd` | `ublue-os/packages` COPR **1.4.0-2** | **No** | Apache-2.0 | Go | **Own later** | Already consumed by pluto via `copr_install_isolated`; single Go binary, clean second target |
| `oversteer` + `oversteer-udev` | likely `berarma/oversteer` (unverified; `ublue-os/oversteer` 404s) | `ublue-os/packages` COPR `0.0.git.415.7f6b3ad8-1` | **No** | unverified | Python/GTK (likely) | **Drop** | Logitech-wheel manager from the Bazzite gaming set; not needed by a niri/DMS desktop |
| ublue `bazaar`, `ublue-*`, `kcm_ublue`, `ublue-recipes` etc. | `ublue-os/packages` | COPR | **No** | Apache-2.0 | mixed | **Leave (COPR)** | Not part of pluto's desktop; only `uupd` is currently consumed |

The archived pluto's manifests put `ghostty` in `["copr:scottames/ghostty"]`
and `uupd`/`oversteer-udev` in `["copr:ublue-os/packages"]`
(`pluto-archive:build/packages/{base,niri}.toml`). The current finpilot template
already installs `uupd` from `ublue-os/packages` through
`build/copr-helpers.sh`.

## B.3 The utah set pluto may still want — desktop-agnostic pieces

Pluto is niri, not GNOME, so the GNOME 51 core (34 recipes) is irrelevant. The
desktop-agnostic parts of utah's 352 are what matter. Verified against Fedora 44
and the Hummingbird repo:

| Piece | utah recipes | In Fedora 44? | In Hummingbird? | Priority | Recommendation |
|---|---|---|---|---|---|
| Portals | `xdg-desktop-portal`, `-gnome`, `-gtk`, `xdg-user-dirs`, `xdg-user-dirs-gtk`, `xdg-terminal-exec`, `libportal` | Yes | No | **Leave (Fedora)** | Fedora builds are lean leaf services; rebuild only if ABI bites |
| Audio | `pipewire`, `wireplumber`, `pulseaudio`, `alsa-lib/‑utils/‑tools`, `sbc`, `libcanberra`, `gsound` | Yes | No | **Leave (Fedora)** | Core ABI; if any DMS/Quickshell-linked piece is rebuilt, revisit |
| Graphics | `mesa`, `mesa-libGLU`, `libdrm`, `libglvnd`, `libepoxy`, `vulkan-loader`, `spirv-tools`, `libva`, `intel-*` | Yes (`mesa-va-drivers` merged into `mesa-dri-drivers`) | No | **Leave (Fedora)** | High ABI risk to rebuild; Fedora's is the reference |
| Fonts | `adwaita-fonts`, `hicolor-icon-theme`, `google-noto-*`, `stix-fonts`, `fontconfig`, `fribidi` | Mostly yes; `adwaita-fonts` naming differs | `fontconfig`, `default-fonts` only | **Leave (Fedora)** | Pure data, no ABI |
| Flatpak / containers | `flatpak`, `flatpak-xdg-utils`, `distrobox`, `xdg-dbus-proxy`, `bubblewrap` | Yes | No | **Leave (Fedora)** | Bluefin parity; no rebuild need |
| Keyring / auth | `gnome-keyring`, `gcr`, `gcr3`, `libsecret`, `polkit-kde` | Yes | No | **Leave (Fedora)** | Leaf, but DMS/polkit touch it; revisit if ABI |
| Input / Wayland utils | `libinput`, `libevdev`, `libwacom`, `wayland`, `wayland-protocols`, `wl-clipboard`, `waypipe` | Yes | No | **Leave (Fedora)** | libinput is ABI-sensitive but niri links it from Fedora fine on a paired base |
| X11 / Xwayland | `xorg-x11-server-Xwayland` + 20 X libraries | Yes | No | **Leave (Fedora)** | niri needs `xwayland-satellite`; Xwayland from Fedora |
| Firmware | `linux-firmware` + vendor splits, `amd-ucode-firmware`, `microcode_ctl` | Yes | No | **Leave (Fedora)** | Large data packages; no rebuild value |
| Firmware/hw tooling | `fwupd`, `ddcutil`, `lm_sensors`, `smartmontools`, `v4l-utils` | Yes | No | **Leave (Fedora)** | Leaf; ddcutil is DMS's brightness backend |
| Multimedia / codecs | `ffmpeg`, `gstreamer1*`, `libheif`, `libjxl`, `lame` | `ffmpeg` is RPM Fusion / negativo17 only; rest mostly yes | No | **Leave (Fedora/negativo17)** | The archived pluto used negativo17 for mesa/ffmpeg; a decision, not an inventory item |
| Network | `NetworkManager-wifi` (+ `wpa_supplicant`, `wireless-regdb`, `iw`), `wireguard-tools`, `tailscale` | Yes | `NetworkManager-wifi` only | **Leave (Fedora)** | Hummingbird has the NM plugin but not its supplicant; Fedora fills both |

**Verdict:** none of the desktop foundation *must* be owned by pluto's factory
today. It is all in Fedora 44, and rebuilding it buys only ABI coherence with
Hummingbird — the utah problem, but for a smaller, leaf-heavy desktop. Own it
later, selectively, only when a specific mismatch appears.

## B.4 Must-own vs own-for-currency

**Must own (nothing on Hummingbird *or* Fedora 44 provides it):**
`dms`, `dms-cli`, `dms-greeter`, `quickshell-git`, `material-symbols-fonts`,
`dankcalendar-git`, `ghostty`, `uupd`, `oversteer`/`oversteer-udev`,
`breakpad`, `cpptrace`, `cli11`, `qt6ct-kde`. (All currently COPR-only.)

**Own for currency/control (present but stale or version-coupled):**
`niri`, `xwayland-satellite`, `quickshell`, `matugen`, `danksearch`, `dgop`.
Fedora 44 lags the COPR by 3 major-ish versions on `matugen`, `danksearch` and
`dgop`, and by a minor line on `quickshell`.

**Leave alone:** `greetd`, `greetd-selinux`, `cliphist`, `qt6ct`,
`accountsservice`, `cava`, `qt6-qtmultimedia`, `cups-pk-helper`, `khal`,
`kanshi`, `wtype`, `wl-mirror`, `wev`, `udiskie`, `gnome-disk-utility`,
`nm-connection-editor`, `accountsservice`, `kf6-*`, `plasma-breeze`, all the
portals/audio/graphics/fonts/firmware rows above.

## B.5 Recommended first slice

The factory scaffold already exists in this repo (commit `bd66585`):
`packages/config/factory-contract.json` pins the registry
(`ghcr.io/siddhj2206/pluto-packages`), the disttag suffix (`.hum1.pluto`), the
build container (`quay.io/fedora/fedora:44`) and the Hummingbird base, and
`packages/README.md` defines the allow-list-first workflow. Nothing in this
recommendation requires changing that shape; slice 1 is the first recipe.

The prior reports' ordering (DMS → danklinux → niri) still holds; the new data
sharpens it.

**Slice 1 — `dms` + `dms-cli`.** One MIT recipe (`DankMaterialShell`), two
subpackages, Go + QML, no ABI consumers, currently third-party. This is the
smallest change that proves "spec → `rpmbuild` in Fedora 44 + Hummingbird
overlay → OCI repo image → digest-pinned consumption". Acceptance: the built
`dms`/`dms-cli` install on the base with Fedora 44's `quickshell`, `dgop` and
`accountsservice` satisfying the requires.

**Slice 1b (likely, same PR series) — `quickshell`.** If DMS 1.6.2 rejects
Fedora's 0.2.1, own the 0.3.x line (`quickshell` stable or `quickshell-git`).
This is also the first C++/Qt6 build and the first place the Hummingbird-vs-
Fedora Qt ABI question is real.

**Slice 2 — `dgop` + `matugen` + `danksearch` + `material-symbols-fonts`.**
Small leaf Go/Rust/data packages; `dgop` is a hard Requires of `dms`, the rest
are DMS Recommends and the login/first-run experience degrades without them.

**Slice 3 — `dms-greeter`, then `niri` + `xwayland-satellite` for currency.**
`dms-greeter` completes the greetd login path. niri/satellite are already
available from Fedora, so only own them when a Fedora lag actually blocks a fix;
when you do, expect Rust `%generate_buildrequires` retries.

**Slice 4 / defer — `ghostty`, `uupd`.** `ghostty` needs Zig and pulls
`gtk4-layer-shell` (in Fedora); own it for control of the default terminal.
`uupd` is a single Go binary already consumed via COPR and is a clean migration.
Drop `oversteer` entirely.

Pluto's factory should build in **Fedora 44 + the Hummingbird Pulp overlay**
with disttag `.hum1.pluto`, exactly as Kestrel's `pigeon` does, publishing
`ghcr.io/siddhj2206/pluto-packages` — this keeps the Hummingbird ABI story open
and avoids the Packit→Copr Fedora-chroot problem the second report flagged.

## B.6 Cross-checks, flags and unknowns

**Verified live (2026-09-23)**

- Fedora 44 (`dnf5 repoquery`): `niri 26.04`, `xwayland-satellite 0.8.2`,
  `greetd 0.10.3`, `quickshell 0.2.1^git20260209`, `matugen 3.1.0`,
  `danksearch 0.1.2`, `dgop 0.2.1`, `cliphist 0.7.0`, `qt6ct 0.11`, `kanshi`,
  `kf6-*`, `plasma-breeze`, `khal`, all portals, pipewire/mesa/fonts/flatpak.
  Absent from F44: `dms`, `dms-cli`, `dms-greeter`, `quickshell-git`,
  `material-symbols-fonts`, `dankcalendar`, `ghostty`, `uupd`, `oversteer`,
  `breakpad`, `cpptrace`, `cli11`, `qt6ct-kde`; `mesa-va-drivers` is gone
  (merged into `mesa-dri-drivers`).
- COPRs (`api_3`): `avengemedia/dms` carries one source package `dms` at **1.6.2-1**
  (produces `dms` + `dms-cli`); `avengemedia/danklinux` carries 15 source
  packages (the B.1/B.2 set) with `danklinux` as a `coprdep` of `dms`;
  `avengemedia/dms-git` exists (`2:0.0.git.4850`); `scottames/ghostty` carries
  `ghostty 1.3.1-4`, `gtk4-layer-shell 1.3.0`, `zig015 0.15.2`;
  `ublue-os/packages` carries `uupd 1.4.0-2` and `oversteer 0.0.git.415`;
  `yalter/niri-git` carries niri git 2926.
- Hummingbird: 3,515 binary names; only `fontconfig`, `default-fonts` and
  `NetworkManager-wifi` from the checked desktop set, and only four base-name
  overlaps with utah's 352. Hummingbird is an overlay, not a distribution.

**Could not verify / flags**

- **Redistribution right** of the `avengemedia` specs: the repos declare MIT,
  but there is no separate redistribution statement. Importing the spec text
  under MIT with attribution is the defensible reading; a human should confirm
  before publishing rebuilt RPMs.
- **`oversteer` upstream unverified.** `ublue-os/oversteer` returns 404; the
  spec is not in `ublue-os/packages@main` (only `staging/` items surfaced by
  tree listing, and not that one). Likely `berarma/oversteer`, but not confirmed.
  Recommendation stands regardless: drop it.
- **DMS ↔ Fedora `quickshell`/`dgop` compatibility is untested.** DMS 1.6.2's
  private-API expectations against Fedora's quickshell 0.2.1 snapshot are
  unknown; if they fail, both move into slice 1. Same question for Fedora's
  `dgop` 0.2.1 versus DMS's `dgop` hard-require.
- **Epoch hazards.** `dms` git builds carry epoch 2 and `dgop` git carries
  epoch 1 in the COPR; a pluto-built package that must outrank the COPR needs a
  matching or higher epoch, and the utah `precedence` pattern (outrank Fedora +
  Hummingbird) should include the COPR until it is retired.
- **Hummingbird-vs-Fedora ABI.** The utah report and the second report both flag
  it: a Fedora-chroot rebuild does not answer the soname question. pluto's
  factory should build against the Hummingbird overlay so `quickshell`'s Qt6
  and libEGL links are coherent with the shipped base. This is a reason to
  prefer the Kestrel buildroot over a plain Fedora container.
- **`zig015` and `gtk4-layer-shell`** as named in the `scottames/ghostty` COPR
  are only needed if pluto copies that COPR's build model; Fedora 44 already
  provides both under different names, so they should not be owned.
- **utah doc drift** (193/345 vs 352) is a reminder that counts in prose are
  stale; the counts here are measured from the tree.

---

## Sources

### Repositories cloned / extracted

```bash
git clone --depth 1 https://github.com/projectbluefin/utah-packages \
  /tmp/opencode/pkg-inventory/utah-packages          # HEAD e2c4f43
git -C /var/home/sid/Documents/Projects/pluto archive origin/archive/pluto-pre-rewrite \
  | tar -x -C /tmp/opencode/pkg-inventory/pluto-archive   # 18d57a0
```

### utah-packages files cited

- `.packit.yaml` (352 `packages:` entries, 0 `jobs:`)
- `packages/*/` (352 dirs), `packages/gnome-shell/gnome-shell.spec` (sampled),
  `packages/mesa/mesa.spec` + `sources` (sampled)
- `.hummingbird-upstream.json` (all 352; 349 rawhide, 3 GitHub)
- `config/upstream-sources.json`, `config/hummingbird.repo`,
  `config/runtime-contract.toml`, `config/factory-contract.json`
- `README.md`, `docs/architecture.md`
- `.github/workflows/packit-srpm-pilot.yml`, `rebuild-rpms.yml`,
  `import-rawhide-package.yml`
- `tools/source_pipeline.py`, `tools/render_packit_config.py`

### Archived pluto files cited

- `build/packages/{base,niri,multimedia,dx,firmware}.toml`
- `docs/research/copr-inventory.md`, `docs/research/niri-packages.md`
- `build/scripts/package-lib.sh`, `build/clean-stage.sh`

### Upstream / registry checks

```bash
# Fedora 44 existence + versions (host is Fedora 44)
dnf repoquery --available --qf $'%{name} %{evr}\n' <names>

# COPR project metadata / package list / build list (api_3)
curl -sS 'https://copr.fedorainfracloud.org/api_3/project?ownername=avengemedia&projectname=danklinux'
curl -sS 'https://copr.fedorainfracloud.org/api_3/package/list?ownername=avengemedia&projectname=danklinux'
curl -sS 'https://copr.fedorainfracloud.org/api_3/build/list?ownername=avengemedia&projectname=danklinux&limit=100'
# (same for avengemedia/dms, avengemedia/dms-git, scottames/ghostty,
#  ublue-os/packages, yalter/niri-git)

# Hummingbird base repo (Pulp primary.xml, 3,515 binary names)
curl -sSL 'https://koji-s3-cache.hummingbird-project.io/packages.redhat.com/api/pulp-content/public-hummingbird/x86_64/repodata/repomd.xml'

# Spec/upstream/license sources
gh api repos/<owner>/<repo> --jq '{license:.license.spdx_id,language,default_branch}'
curl -sS https://raw.githubusercontent.com/AvengeMedia/danklinux/master/distro/fedora/<pkg>/<pkg>.spec
curl -sS https://raw.githubusercontent.com/AvengeMedia/DankMaterialShell/master/distro/fedora/dms.spec
curl -sS https://raw.githubusercontent.com/AvengeMedia/dank-greeter/master/distro/fedora/dms-greeter.spec
curl -sS https://src.fedoraproject.org/rpms/<pkg>/raw/rawhide/f/<pkg>.spec
```

### URLs

- utah-packages: https://github.com/projectbluefin/utah-packages
- utah: https://github.com/projectbluefin/utah
- COPRs: https://copr.fedorainfracloud.org/coprs/avengemedia/dms ·
  `.../avengemedia/dms-git` · `.../avengemedia/danklinux` ·
  `.../scottames/ghostty` · `.../ublue-os/packages` · `.../yalter/niri-git`
- niri: https://github.com/niri-wm/niri
- xwayland-satellite: https://github.com/Supreeeme/xwayland-satellite
- greetd: https://sr.ht/~kennylevinsen/greetd/
- DankMaterialShell: https://github.com/AvengeMedia/DankMaterialShell
- dank-greeter: https://github.com/AvengeMedia/dank-greeter
- danklinux (specs): https://github.com/AvengeMedia/danklinux
- quickshell: https://github.com/quickshell-mirror/quickshell · https://quickshell.org/
- matugen: https://github.com/InioX/matugen
- dgop: https://github.com/AvengeMedia/dgop
- danksearch: https://github.com/AvengeMedia/danksearch
- material-symbols-fonts: https://github.com/google/material-design-icons
- ghostty: https://github.com/ghostty-org/ghostty
- uupd: https://github.com/ublue-os/uupd
- Hummingbird base: https://packages.redhat.com ·
  `quay.io/hummingbird-community/bootc-os`
- Kestrel (factory pattern): https://github.com/HuntedRaven7/Kestrel
