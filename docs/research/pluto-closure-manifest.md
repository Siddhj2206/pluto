# Pluto closure manifest — building the niri + DMS desktop on a Hummingbird-only base

**Date:** 2026-09-23
**Author:** research subagent (DeepSeek V4.1 Flash via OpenCode)
**Scope:** the exact set of RPM recipes pluto must build — and the order — so a
niri + DankMaterialShell desktop runs on `quay.io/hummingbird-community/bootc-os`
with **no Fedora repositories enabled in the image at build or runtime** (Model 3,
the `projectbluefin/utah` model), published as `ghcr.io/siddhj2206/pluto-packages`.

**Prior reports this builds on (read first, not repeated here):**
`docs/research/utah-hummingbird-niri-dms.md`, `docs/research/packit-monorepo-packages.md`,
`docs/research/kestrel-monorepo.md`, `docs/research/package-inventory.md`.

---

## TL;DR

1. **Hummingbird ships no desktop.** Its parsed package index has **3,514 binary names**
   — the Fedora base OS plus a handful of libraries (`glib2`, `systemd`, `polkit`,
   `NetworkManager`, `fontconfig`, `default-fonts`, `google-noto-*`, `alsa-lib`,
   `qt6-qtbase`, `qt6-qtsvg`, `qt6-qtbase-private-devel`, `libX11`, `libxkbcommon`,
   `xkeyboard-config`, `harfbuzz`, `freetype`, `libpng`, `libjpeg-turbo`,
   `libtiff`, `libwebp`, `kbd`, `firewalld`, `podman`/`crun`/`netavark`, `avahi`,
   `cups`, `ostree`/`bootc`, `rpm`/`dnf5`). It ships **no GTK, no mesa, no
   libdrm, no wayland, no pipewire, no Qt6 declarative/wayland, no kf6, no
   portals, no niri/greetd/quickshell**.
2. **Pluto must own 479 recipes.** Measured: 471 recipes from the transitive
   runtime closure of the declared top-level set minus the Hummingbird index,
   plus 8 packages absent from Fedora 44 entirely (`dms`, `dms-cli`,
   `dms-greeter`, `material-symbols-fonts`, `ghostty`, `uupd`, `dankcalendar-git`,
   `cpptrace`). **239 are reusable from utah-packages**
   (same-or-adapt), **240 are new** (Qt6/KF6/niri/DMS
   and the DMS stack utah never built).
3. **The strict niri/DMS core is much smaller than the parity closure.** A BFS
   from niri/quickshell/DMS over the measured dependency graph gives
   **110 core recipes**; the rest
   (nautilus, gvfs, glycin, flatpak, distrobox, multimedia, language fonts…) is
   opt-in parity that can be deferred without breaking the shell.
4. **Order is bottom-up in five waves.** Wave 0 has no in-closure dependencies;
   the DMS stack is wave 4. See [Wave plan](#wave-plan).
5. **Biggest risks:** Qt6 private-API version coupling (`quickshell` ↔ DMS ↔ the
   `qt6-qtbase` Hummingbird ships), the danklinux specs shipping **prebuilt Go
   binaries downloaded at build time**, and the `kf6` closure that niri itself
   never needed but DMS theming does. See [Hazards](#hazards).

---

## 1. Method

1. **Seed set.** The top-level packages pluto must ship (`niri`,
   `xwayland-satellite`, `greetd`/`greetd-selinux`, `quickshell`/`quickshell-git`,
   `dms`, `dms-cli`, `dms-greeter`, `dgop`, `matugen`, `danksearch`,
   `material-symbols-fonts`, `cliphist`, `qt6ct`, `ghostty`, `uupd`) plus the
   desktop foundation from the archived pluto manifests
   (`pluto-archive:build/packages/{base,niri}.toml`: portals, pipewire/
   wireplumber, mesa/VA/vulkan, fonts, libinput/wayland, Xwayland + X libs,
   `NetworkManager-wifi` + supplicant/regdb/iw, polkit, accountsservice, …).
2. **Transitive closure.** On a Fedora 44 host,
   `dnf5 repoquery --providers-of=requires --recursive` over the seed set, mapped
   binary→source RPM (`%{sourcerpm}`), giving 1,399 binaries / 689 SRPMs.
   Build deps were read from the real upstream specs, not guessed.
3. **Subtract Hummingbird.** A recipe is needed only when at least one of its
   required binaries is **absent from Hummingbird's 3,514-name index** (parsed
   from the Pulp `primary.xml` in the prior inventory). Shared libraries present
   in Hummingbird (`glib2`, `glibc`, `qt6-qtbase`, `libxkbcommon`, …) are **not**
   rebuilt — the desktop is built *against* Hummingbird's copies, exactly as utah
   does. No ABI-coherence rebuild of a Hummingbird-provided library is required
   for pluto's stack because every Hummingbird-provided library is a base
   library, not a desktop library.
4. **Cross-reference utah.** Each member is matched against `utah-packages`'
   352 recipe directories (`packages/<name>/<name>.spec`): **reuse**, **adapt**,
   or **new**.
5. **Waves.** A dependency graph was built by querying each recipe's direct
   requires (`dnf5 repoquery --providers-of=requires`) and keeping only edges
   inside the closure. Wave = longest-path depth, bucketed 0–4.

**Reproduce (host = Fedora 44):**

```bash
dnf5 repoquery --providers-of=requires --recursive --qf '%{name}\n' <seeds> \
  | sort -u > closure-fedora44.txt
# map to SRPMs, subtract Hummingbird, then per-package direct requires:
dnf5 repoquery --providers-of=requires --qf '%{name}\n' <pkg>
```

> **Over-approximation caveat.** `--providers-of=requires` lists *all* providers
> of a capability, including alternative providers (`docker-cli`, `iwd`, `qemu`).
> Those are flagged `scope=drop` below. The core/parity split is a recommendation,
> not a hard solver output. Anything marked **UNVERIFIED** needs a human call.

---

## 2. The Hummingbird boundary

| Layer | Provided by Hummingbird? | Consequence |
|---|---|---|
| Kernel, systemd, glibc, glib2, rpm/dnf5, podman/crun/netavark, ostree/bootc, firewalld, avahi, cups, `alsa-lib`, `polkit`, `NetworkManager`, `fontconfig`, `default-fonts`, `google-noto-*`, `libX11`, `libxkbcommon`, `xkeyboard-config`, `harfbuzz`, `freetype`, `libpng`/`libjpeg-turbo`/`libtiff`/`libwebp` | **Yes** | Take as-is; do **not** rebuild. |
| `qt6-qtbase`, `qt6-qtsvg`, `qt6-qtbase-private-devel` | **Yes** (unusual, but verified) | quickshell links Hummingbird's Qt6 base — the Qt6 *declarative/wayland/shadertools* modules are **not** there and must be built to match it. |
| mesa, libdrm, libglvnd, libepoxy, vulkan-loader, wayland libs, libinput, libevdev, libdisplay-info, libseat | **No** | Rebuild the whole graphics/Wayland base. |
| GTK2/3/4, libadwaita, cairo, pango, gdk-pixbuf2, graphene | **No** | Rebuild (needed by ghostty/nautilus; DMS itself is Qt). |
| pipewire, wireplumber, pulseaudio, portals, gnome-keyring, libsecret, dconf, accountsservice, bluez, colord, upower, udisks2, libblockdev, rtkit | **No** | Rebuild. |
| Qt6 declarative/wayland/shadertools, kf6, plasma-breeze, niri, greetd, quickshell, DMS stack | **No** | Own entirely. |

Verify any name with:

```bash
grep -qx '<pkg>' hummingbird-names.txt   # /tmp/opencode/pkg-inventory/hummingbird-names.txt
```

---

## Wave plan

Waves are the longest in-closure dependency depth, bucketed 0–4 (utah/Kestrel
practice; utah's own `config/upstream-sources.json` now stages 0–10, a finer
grain of the same idea — most utah entries still carry no explicit stage, so the
measured graph is the better source here).

| Wave | Meaning | Recipes | utah-reusable | new |
|---|---|---:|---:|---:|
| 0 | Lowest-level libraries, no in-closure deps (X/Wayland/Qt-base plumbing, codecs, Rust/C++ runtimes) | 225 | 140 | 85 |
| 1 | Core graphics/audio/media + toolkit prerequisites | 74 | 38 | 36 |
| 2 | GTK/services/portals/compositor-support libs | 79 | 22 | 57 |
| 3 | Qt6 + KF6 + pipewire/wireplumber + compositor + Xwayland | 31 | 6 | 25 |
| 4 | DMS stack, login, apps, session glue | 70 | 33 | 37 |
| **Σ** | | **479** | **239** | **240** |

Waves are *parallel within a wave* — every package in wave N only needs wave
< N (plus Hummingbird) to build.

---

## Manifest

**Columns.** *Reuse* = `packages/<name>/<name>.spec` in
`projectbluefin/utah-packages` (reuse/adapt) or `new`. *Why owned* = the
required binary/binary that is missing from the Hummingbird index.
*Key deps* = in-closure dependencies only (Hummingbird-provided deps hidden).
*Build* = upstream build system. *Scope* = **core** (strict niri/DMS closure),
**parity** (desktop nicety, deferrable), **drop** (recommended against —
alternative providers, ISO-only, or superseded).


### Wave 0 (225 recipes)

| Package | Reuse / New | Why owned | Key deps | License | Build | Patches | Scope | Hazards / notes |
|---|---|---|---|---|---|---:|---|---|
| `accountsservice` | utah `packages/accountsservice/accountsservice.spec` | not in Hummingbird (accountsservice, accountsservice-libs) | — | GPL-3.0-or-later | Meson | 0 | core | Hard Requires of dms; GPL-3.0-or-later; absent from Hummingbird. |
| `adwaita-icon-theme-legacy` | utah `packages/adwaita-icon-theme-legacy/adwaita-icon-theme-legacy.spec` | not in Hummingbird (adwaita-icon-theme-legacy) | — | LGPL-3.0-only OR CC-BY-SA-3.0 | — | 0 | core |  |
| `cdparanoia` | utah `packages/cdparanoia/cdparanoia.spec` | not in Hummingbird (cdparanoia-libs) | — | LicenseRef-Callaway-LGPLv2 | — | 7 | core |  |
| `cpptrace` | **new** | absent from Fedora 44 and Hummingbird | libdwarf, libzstd | MIT | C++/CMake | 0 | core | BUILD-ONLY: quickshell's crash handler links it; absent from Fedora 44. Alternatively build quickshell with -DCRASH_HANDLER=OFF and skip cpptrace. |
| `desktop-file-utils` | utah `packages/desktop-file-utils/desktop-file-utils.spec` | not in Hummingbird (desktop-file-utils) | — | GPL-2.0-or-later | — | 1 | core |  |
| `fribidi` | utah `packages/fribidi/fribidi.spec` | not in Hummingbird (fribidi) | — | LGPL-2.1-or-later AND Unicode-DFS-2016 | — | 1 | core |  |
| `graphene` | utah `packages/graphene/graphene.spec` | not in Hummingbird (graphene) | — | MIT | Meson | 1 | core |  |
| `gsettings-desktop-schemas` | utah `packages/gsettings-desktop-schemas/gsettings-desktop-schemas.spec` | not in Hummingbird (gsettings-desktop-schemas) | — | LGPL-2.1-or-later | — | 0 | core |  |
| `gstreamer1` | utah `packages/gstreamer1/gstreamer1.spec` | not in Hummingbird (gstreamer1) | — | LGPL-2.1-or-later | — | 2 | core |  |
| `hicolor-icon-theme` | utah `packages/hicolor-icon-theme/hicolor-icon-theme.spec` | not in Hummingbird (hicolor-icon-theme) | — | GPL-2.0-or-later | Meson/data | 0 | core |  |
| `hwdata` | utah `packages/hwdata/hwdata.spec` | not in Hummingbird (hwdata) | — | GPL-2.0-or-later | — | 0 | core |  |
| `iso-codes` | utah `packages/iso-codes/iso-codes.spec` | not in Hummingbird (iso-codes) | — | LGPL-2.1-or-later | — | 0 | core |  |
| `json-glib` | utah `packages/json-glib/json-glib.spec` | not in Hummingbird (json-glib) | — | LGPL-2.1-or-later | Meson | 0 | core |  |
| `kde-filesystem` | utah `packages/kde-filesystem/kde-filesystem.spec` | not in Hummingbird (kde-filesystem) | — | LicenseRef-Not-Copyrightable | — | 0 | core |  |
| `kf6-breeze-icons` | **new** | not in Hummingbird (breeze-icon-theme, kf6-breeze-icons) | — | LGPL-2.0-or-later AND LGPL-2.1-or-later AND LGPL-3.0-or-later AND CC-BY-SA-4.0 / LGPL-2.1-or-later AND LGPL-3.0-or-later AND CC-BY-SA-4.0 | — | 0 | core |  |
| `lcms2` | utah `packages/lcms2/lcms2.spec` | not in Hummingbird (lcms2) | — | MIT AND GPL-3.0-or-later | — | 0 | core |  |
| `libcloudproviders` | utah `packages/libcloudproviders/libcloudproviders.spec` | not in Hummingbird (libcloudproviders) | — | LGPL-3.0-or-later | — | 0 | core |  |
| `libdatrie` | utah `packages/libdatrie/libdatrie.spec` | not in Hummingbird (libdatrie) | — | LGPL-2.1-or-later | — | 1 | core |  |
| `libdisplay-info` | utah `packages/libdisplay-info/libdisplay-info.spec` | not in Hummingbird (libdisplay-info) | — | MIT | Meson | 0 | core |  |
| `libei` | utah `packages/libei/libei.spec` | not in Hummingbird (libei, liboeffis) | — | MIT | Meson | 0 | core |  |
| `libepoxy` | utah `packages/libepoxy/libepoxy.spec` | not in Hummingbird (libepoxy) | — | MIT | CMake | 1 | core |  |
| `libevdev` | utah `packages/libevdev/libevdev.spec` | not in Hummingbird (libevdev) | — | MIT | Meson | 0 | core |  |
| `libglvnd` | utah `packages/libglvnd/libglvnd.spec` | not in Hummingbird (libglvnd, libglvnd-egl) | — | MIT-feh AND MIT-Modern-Variant AND BSD-1-Clause AND BSD-3-Clause AND GPL-3.0-or-later WITH Autoconf-exception-macro | CMake | 1 | core |  |
| `libgudev` | utah `packages/libgudev/libgudev.spec` | not in Hummingbird (libgudev) | — | LGPL-2.1-or-later | — | 0 | core |  |
| `libogg` | utah `packages/libogg/libogg.spec` | not in Hummingbird (libogg) | — | BSD-3-Clause | — | 0 | core |  |
| `libproxy` | utah `packages/libproxy/libproxy.spec` | not in Hummingbird (libproxy) | — | LGPL-2.1-or-later | — | 1 | core |  |
| `libvisual` | utah `packages/libvisual/libvisual.spec` | not in Hummingbird (libvisual) | — | LGPL-2.1-or-later | — | 3 | core |  |
| `libxcvt` | utah `packages/libxcvt/libxcvt.spec` | not in Hummingbird (libxcvt) | — | MIT AND HPND-sell-variant | Meson | 0 | core |  |
| `libXdmcp` | utah `packages/libXdmcp/libXdmcp.spec` | not in Hummingbird (libXdmcp) | — | MIT-open-group | autotools | 0 | core |  |
| `libXfixes` | utah `packages/libXfixes/libXfixes.spec` | not in Hummingbird (libXfixes) | — | MIT AND HPND-sell-variant | autotools | 0 | core |  |
| `libXfont2` | utah `packages/libXfont2/libXfont2.spec` | not in Hummingbird (libXfont2) | — | BSD-2-Clause AND BSD-4-Clause-UC AND HPND-sell-variant AND MIT-open-group AND SMLNJ AND X11 | autotools | 0 | core |  |
| `libXinerama` | utah `packages/libXinerama/libXinerama.spec` | not in Hummingbird (libXinerama) | — | MIT AND MIT-open-group AND X11 | autotools | 0 | core |  |
| `libxkbfile` | utah `packages/libxkbfile/libxkbfile.spec` | not in Hummingbird (libxkbfile) | — | MIT-open-group AND HPND AND SMLNJ | — | 0 | core |  |
| `libXrandr` | utah `packages/libXrandr/libXrandr.spec` | not in Hummingbird (libXrandr) | — | HPND-sell-variant | autotools | 0 | core |  |
| `libxshmfence` | utah `packages/libxshmfence/libxshmfence.spec` | not in Hummingbird (libxshmfence) | — | HPND-sell-variant | Meson | 1 | core |  |
| `libXv` | utah `packages/libXv/libXv.spec` | not in Hummingbird (libXv) | — | SMLNJ AND HPND-sell-variant | autotools | 0 | core |  |
| `mtdev` | utah `packages/mtdev/mtdev.spec` | not in Hummingbird (mtdev) | — | MIT | Meson | 0 | core |  |
| `opus` | utah `packages/opus/opus.spec` | not in Hummingbird (opus) | — | BSD-3-Clause AND BSD-2-Clause | — | 0 | core |  |
| `orc` | utah `packages/orc/orc.spec` | not in Hummingbird (orc) | — | BSD-2-Clause AND BSD-3-Clause | — | 0 | core |  |
| `pixman` | utah `packages/pixman/pixman.spec` | not in Hummingbird (pixman) | — | MIT | autotools | 0 | core |  |
| `rtkit` | utah `packages/rtkit/rtkit.spec` | not in Hummingbird (rtkit) | — | GPL-3.0-or-later AND MIT | Meson | 1 | core |  |
| `seatd` | **new** | not in Hummingbird (libseat) | — | MIT | — | 0 | core |  |
| `shared-mime-info` | utah `packages/shared-mime-info/shared-mime-info.spec` | not in Hummingbird (shared-mime-info) | — | GPL-2.0-or-later | — | 1 | core |  |
| `vulkan-loader` | utah `packages/vulkan-loader/vulkan-loader.spec` | not in Hummingbird (vulkan-loader) | — | Apache-2.0 | Meson | 1 | core |  |
| `wayland` | utah `packages/wayland/wayland.spec` | not in Hummingbird (libwayland-client, libwayland-cursor) | — | MIT | Meson | 1 | core | Provides libwayland-client/server/cursor/egl; everything Wayland below niri links it. |
| `xcb-util` | utah `packages/xcb-util/xcb-util.spec` | not in Hummingbird (xcb-util) | — | X11-distribute-modifications-variant | autotools | 0 | core |  |
| `xcb-util-renderutil` | utah `packages/xcb-util-renderutil/xcb-util-renderutil.spec` | not in Hummingbird (xcb-util-renderutil) | — | X11-distribute-modifications-variant AND HPND-sell-variant | autotools | 0 | core |  |
| `xprop` | utah `packages/xprop/xprop.spec` | not in Hummingbird (xprop) | — | MIT | — | 0 | core |  |
| `abseil-cpp` | utah `packages/abseil-cpp/abseil-cpp.spec` | not in Hummingbird (abseil-cpp) | — | Apache-2.0 AND LicenseRef-Fedora-Public-Domain | — | 2 | parity |  |
| `adw-gtk3-theme` | utah `packages/adw-gtk3-theme/adw-gtk3-theme.spec` | not in Hummingbird | — | LGPL-2.1-only | — | 0 | parity |  |
| `adwaita-fonts` | utah `packages/adwaita-fonts/adwaita-fonts.spec` | not in Hummingbird (adwaita-mono-fonts, adwaita-sans-fonts) | — | OFL-1.1 | Meson/data | 0 | parity |  |
| `aml` | **new** | not in Hummingbird (aml) | — | ISC AND LicenseRef-Callaway-BSD | — | 0 | parity |  |
| `appstream-data` | **new** | not in Hummingbird (appstream-data) | — | CC0-1.0 AND CC-BY-1.0 AND CC-BY-SA-1.0 AND GFDL-1.1-or-later | — | 0 | parity |  |
| `aribb24` | utah `packages/aribb24/aribb24.spec` | not in Hummingbird (aribb24) | — | LGPL-3.0-only | — | 0 | parity |  |
| `bluez` | utah `packages/bluez/bluez.spec` | not in Hummingbird (bluez-libs) | — | GPL-2.0-or-later | Meson | 4 | parity |  |
| `brightnessctl` | **new** | not in Hummingbird | — | MIT | — | 0 | parity |  |
| `cjson` | **new** | not in Hummingbird (cjson) | — | MIT | — | 0 | **drop** |  |
| `cups-pk-helper` | utah `packages/cups-pk-helper/cups-pk-helper.spec` | not in Hummingbird | — | GPL-2.0-or-later | — | 0 | parity |  |
| `dbusmenu-qt` | **new** | not in Hummingbird (dbusmenu-qt5) | — | LGPL-2.0-or-later | — | 0 | parity |  |
| `dconf` | utah `packages/dconf/dconf.spec` | not in Hummingbird (dconf) | — | LGPL-2.0-or-later AND LGPL-2.1-or-later AND GPL-2.0-or-later AND GPL-3.0-or-later | Meson | 1 | parity |  |
| `dmidecode` | **new** | not in Hummingbird (dmidecode) | — | GPL-2.0-or-later | — | 0 | **drop** |  |
| `docbook-dtds` | **new** | not in Hummingbird (docbook-dtds) | — | LicenseRef-docbook-dtds | — | 0 | **drop** |  |
| `double-conversion` | utah `packages/double-conversion/double-conversion.spec` | not in Hummingbird (double-conversion) | — | BSD-3-Clause | — | 0 | parity |  |
| `evtest` | utah `packages/evtest/evtest.spec` | not in Hummingbird | — | GPL-2.0-or-later | — | 0 | parity |  |
| `exempi` | **new** | not in Hummingbird (exempi) | — | BSD-3-Clause | — | 0 | **drop** |  |
| `fdk-aac-free` | utah `packages/fdk-aac-free/fdk-aac-free.spec` | not in Hummingbird (fdk-aac-free) | — | FDK-AAC | — | 0 | parity |  |
| `fedora-iot-config` | **new** | not in Hummingbird (fedora-iot-config) | — | MIT | — | 0 | **drop** |  |
| `fedora-release` | **new** | not in Hummingbird (fedora-release, fedora-release-budgie) | — | MIT | — | 0 | **drop** |  |
| `fish` | utah `packages/fish/fish.spec` | not in Hummingbird | — | Apache-2.0 OR MIT and GPL-2.0-only AND LGPL-2.0-or-later AND MIT AND PSF-2.0 and Unlicense OR MIT and WTFPL and Zlib | — | 0 | parity |  |
| `game-music-emu` | **new** | not in Hummingbird (game-music-emu) | — | LicenseRef-Callaway-LGPLv2+ | — | 0 | **drop** |  |
| `gcr` | utah `packages/gcr/gcr.spec` | not in Hummingbird (gcr-libs) | — | LGPL-2.1-or-later AND FSFULLRWD AND (LGPL-3.0-or-later OR CC-BY-SA-3.0) AND (MPL-1.1 OR GPL-2.0-or-later OR LGPL-2.1-or-later) AND GCR-docs | — | 0 | parity |  |
| `generic-logos` | **new** | not in Hummingbird (generic-logos) | — | GPL-2.0-only AND LicenseRef-Callaway-LGPLv2+ | — | 0 | **drop** |  |
| `generic-release` | **new** | not in Hummingbird (generic-release, generic-release-common) | — | MIT | — | 0 | **drop** |  |
| `giflib` | **new** | not in Hummingbird (giflib) | — | MIT | — | 0 | **drop** |  |
| `gobject-introspection` | utah `packages/gobject-introspection/gobject-introspection.spec` | not in Hummingbird (gobject-introspection) | — | GPL-2.0-or-later AND LGPL-2.0-or-later AND LGPL-2.1-or-later AND BSD-2-Clause | — | 3 | parity |  |
| `google-noto-emoji-fonts` | **new** | not in Hummingbird (google-noto-color-emoji-fonts) | — | OFL-1.1 AND Apache-2.0 | — | 0 | **drop** |  |
| `google-noto-sans-cjk-vf-fonts` | utah `packages/google-noto-sans-cjk-vf-fonts/google-noto-sans-cjk-vf-fonts.spec` | not in Hummingbird (google-noto-sans-cjk-vf-fonts, google-noto-sans-mono-cjk-vf-fonts) | — | OFL-1.1 | — | 0 | parity |  |
| `google-noto-serif-cjk-vf-fonts` | **new** | not in Hummingbird (google-noto-serif-cjk-vf-fonts) | — | OFL-1.1 | — | 0 | **drop** |  |
| `grub2` | utah `packages/grub2/grub2.spec` | not in Hummingbird (grub2-common, grub2-tools) | — | GPL-3.0-or-later | — | 451 | parity |  |
| `gsl` | utah `packages/gsl/gsl.spec` | not in Hummingbird (gsl) | — | GPL-3.0-or-later | — | 3 | parity |  |
| `gsm` | utah `packages/gsm/gsm.spec` | not in Hummingbird (gsm) | — | tu-berlin-2.0 | — | 2 | parity |  |
| `gum` | utah `packages/gum/gum.spec` | not in Hummingbird | — | BSD-3-Clause AND MIT AND OFL-1.1 | Go | 0 | parity |  |
| `highway` | utah `packages/highway/highway.spec` | not in Hummingbird (highway) | — | Apache-2.0 | — | 3 | parity |  |
| `i2c-tools` | utah `packages/i2c-tools/i2c-tools.spec` | not in Hummingbird (i2c-tools, libi2c) | — | GPL-2.0-or-later / LGPL-2.1-or-later | — | 0 | parity |  |
| `ilbc` | utah `packages/ilbc/ilbc.spec` | not in Hummingbird (ilbc) | — | BSD-3-Clause | — | 3 | parity |  |
| `imath` | **new** | not in Hummingbird (imath) | — | BSD-3-Clause | — | 0 | **drop** |  |
| `inih` | utah `packages/inih/inih.spec` | not in Hummingbird (inih, inih-cpp) | — | BSD-3-Clause | — | 0 | parity |  |
| `iniparser` | **new** | not in Hummingbird (iniparser) | — | MIT | — | 0 | **drop** |  |
| `iw` | utah `packages/iw/iw.spec` | not in Hummingbird (iw) | — | ISC AND LicenseRef-Fedora-Public-Domain | — | 0 | parity |  |
| `jomolhari-fonts` | **new** | not in Hummingbird (jomolhari-fonts) | — | OFL-1.1 | — | 0 | **drop** |  |
| `jxrlib` | **new** | not in Hummingbird (jxrlib) | — | LicenseRef-Callaway-BSD | — | 0 | **drop** |  |
| `kf5-kglobalaccel` | **new** | not in Hummingbird (kf5-kglobalaccel, kf5-kglobalaccel-libs) | — | CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kwallet` | **new** | not in Hummingbird (kf5-kwallet, kf5-kwallet-libs) | — | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-or-later AND LGPL-3.0-or-later | — | 0 | **drop** |  |
| `lame` | utah `packages/lame/lame.spec` | not in Hummingbird (lame-libs) | — | LGPL-2.0-or-later AND LGPL-2.1-or-later | — | 3 | parity |  |
| `libaribcaption` | utah `packages/libaribcaption/libaribcaption.spec` | not in Hummingbird (libaribcaption) | — | MIT | — | 1 | parity |  |
| `libasyncns` | utah `packages/libasyncns/libasyncns.spec` | not in Hummingbird (libasyncns) | — | LGPL-2.1-or-later | — | 2 | parity |  |
| `libatasmart` | utah `packages/libatasmart/libatasmart.spec` | not in Hummingbird (libatasmart) | — | LGPL-2.1-or-later | — | 1 | parity |  |
| `libblockdev` | utah `packages/libblockdev/libblockdev.spec` | not in Hummingbird (libblockdev, libblockdev-crypto) | — | LGPL-2.1-or-later | — | 2 | parity |  |
| `libbytesize` | utah `packages/libbytesize/libbytesize.spec` | not in Hummingbird (libbytesize) | — | LGPL-2.1-or-later | — | 0 | parity |  |
| `libcdio` | utah `packages/libcdio/libcdio.spec` | not in Hummingbird (libcdio) | — | GPL-3.0-or-later AND BSD-2-Clause AND LGPL-2.1-or-later | — | 0 | parity |  |
| `libcue` | **new** | not in Hummingbird (libcue) | — | GPL-2.0-only AND BSD-2-Clause | — | 0 | **drop** |  |
| `libdbusmenu` | utah `packages/libdbusmenu/libdbusmenu.spec` | not in Hummingbird (libdbusmenu, libdbusmenu-gtk3) | — | (LGPL-3.0-only OR LGPL-2.1-only) AND GPL-3.0-only | — | 1 | parity |  |
| `libdeflate` | **new** | not in Hummingbird (libdeflate) | — | MIT | — | 0 | **drop** |  |
| `libdvdread` | **new** | not in Hummingbird (libdvdread) | — | GPL-2.0-or-later AND LGPL-2.1-or-later AND (GPL-2.0-only OR GPL-3.0-only) AND LicenseRef-Fedora-Public-Domain | — | 0 | **drop** |  |
| `libebur128` | utah `packages/libebur128/libebur128.spec` | not in Hummingbird (libebur128) | — | MIT | — | 0 | parity |  |
| `libell` | **new** | not in Hummingbird (libell) | — | LGPL-2.0-or-later | — | 0 | **drop** |  |
| `libfyaml` | utah `packages/libfyaml/libfyaml.spec` | not in Hummingbird (libfyaml) | — | MIT and GPL-2.0-only and BSD-2-Clause | — | 1 | parity |  |
| `libICE` | utah `packages/libICE/libICE.spec` | not in Hummingbird (libICE) | — | MIT-open-group | autotools | 1 | parity |  |
| `liblc3` | utah `packages/liblc3/liblc3.spec` | not in Hummingbird (liblc3) | — | Apache-2.0 | — | 1 | parity |  |
| `libldac` | utah `packages/libldac/libldac.spec` | not in Hummingbird (libldac) | — | Apache-2.0 | — | 0 | parity |  |
| `libmodplug` | **new** | not in Hummingbird (libmodplug) | — | LicenseRef-Fedora-Public-Domain | — | 0 | **drop** |  |
| `libnvme` | utah `packages/libnvme/libnvme.spec` | not in Hummingbird (libnvme) | — | LGPL-2.1-or-later | — | 1 | parity |  |
| `liboping` | utah `packages/liboping/liboping.spec` | not in Hummingbird (liboping) | — | GPL-2.0-only | — | 2 | parity |  |
| `libplist` | utah `packages/libplist/libplist.spec` | not in Hummingbird (libplist) | — | LGPL-2.0-or-later | — | 0 | parity |  |
| `libportal` | utah `packages/libportal/libportal.spec` | not in Hummingbird (libportal, libportal-gtk4) | — | LGPL-3.0-only AND LGPL-2.1-or-later | Meson | 0 | parity |  |
| `librabbitmq` | **new** | not in Hummingbird (librabbitmq) | — | MIT | — | 0 | **drop** |  |
| `libreport` | **new** | not in Hummingbird (libreport-filesystem) | — | GPL-2.0-or-later | — | 0 | **drop** |  |
| `libscfg` | **new** | not in Hummingbird (libscfg) | — | MIT | — | 0 | parity |  |
| `libsecret` | utah `packages/libsecret/libsecret.spec` | not in Hummingbird (libsecret) | — | LGPL-2.1-or-later AND Apache-2.0 AND (GPL-2.0-or-later OR TGPPL-1.0) AND LicenseRef-Fedora-Public-Domain AND GCR-docs | Meson | 0 | parity |  |
| `libsigc++30` | **new** | not in Hummingbird (libsigc++30) | — | LGPL-2.1-or-later | — | 0 | **drop** |  |
| `libtalloc` | utah `packages/libtalloc/libtalloc.spec` | not in Hummingbird (libtalloc) | — | LGPL-3.0-or-later | — | 0 | parity |  |
| `libtdb` | utah `packages/libtdb/libtdb.spec` | not in Hummingbird (libtdb) | — | LGPL-3.0-or-later | — | 0 | parity |  |
| `libudfread` | **new** | not in Hummingbird (libudfread) | — | LGPL-2.0-or-later | — | 0 | **drop** |  |
| `libusbauth-configparser` | **new** | not in Hummingbird (libusbauth-configparser) | — | LicenseRef-Callaway-LGPLv2 | — | 0 | **drop** |  |
| `libutempter` | **new** | not in Hummingbird (libutempter) | — | LGPL-2.1-or-later AND LGPL-2.1-only AND BSD-2-Clause | — | 0 | parity |  |
| `libvdpau` | utah `packages/libvdpau/libvdpau.spec` | not in Hummingbird (libvdpau) | — | MIT | Meson | 1 | parity |  |
| `libvpl` | utah `packages/libvpl/libvpl.spec` | not in Hummingbird (libvpl) | — | MIT | CMake | 0 | parity |  |
| `libvpx` | utah `packages/libvpx/libvpx.spec` | not in Hummingbird (libvpx) | — | BSD-3-Clause | — | 5 | parity |  |
| `libXxf86vm` | utah `packages/libXxf86vm/libXxf86vm.spec` | not in Hummingbird (libXxf86vm) | — | X11-distribute-modifications-variant | autotools | 0 | parity |  |
| `lpcnetfreedv` | utah `packages/lpcnetfreedv/lpcnetfreedv.spec` | not in Hummingbird (lpcnetfreedv) | — | LicenseRef-Callaway-BSD | — | 1 | parity |  |
| `madan-fonts` | **new** | not in Hummingbird (madan-fonts) | — | GPL-1.0-or-later | — | 0 | **drop** |  |
| `mcstrans` | **new** | not in Hummingbird (mcstrans) | — | GPL-2.0-or-later | — | 0 | **drop** |  |
| `microcode_ctl` | utah `packages/microcode_ctl/microcode_ctl.spec` | not in Hummingbird | — | GPL-2.0-or-later AND LicenseRef-Fedora-Firmware | — | 0 | parity |  |
| `mobile-broadband-provider-info` | utah `packages/mobile-broadband-provider-info/mobile-broadband-provider-info.spec` | not in Hummingbird (mobile-broadband-provider-info) | — | CC-PDDC | — | 0 | parity |  |
| `moby-engine` | **new** | not in Hummingbird (docker-cli, moby-filesystem) | — | Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND MIT / LicenseRef-Not-Copyrightable | — | 0 | **drop** |  |
| `ModemManager` | utah `packages/ModemManager/ModemManager.spec` | not in Hummingbird (ModemManager-glib) | — | LGPL-2.1-or-later | — | 0 | parity |  |
| `mpg123` | utah `packages/mpg123/mpg123.spec` | not in Hummingbird (mpg123-libs) | — | GPL-2.0-or-later | — | 0 | parity |  |
| `noopenh264` | utah `packages/noopenh264/noopenh264.spec` | not in Hummingbird (noopenh264) | — | BSD-2-Clause and LGPL-2.1-or-later | — | 0 | parity |  |
| `open-sans-fonts` | **new** | not in Hummingbird (open-sans-fonts) | — | Apache-2.0 | — | 0 | **drop** |  |
| `openapv` | utah `packages/openapv/openapv.spec` | not in Hummingbird (openapv-libs) | — | BSD-3-Clause | — | 1 | parity |  |
| `opencore-amr` | utah `packages/opencore-amr/opencore-amr.spec` | not in Hummingbird (opencore-amr) | — | Apache-2.0 | — | 1 | parity |  |
| `openh264` | **new** | not in Hummingbird (openh264) | — | BSD-2-Clause | — | 0 | parity |  |
| `openjpeg` | utah `packages/openjpeg/openjpeg.spec` | not in Hummingbird (openjpeg) | — | BSD-2-Clause AND MIT | — | 0 | parity |  |
| `openjph` | **new** | not in Hummingbird (libopenjph) | — | BSD-2-Clause | — | 0 | **drop** |  |
| `openpgm` | **new** | not in Hummingbird (openpgm) | — | LGPL-2.1-or-later | — | 0 | **drop** |  |
| `openxr` | **new** | not in Hummingbird (openxr-libs) | — | Apache-2.0 | — | 0 | parity |  |
| `PackageKit-Qt` | **new** | not in Hummingbird (PackageKit-Qt6) | — | LGPL-2.1-only | — | 0 | **drop** |  |
| `paktype-naskh-basic-fonts` | **new** | not in Hummingbird (paktype-naskh-basic-fonts) | — | GPL-2.0-only WITH Font-exception-2.0 | — | 0 | **drop** |  |
| `passim` | utah `packages/passim/passim.spec` | not in Hummingbird (passim-libs) | — | LGPL-2.1-or-later | — | 0 | parity |  |
| `pciutils` | utah `packages/pciutils/pciutils.spec` | not in Hummingbird (pciutils-libs) | — | GPL-2.0-or-later | — | 4 | parity |  |
| `perl-Unicode-Normalize` | **new** | not in Hummingbird (perl-Unicode-Normalize) | — | GPL-1.0-or-later OR Artistic-1.0-Perl | — | 0 | **drop** |  |
| `playerctl` | **new** | not in Hummingbird (playerctl, playerctl-libs) | — | LGPL-3.0-or-later | — | 0 | parity |  |
| `polkit-qt-1` | **new** | not in Hummingbird (polkit-qt5-1, polkit-qt6-1) | — | BSD-3-Clause AND GPL-2.0-or-later AND LGPL-2.0-or-later | — | 0 | parity |  |
| `poly2tri` | **new** | not in Hummingbird (poly2tri) | — | LicenseRef-Callaway-BSD | — | 0 | **drop** |  |
| `poppler-data` | **new** | not in Hummingbird (poppler-data) | — | (GPL-2.0-only OR GPL-3.0-only) AND BSD-3-Clause | — | 0 | parity |  |
| `pugixml` | **new** | not in Hummingbird (pugixml) | — | MIT | — | 0 | **drop** |  |
| `python-aiohappyeyeballs` | **new** | not in Hummingbird (python3-aiohappyeyeballs) | — | PSF-2.0 | — | 0 | parity |  |
| `python-aiostream` | **new** | not in Hummingbird (python3-aiostream) | — | GPL-3.0-only | — | 0 | parity |  |
| `python-annotated-types` | utah `packages/python-annotated-types/python-annotated-types.spec` | not in Hummingbird (python3-annotated-types) | — | MIT | — | 0 | parity |  |
| `python-click` | **new** | not in Hummingbird (python3-click) | — | BSD-3-Clause | — | 0 | parity |  |
| `python-configobj` | **new** | not in Hummingbird (python3-configobj) | — | BSD-3-Clause | — | 0 | parity |  |
| `python-dasbus` | utah `packages/python-dasbus/python-dasbus.spec` | not in Hummingbird (python3-dasbus) | — | LGPL-2.1-or-later | — | 0 | parity |  |
| `python-distro` | utah `packages/python-distro/python-distro.spec` | not in Hummingbird (python3-distro) | — | Apache-2.0 | — | 0 | parity |  |
| `python-docopt` | **new** | not in Hummingbird (python3-docopt) | — | MIT | — | 0 | parity |  |
| `python-evdev` | utah `packages/python-evdev/python-evdev.spec` | not in Hummingbird (python3-evdev) | — | BSD-3-Clause | — | 0 | parity |  |
| `python-frozenlist` | **new** | not in Hummingbird (python3-frozenlist) | — | Apache-2.0 | — | 0 | parity |  |
| `python-lxml` | **new** | not in Hummingbird (python3-lxml) | — | BSD-3-Clause AND MIT-CMU AND MIT | — | 0 | parity |  |
| `python-multidict` | **new** | not in Hummingbird (python3-multidict) | — | Apache-2.0 | — | 0 | parity |  |
| `python-oauthlib` | **new** | not in Hummingbird (python3-oauthlib) | — | BSD-3-Clause | — | 0 | parity |  |
| `python-propcache` | **new** | not in Hummingbird (python3-propcache) | — | Apache-2.0 | — | 0 | parity |  |
| `python-psutil` | utah `packages/python-psutil/python-psutil.spec` | not in Hummingbird (python3-psutil) | — | BSD-3-Clause | — | 0 | parity |  |
| `python-pydantic-core` | utah `packages/python-pydantic-core/python-pydantic-core.spec` | not in Hummingbird (python3-pydantic-core) | — | (MIT OR Apache-2.0) AND MIT AND Unicode-3.0 AND Unicode-DFS-2016 AND (Apache-2.0 OR BSL-1.0) AND (BSD-2-Clause OR Apache-2.0 OR MIT) AND (Unlicense OR MIT) | — | 3 | parity |  |
| `python-pydbus` | **new** | not in Hummingbird (python3-pydbus) | — | LicenseRef-Callaway-LGPLv2+ | — | 0 | parity |  |
| `python-six` | **new** | not in Hummingbird (python3-six) | — | MIT | — | 0 | **drop** |  |
| `python-typing-inspection` | utah `packages/python-typing-inspection/python-typing-inspection.spec` | not in Hummingbird (python3-typing-inspection) | — | MIT | — | 0 | parity |  |
| `python-tzlocal` | **new** | not in Hummingbird (python3-tzlocal) | — | MIT | — | 0 | parity |  |
| `python-wcwidth` | **new** | not in Hummingbird (python3-wcwidth) | — | MIT AND HPND-Markus-Kuhn | — | 0 | **drop** |  |
| `pytz` | **new** | not in Hummingbird (python3-pytz) | — | MIT | — | 0 | **drop** |  |
| `pyxdg` | **new** | not in Hummingbird (python3-pyxdg) | — | LGPL-2.0-only | — | 0 | **drop** |  |
| `qemu` | **new** | not in Hummingbird (qemu-user-static-aarch64, qemu-user-static-arm) | — | Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND FSFAP AND GPL-1.0-or-later AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-2.0-or-later WITH GCC-exception-2.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND MIT AND LicenseRef-Fedora-Public-Domain AND CC-BY-3.0 | — | 0 | **drop** |  |
| `qt5-qtspeech` | **new** | not in Hummingbird (qt5-qtspeech) | — | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | — | 0 | **drop** |  |
| `qt5-qtsvg` | **new** | not in Hummingbird (qt5-qtsvg) | — | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | — | 0 | **drop** |  |
| `qt5-qtx11extras` | **new** | not in Hummingbird (qt5-qtx11extras) | — | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | — | 0 | **drop** |  |
| `rit-meera-new-fonts` | **new** | not in Hummingbird (rit-meera-new-fonts) | — | OFL-1.1 | — | 0 | **drop** |  |
| `rit-rachana-fonts` | **new** | not in Hummingbird (rit-rachana-fonts) | — | OFL-1.1 | — | 0 | **drop** |  |
| `rsync` | **new** | not in Hummingbird | — | GPL-3.0-or-later | — | 0 | parity |  |
| `runc` | utah `packages/runc/runc.spec` | not in Hummingbird (runc) | — | Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND MIT AND MPL-2.0 | Go | 0 | parity |  |
| `rust-just` | utah `packages/rust-just/rust-just.spec` | not in Hummingbird (just) | — | CC0-1.0 AND Apache-2.0 AND (Apache-2.0 OR Apache-2.0 WITH LLVM-exception) AND (Apache-2.0 OR MIT) AND (Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT) AND BSD-2-Clause AND MIT AND (MIT-0 OR Apache-2.0) AND MPL-2.0 AND Unicode-DFS-2016 AND (Unlicense OR MIT) | — | 0 | parity |  |
| `sbc` | utah `packages/sbc/sbc.spec` | not in Hummingbird (libsbc) | — | GPL-2.0-only AND LGPL-2.1-or-later | — | 0 | parity |  |
| `sdubby` | **new** | not in Hummingbird (sdubby) | — | GPL-2.0-or-later | — | 0 | **drop** |  |
| `setools` | utah `packages/setools/setools.spec` | not in Hummingbird (python3-setools) | — | LGPL-2.1-only | — | 0 | parity |  |
| `sil-padauk-fonts` | **new** | not in Hummingbird (sil-padauk-fonts) | — | OFL-1.1 | — | 0 | **drop** |  |
| `slang` | **new** | not in Hummingbird (slang) | — | GPL-2.0-or-later | — | 0 | **drop** |  |
| `smartmontools` | **new** | not in Hummingbird (smartmontools-selinux) | — | GPL-2.0-or-later | — | 0 | **drop** |  |
| `snowball` | utah `packages/snowball/snowball.spec` | not in Hummingbird (libstemmer) | — | BSD-3-Clause | — | 1 | parity |  |
| `sound-theme-freedesktop` | utah `packages/sound-theme-freedesktop/sound-theme-freedesktop.spec` | not in Hummingbird (sound-theme-freedesktop) | — | GPL-2.0-only AND GPL-2.0-or-later AND LGPL-2.0-or-later AND CC-BY-SA-3.0 AND CC-BY-3.0 AND CC-BY-4.0 | — | 0 | parity |  |
| `soxr` | utah `packages/soxr/soxr.spec` | not in Hummingbird (soxr) | — | LGPL-2.1-or-later | — | 1 | parity |  |
| `spandsp` | utah `packages/spandsp/spandsp.spec` | not in Hummingbird (spandsp) | — | LGPL-2.1-only AND GPL-2.0-only | — | 0 | parity |  |
| `speex` | utah `packages/speex/speex.spec` | not in Hummingbird (speex) | — | BSD-3-clause AND TU-Berlin-1.0 | — | 1 | parity |  |
| `spirv-tools` | utah `packages/spirv-tools/spirv-tools.spec` | not in Hummingbird (spirv-tools-libs) | — | Apache-2.0 | Meson | 0 | parity |  |
| `srt` | **new** | not in Hummingbird (srt-libs) | — | MPL-2.0 | — | 0 | **drop** |  |
| `stix-fonts` | utah `packages/stix-fonts/stix-fonts.spec` | not in Hummingbird (stix-fonts) | — | OFL-1.1 | — | 0 | parity |  |
| `tailscale` | utah `packages/tailscale/tailscale.spec` | not in Hummingbird | — | BSD-2-Clause AND MIT AND Apache-2.0 AND MPL-2.0 AND GPL-3.0-or-later AND ISC AND 0BSD AND BSD-3-Clause | Go | 0 | parity |  |
| `twolame` | utah `packages/twolame/twolame.spec` | not in Hummingbird (twolame-libs) | — | LGPL-2.1-or-later | — | 0 | parity |  |
| `uchardet` | **new** | not in Hummingbird (uchardet) | — | MPL-1.1 OR GPL-2.0-or-later OR LGPL-2.0-or-later | — | 0 | **drop** |  |
| `vazirmatn-fonts` | **new** | not in Hummingbird (vazirmatn-vf-fonts) | — | OFL-1.1 | — | 0 | **drop** |  |
| `vo-amrwbenc` | utah `packages/vo-amrwbenc/vo-amrwbenc.spec` | not in Hummingbird (vo-amrwbenc) | — | Apache-2.0 | — | 0 | parity |  |
| `volume_key` | utah `packages/volume_key/volume_key.spec` | not in Hummingbird (volume_key-libs) | — | GPL-2.0-only AND (MPL-1.1 OR GPL-2.0-or-later OR LGPL-2.1-or-later) | — | 2 | parity |  |
| `waypipe` | utah `packages/waypipe/waypipe.spec` | not in Hummingbird | — | GPL-3.0-or-later AND (Apache-2.0 OR MIT) AND ISC AND MIT | Meson (Rust) | 0 | parity |  |
| `wireguard-tools` | utah `packages/wireguard-tools/wireguard-tools.spec` | not in Hummingbird | — | GPL-2.0-only | — | 0 | parity |  |
| `wpa_supplicant` | utah `packages/wpa_supplicant/wpa_supplicant.spec` | not in Hummingbird (wpa_supplicant) | — | BSD-3-Clause | — | 14 | parity |  |
| `wsdd` | utah `packages/wsdd/wsdd.spec` | not in Hummingbird (wsdd) | — | MIT | — | 1 | parity |  |
| `xcb-util-keysyms` | utah `packages/xcb-util-keysyms/xcb-util-keysyms.spec` | not in Hummingbird (xcb-util-keysyms) | — | X11-distribute-modifications-variant | autotools | 0 | parity |  |
| `xcb-util-wm` | utah `packages/xcb-util-wm/xcb-util-wm.spec` | not in Hummingbird (xcb-util-wm) | — | X11-distribute-modifications-variant | autotools | 0 | parity |  |
| `xdg-dbus-proxy` | utah `packages/xdg-dbus-proxy/xdg-dbus-proxy.spec` | not in Hummingbird (xdg-dbus-proxy) | — | LGPL-2.1-or-later | Meson | 0 | parity |  |
| `xdg-terminal-exec` | utah `packages/xdg-terminal-exec/xdg-terminal-exec.spec` | not in Hummingbird | — | GPL-3.0-or-later | — | 0 | parity |  |
| `xevd` | utah `packages/xevd/xevd.spec` | not in Hummingbird (xevd-libs) | — | BSD-3-Clause | — | 1 | parity |  |
| `xeve` | utah `packages/xeve/xeve.spec` | not in Hummingbird (xeve-libs) | — | BSD-3-Clause | — | 1 | parity |  |
| `xmlrpc-c` | utah `packages/xmlrpc-c/xmlrpc-c.spec` | not in Hummingbird (xmlrpc-c, xmlrpc-c-client) | — | LicenseRef-Callaway-BSD AND LicenseRef-Callaway-MIT | — | 2 | parity |  |
| `xvidcore` | utah `packages/xvidcore/xvidcore.spec` | not in Hummingbird (xvidcore) | — | GPL-2.0-or-later | — | 2 | parity |  |
| `yajl` | **new** | not in Hummingbird (yajl) | — | ISC | — | 0 | **drop** |  |
| `yyjson` | utah `packages/yyjson/yyjson.spec` | not in Hummingbird (yyjson) | — | MIT | — | 0 | parity |  |
| `zram-generator` | **new** | not in Hummingbird | — | MIT AND (MIT OR Apache-2.0) | — | 0 | parity |  |
| `zsh` | utah `packages/zsh/zsh.spec` | not in Hummingbird | — | MIT-Modern-Variant AND ISC AND GPL-2.0-only | — | 0 | parity |  |
| `zvbi` | utah `packages/zvbi/zvbi.spec` | not in Hummingbird (zvbi) | — | GPL-2.0-or-later AND LGPL-2.0-or-later AND LGPL-2.1-or-later AND BSD-2-Clause AND MIT | — | 2 | parity |  |


### Wave 1 (74 recipes)

| Package | Reuse / New | Why owned | Key deps | License | Build | Patches | Scope | Hazards / notes |
|---|---|---|---|---|---|---:|---|---|
| `adwaita-icon-theme` | utah `packages/adwaita-icon-theme/adwaita-icon-theme.spec` | not in Hummingbird (adwaita-cursor-theme, adwaita-icon-theme) | adwaita-icon-theme-legacy | LGPL-3.0-only OR CC-BY-SA-3.0 | Meson/data | 0 | core |  |
| `at-spi2-core` | utah `packages/at-spi2-core/at-spi2-core.spec` | not in Hummingbird (at-spi2-atk, at-spi2-core) | xprop | LGPL-2.1-or-later | — | 0 | core |  |
| `cairo` | utah `packages/cairo/cairo.spec` | not in Hummingbird (cairo, cairo-gobject) | pixman | LGPL-2.1-only OR MPL-1.1 | Meson | 1 | core |  |
| `glib-networking` | utah `packages/glib-networking/glib-networking.spec` | not in Hummingbird (glib-networking) | gsettings-desktop-schemas, libproxy | LGPL-2.1-or-later WITH cryptsetup-OpenSSL-exception | Meson | 0 | core |  |
| `kf6` | **new** | not in Hummingbird (kf6-filesystem) | kde-filesystem | BSD-3-Clause | — | 0 | core |  |
| `libgusb` | utah `packages/libgusb/libgusb.spec` | not in Hummingbird (libgusb) | json-glib | LGPL-2.1-or-later | — | 0 | core |  |
| `libpciaccess` | utah `packages/libpciaccess/libpciaccess.spec` | not in Hummingbird (libpciaccess) | hwdata | HPND AND MIT | autotools | 1 | core |  |
| `libthai` | utah `packages/libthai/libthai.spec` | not in Hummingbird (libthai) | libdatrie | LGPL-2.1-or-later | — | 2 | core |  |
| `libtheora` | utah `packages/libtheora/libtheora.spec` | not in Hummingbird (libtheora) | libogg | BSD-3-Clause | — | 3 | core |  |
| `libvorbis` | utah `packages/libvorbis/libvorbis.spec` | not in Hummingbird (libvorbis) | libogg | BSD-3-Clause | — | 9 | core |  |
| `libwacom` | utah `packages/libwacom/libwacom.spec` | not in Hummingbird (libwacom, libwacom-data) | libevdev, libgudev | HPND | Meson | 0 | core |  |
| `libXcursor` | utah `packages/libXcursor/libXcursor.spec` | not in Hummingbird (libXcursor) | libXfixes | HPND-sell-variant | autotools | 0 | core |  |
| `libXdamage` | utah `packages/libXdamage/libXdamage.spec` | not in Hummingbird (libXdamage) | libXfixes | HPND-sell-variant | autotools | 0 | core |  |
| `wl-clipboard` | utah `packages/wl-clipboard/wl-clipboard.spec` | not in Hummingbird (wl-clipboard) | wayland | GPL-3.0-or-later | — | 0 | core |  |
| `xcb-util-image` | utah `packages/xcb-util-image/xcb-util-image.spec` | not in Hummingbird (xcb-util-image) | xcb-util | X11-distribute-modifications-variant | autotools | 0 | core |  |
| `xdg-utils` | **new** | not in Hummingbird (xdg-utils) | desktop-file-utils | MIT | — | 0 | core |  |
| `xkbcomp` | utah `packages/xkbcomp/xkbcomp.spec` | not in Hummingbird (xkbcomp) | libxkbfile | MIT-open-group AND HPND-DEC | autotools | 0 | core |  |
| `assimp` | **new** | not in Hummingbird (assimp) | poly2tri, pugixml | BSD-3-Clause AND MIT AND BSL-1.0 AND Unlicense AND Zlib | — | 0 | **drop** |  |
| `codec2` | utah `packages/codec2/codec2.spec` | not in Hummingbird (codec2) | lpcnetfreedv | LGPL-2.1-only | — | 0 | parity |  |
| `distrobox` | utah `packages/distrobox/distrobox.spec` | not in Hummingbird | hicolor-icon-theme, moby-engine | GPL-3.0-only | — | 0 | parity |  |
| `docbook-style-xsl` | **new** | not in Hummingbird (docbook-style-xsl) | docbook-dtds | LicenseRef-DMIT | — | 0 | **drop** |  |
| `exiv2` | utah `packages/exiv2/exiv2.spec` | not in Hummingbird (exiv2-libs) | inih | GPL-2.0-or-later AND BSD-3-Clause AND LicenseRef-Fedora-Public-Domain | — | 0 | parity |  |
| `fastfetch` | utah `packages/fastfetch/fastfetch.spec` | not in Hummingbird | yyjson | MIT | — | 0 | parity |  |
| `flac` | utah `packages/flac/flac.spec` | not in Hummingbird (flac-libs) | libogg | BSD-3-Clause AND GPL-2.0-or-later AND GFDL-1.3-or-later | — | 0 | parity |  |
| `glibmm2.68` | **new** | not in Hummingbird (glibmm2.68) | libsigc++30 | LGPL-2.1-or-later AND GPL-2.0-or-later | — | 0 | **drop** |  |
| `grubby` | **new** | not in Hummingbird (grubby) | grub2 | GPL-2.0-or-later | — | 0 | **drop** |  |
| `iwd` | **new** | not in Hummingbird (iwd) | libell | LGPL-2.1-or-later | — | 0 | **drop** |  |
| `jpegxl` | utah `packages/jpegxl/jpegxl.spec` | not in Hummingbird (libjxl) | highway, shared-mime-info | BSD-3-Clause AND Apache-2.0 AND Zlib | — | 0 | parity |  |
| `kf5` | utah `packages/kf5/kf5.spec` | not in Hummingbird (kf5-filesystem) | kde-filesystem | BSD-3-Clause | — | 0 | parity |  |
| `kf6-kirigami` | **new** | not in Hummingbird (kf6-kirigami) | qt6-qtdeclarative | BSD-3-Clause AND CC0-1.0 AND FSFAP AND GPL-2.0-or-later AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) AND MIT | CMake | 0 | parity | KF6 version must match the rest of the kf6 closure. |
| `libbluray` | **new** | not in Hummingbird (libbluray) | libudfread | LGPL-2.0-or-later | — | 0 | **drop** |  |
| `libcdio-paranoia` | utah `packages/libcdio-paranoia/libcdio-paranoia.spec` | not in Hummingbird (libcdio-paranoia) | libcdio | GPL-3.0-or-later | — | 1 | parity |  |
| `libdvdnav` | **new** | not in Hummingbird (libdvdnav) | libdvdread | GPL-2.0-or-later AND LGPL-2.1-or-later | — | 0 | **drop** |  |
| `libheif` | utah `packages/libheif/libheif.spec` | not in Hummingbird (libheif) | noopenh264, openh264, openjpeg | LGPL-3.0-or-later and MIT | — | 2 | parity |  |
| `libimobiledevice-glue` | utah `packages/libimobiledevice-glue/libimobiledevice-glue.spec` | not in Hummingbird (libimobiledevice-glue) | libplist | LGPL-2.1-or-later | — | 0 | parity |  |
| `libjcat` | **new** | not in Hummingbird (libjcat) | json-glib | LGPL-2.1-or-later | — | 0 | parity |  |
| `libratbag` | utah `packages/libratbag/libratbag.spec` | not in Hummingbird (libratbag-ratbagd) | libevdev, python-evdev | MIT | — | 0 | parity |  |
| `LibRaw` | **new** | not in Hummingbird (LibRaw) | lcms2 | BSD-3-Clause and (CDDL-1.0 or LGPL-2.1-only) | — | 0 | **drop** |  |
| `librist` | **new** | not in Hummingbird (librist) | cjson | BSD-2-Clause and ISC | — | 0 | **drop** |  |
| `libSM` | utah `packages/libSM/libSM.spec` | not in Hummingbird (libSM) | libICE | MIT AND MIT-open-group | autotools | 0 | parity |  |
| `libtevent` | utah `packages/libtevent/libtevent.spec` | not in Hummingbird (libtevent) | libtalloc | LGPL-3.0-or-later | — | 0 | parity |  |
| `libxmlb` | utah `packages/libxmlb/libxmlb.spec` | not in Hummingbird (libxmlb) | shared-mime-info | LGPL-2.1-or-later | — | 0 | parity |  |
| `lm_sensors` | utah `packages/lm_sensors/lm_sensors.spec` | not in Hummingbird (lm_sensors-libs) | dmidecode | LGPL-2.1-or-later | — | 5 | parity |  |
| `malcontent` | utah `packages/malcontent/malcontent.spec` | not in Hummingbird (malcontent-libs) | accountsservice | LGPL-2.1-only AND CC-BY-3.0 | — | 0 | parity |  |
| `mdadm` | utah `packages/mdadm/mdadm.spec` | not in Hummingbird (mdadm) | libreport | GPL-2.0-or-later | — | 4 | parity |  |
| `mesa-demos` | utah `packages/mesa-demos/mesa-demos.spec` | not in Hummingbird (glx-utils) | libglvnd | MIT | — | 2 | parity |  |
| `newt` | **new** | not in Hummingbird (newt) | slang | LGPL-2.0-only | — | 0 | **drop** |  |
| `openexr` | **new** | not in Hummingbird (openexr-libs) | imath, libdeflate | BSD-3-Clause WITH AdditionRef-OpenEXR-Additional-IP-Rights-Grant OR Apache-2.0 | — | 0 | **drop** |  |
| `os-prober` | **new** | not in Hummingbird (os-prober) | grub2 | GPL-2.0-or-later AND GPL-1.0-or-later | — | 0 | **drop** |  |
| `osinfo-db` | **new** | not in Hummingbird (osinfo-db) | hwdata | GPL-2.0-or-later | — | 0 | **drop** |  |
| `pinentry` | **new** | not in Hummingbird (pinentry, pinentry-gnome3) | libsecret | GPL-2.0-or-later | — | 0 | parity |  |
| `pipewire-media-session` | **new** | not in Hummingbird (pipewire-media-session) | pipewire | MIT | — | 0 | **drop** |  |
| `power-profiles-daemon` | **new** | not in Hummingbird | libgudev | GPL-3.0-or-later | — | 0 | parity |  |
| `python-aiosignal` | **new** | not in Hummingbird (python3-aiosignal) | python-frozenlist | Apache-2.0 | — | 0 | parity |  |
| `python-click-log` | **new** | not in Hummingbird (python3-click-log) | python-click | MIT | — | 0 | parity |  |
| `python-click-threading` | **new** | not in Hummingbird (python3-click-threading) | python-click | MIT | — | 0 | parity |  |
| `python-dateutil` | **new** | not in Hummingbird (python3-dateutil) | python-six | (Apache-2.0 AND BSD-3-Clause) OR BSD-3-Clause | — | 0 | parity |  |
| `python-pydantic` | utah `packages/python-pydantic/python-pydantic.spec` | not in Hummingbird (python3-pydantic) | python-annotated-types, python-pydantic-core, python-typing-inspection | MIT | — | 2 | parity |  |
| `python-urwid` | **new** | not in Hummingbird (python3-urwid) | python-wcwidth | LGPL-2.1-or-later AND MIT | — | 0 | parity |  |
| `python-yarl` | **new** | not in Hummingbird (python3-yarl) | python-multidict, python-propcache | Apache-2.0 | — | 0 | parity |  |
| `qt5-qtdeclarative` | **new** | not in Hummingbird (qt5-qtdeclarative) | libglvnd | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | — | 0 | **drop** |  |
| `qt6-qtquicktimeline` | **new** | not in Hummingbird (qt6-qtquicktimeline) | qt6-qtdeclarative | GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | 0 | parity |  |
| `shaderc` | **new** | not in Hummingbird (libshaderc) | spirv-tools | Apache-2.0 | — | 0 | parity |  |
| `tmux` | **new** | not in Hummingbird | libutempter | ISC AND BSD-2-Clause AND BSD-3-Clause AND SSH-short AND LicenseRef-Fedora-Public-Domain | — | 0 | parity |  |
| `totem-pl-parser` | **new** | not in Hummingbird (totem-pl-parser) | uchardet | LicenseRef-Callaway-LGPLv2+ | — | 0 | **drop** |  |
| `udiskie` | **new** | not in Hummingbird (python3-udiskie) | hicolor-icon-theme | MIT | — | 0 | parity |  |
| `udisks2` | utah `packages/udisks2/udisks2.spec` | not in Hummingbird (libudisks2, udisks2) | libblockdev, libgudev | GPL-2.0-or-later / LGPL-2.0-or-later | Meson | 0 | parity |  |
| `upower` | utah `packages/upower/upower.spec` | not in Hummingbird (upower-libs) | gobject-introspection | GPL-2.0-or-later | Meson | 0 | parity |  |
| `usbauth` | **new** | not in Hummingbird (usbauth) | libusbauth-configparser | GPL-2.0-only | — | 0 | **drop** |  |
| `vali` | **new** | not in Hummingbird (vali) | aml | MIT | — | 0 | parity |  |
| `webrtc-audio-processing` | utah `packages/webrtc-audio-processing/webrtc-audio-processing.spec` | not in Hummingbird (webrtc-audio-processing) | abseil-cpp | BSD-3-Clause | — | 3 | parity |  |
| `wev` | **new** | not in Hummingbird | wayland | MIT | — | 0 | parity |  |
| `wireless-regdb` | utah `packages/wireless-regdb/wireless-regdb.spec` | not in Hummingbird (wireless-regdb) | iw | ISC | — | 0 | parity |  |
| `wtype` | **new** | not in Hummingbird | wayland | MIT | — | 0 | parity |  |


### Wave 2 (79 recipes)

| Package | Reuse / New | Why owned | Key deps | License | Build | Patches | Scope | Hazards / notes |
|---|---|---|---|---|---|---:|---|---|
| `colord` | utah `packages/colord/colord.spec` | not in Hummingbird (colord-libs) | lcms2, libgusb | GPL-2.0-or-later AND LGPL-2.1-or-later | Meson | 0 | core |  |
| `gssdp` | utah `packages/gssdp/gssdp.spec` | not in Hummingbird (gssdp) | libsoup3 | LicenseRef-Callaway-LGPLv2+ | Meson | 0 | core |  |
| `gstreamer1-plugins-base` | utah `packages/gstreamer1-plugins-base/gstreamer1-plugins-base.spec` | not in Hummingbird (gstreamer1-plugins-base) | cairo, cdparanoia, graphene | LGPL-2.1-or-later | — | 1 | core |  |
| `kf6-karchive` | **new** | not in Hummingbird (kf6-karchive) | kf6 | LGPL-2.0-or-later AND BSD-2-Clause | — | 0 | core |  |
| `kf6-kcolorscheme` | **new** | not in Hummingbird (kf6-kcolorscheme) | kf6, kf6-kconfig, kf6-kguiaddons | BSD-2-Clause and CC0-1.0 and LGPL-2.0-or-later and LGPL-2.1-only and LGPL-3.0-only and (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | core |  |
| `kf6-kconfig` | **new** | not in Hummingbird (kf6-kconfig) | kf6, qt6-qtdeclarative | BSD-2-Clause AND BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND MIT | CMake | 0 | core |  |
| `kf6-kguiaddons` | **new** | not in Hummingbird (kf6-kguiaddons) | kf6, qt6-qtdeclarative, wayland | BSD-2-Clause AND CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only | — | 0 | core |  |
| `kf6-ki18n` | **new** | not in Hummingbird (kf6-ki18n) | iso-codes, kf6, qt6-qtdeclarative | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) AND ODbl-1.0 | — | 0 | core |  |
| `kf6-kwidgetsaddons` | **new** | not in Hummingbird (kf6-kwidgetsaddons) | kf6 | BSD-3-Clause AND CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND LGPL-3.0-or-later | — | 0 | core |  |
| `libdrm` | utah `packages/libdrm/libdrm.spec` | not in Hummingbird (libdrm) | libpciaccess | MIT | Meson | 2 | core |  |
| `libinput` | utah `packages/libinput/libinput.spec` | not in Hummingbird (libinput) | libevdev, libwacom, mtdev | MIT | Meson | 0 | core | ABI-sensitive; niri links it. Rebuild against Hummingbird's libevdev/mtdev. |
| `libsoup3` | utah `packages/libsoup3/libsoup3.spec` | not in Hummingbird (libsoup3) | glib-networking | LGPL-2.0-or-later AND LGPL-2.1-or-later | Meson | 2 | core |  |
| `pango` | utah `packages/pango/pango.spec` | not in Hummingbird (pango) | cairo, fribidi, libthai | LGPL-2.0-or-later | Meson | 0 | core |  |
| `xcb-util-cursor` | **new** | not in Hummingbird (xcb-util-cursor) | xcb-util-image, xcb-util-renderutil | X11-distribute-modifications-variant | autotools | 0 | core |  |
| `appstream` | utah `packages/appstream/appstream.spec` | not in Hummingbird (appstream, appstream-qt) | appstream-data, libfyaml, libxmlb | GPL-2.0-or-later AND LGPL-2.1-or-later | — | 0 | parity |  |
| `cairomm1.16` | **new** | not in Hummingbird (cairomm1.16) | cairo, libsigc++30 | LGPL-2.0-or-later | — | 0 | **drop** |  |
| `ddcutil` | utah `packages/ddcutil/ddcutil.spec` | not in Hummingbird | hwdata, i2c-tools, libXrandr | GPL-2.0-or-later | — | 0 | parity |  |
| `fwupd` | utah `packages/fwupd/fwupd.spec` | not in Hummingbird | libdrm, libjcat, libxmlb | LGPL-2.1-or-later | — | 0 | parity |  |
| `gvfs` | utah `packages/gvfs/gvfs.spec` | not in Hummingbird (gvfs, gvfs-client) | gcr, gsettings-desktop-schemas, libbluray | LGPL-2.0-or-later AND GPL-3.0-only AND MPL-2.0 AND BSD-3-Clause-Sun | — | 0 | parity |  |
| `igt-gpu-tools` | utah `packages/igt-gpu-tools/igt-gpu-tools.spec` | not in Hummingbird | cairo, gsl, libdrm | (MIT AND ISC) AND (GPL-1.0-or-later WITH Linux-syscall-note) AND (GPL-2.0-only OR MIT) AND (GPL-2.0-only WITH Linux-syscall-note OR MIT) AND GPL-2.0-or-later WITH Linux-syscall-note AND HPND-sell-variant AND ICU AND ISC AND (MIT AND LGPL-3.0-or-later) AND X11 | — | 0 | parity |  |
| `kanshi` | **new** | not in Hummingbird | libscfg, vali, wayland | MIT | — | 0 | parity |  |
| `kdecoration` | **new** | not in Hummingbird (kdecoration) | kf6, kf6-ki18n | LGPL-3.0-only AND LGPL-2.1-only AND CC0-1.0 | — | 0 | **drop** |  |
| `kf5-attica` | **new** | not in Hummingbird (kf5-attica) | kf5 | CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-karchive` | **new** | not in Hummingbird (kf5-karchive) | kf5 | BSD-2-Clause AND CC0-1.0 AND LGPL-2.0-or-later | — | 0 | **drop** |  |
| `kf5-kauth` | **new** | not in Hummingbird (kf5-kauth) | kf5-kcoreaddons, polkit-qt-1 | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-or-later | — | 0 | **drop** |  |
| `kf5-kcodecs` | **new** | not in Hummingbird (kf5-kcodecs) | kf5 | CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-or-later AND MIT AND MPL-1.1 | — | 0 | **drop** |  |
| `kf5-kcoreaddons` | **new** | not in Hummingbird (kf5-kcoreaddons) | kf5 | BSD-2-Clause AND BSD-3-Clause AND CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kcrash` | **new** | not in Hummingbird (kf5-kcrash) | kf5-kcoreaddons, qt5-qtx11extras | CC0-1.0 AND LGPL-2.0-or-later | — | 0 | **drop** |  |
| `kf5-kdbusaddons` | **new** | not in Hummingbird (kf5-kdbusaddons) | kf5, qt5-qtx11extras | CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kdoctools` | **new** | not in Hummingbird (kf5-kdoctools) | docbook-dtds, docbook-style-xsl, kf5-karchive | BSD-3-Clause AND CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kguiaddons` | **new** | not in Hummingbird (kf5-kguiaddons) | kf5, qt5-qtwayland, qt5-qtx11extras | BSD-2-Clause AND CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-ki18n` | **new** | not in Hummingbird (kf5-ki18n) | kf5, qt5-qtdeclarative | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) AND ODbL-1.0 | — | 0 | **drop** |  |
| `kf5-kitemviews` | **new** | not in Hummingbird (kf5-kitemviews) | kf5 | CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later | — | 0 | **drop** |  |
| `kf5-kjobwidgets` | **new** | not in Hummingbird (kf5-kjobwidgets) | kf5-kcoreaddons, kf5-kwidgetsaddons, qt5-qtx11extras | CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kpackage` | **new** | not in Hummingbird (kf5-kpackage) | kf5-karchive, kf5-kcoreaddons, kf5-ki18n | CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-or-later | — | 0 | **drop** |  |
| `kf5-kwidgetsaddons` | **new** | not in Hummingbird (kf5-kwidgetsaddons) | kf5 | CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND LGPL-3.0-or-later AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kwindowsystem` | **new** | not in Hummingbird (kf5-kwindowsystem) | kf5, libXfixes, qt5-qtx11extras | CC0-1.0 AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) AND MIT | — | 0 | **drop** |  |
| `kf5-sonnet` | **new** | not in Hummingbird (kf5-sonnet-core, kf5-sonnet-ui) | qt5-qtdeclarative | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-or-later | — | 0 | **drop** |  |
| `kf5-syndication` | **new** | not in Hummingbird (kf5-syndication) | kf5-kcodecs | BSD-2-Clause AND CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-or-later | — | 0 | **drop** |  |
| `kf6-attica` | **new** | not in Hummingbird (kf6-attica) | kf6 | CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | parity |  |
| `kf6-kbookmarks` | **new** | not in Hummingbird (kf6-kbookmarks) | kf6, kf6-kconfig, kf6-kcoreaddons | CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-3.0-only AND LicenseRef-KDE-Accepted-LGPL | — | 0 | parity |  |
| `kf6-kcodecs` | **new** | not in Hummingbird (kf6-kcodecs) | kf6 | BSD-3-Clause AND CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-or-later AND MIT AND MPL-1.1 | — | 0 | parity |  |
| `kf6-kcompletion` | **new** | not in Hummingbird (kf6-kcompletion) | kf6-kcodecs, kf6-kconfig, kf6-kwidgetsaddons | CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-or-later | — | 0 | parity |  |
| `kf6-kcoreaddons` | **new** | not in Hummingbird (kf6-kcoreaddons) | kf6, qt6-qtdeclarative | BSD-2-Clause AND BSD-3-Clause AND CC0-1.0 AND GPL-2.0-or-later AND MPL-1.1 AND LGPL-2.0-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND LGPL-2.1-only WITH Qt-LGPL-exception-1.1 | CMake | 0 | parity |  |
| `kf6-kcrash` | **new** | not in Hummingbird (kf6-kcrash) | kf6-kcoreaddons | CC0-1.0 AND LGPL-2.0-or-later | — | 0 | parity |  |
| `kf6-kdbusaddons` | **new** | not in Hummingbird (kf6-kdbusaddons) | kf6 | CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only | — | 0 | parity |  |
| `kf6-kdoctools` | **new** | not in Hummingbird (kf6-kdoctools) | docbook-dtds, docbook-style-xsl, kf6-karchive | BSD-3-Clause AND CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | parity |  |
| `kf6-kglobalaccel` | **new** | not in Hummingbird (kf6-kglobalaccel) | kf6 | CC0-1.0 AND LGPL-2.0-or-later | — | 0 | parity |  |
| `kf6-kimageformats` | **new** | not in Hummingbird | LibRaw, imath, jpegxl | LGPLv2+ | — | 0 | parity | DMS 'doctor' expects it; KF6 coupling. |
| `kf6-kitemviews` | **new** | not in Hummingbird (kf6-kitemviews) | kf6 | CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later | — | 0 | parity |  |
| `kf6-kpackage` | **new** | not in Hummingbird (kf6-kpackage) | kf6, kf6-karchive, kf6-kcoreaddons | CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-or-later | — | 0 | parity |  |
| `kf6-kservice` | **new** | not in Hummingbird (kf6-kservice) | kf6, kf6-kconfig, kf6-kcoreaddons | CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | parity |  |
| `kf6-kwindowsystem` | **new** | not in Hummingbird (kf6-kwindowsystem) | kf6, libXfixes, qt6-qtdeclarative | CC0-1.0 AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND MIT | — | 0 | parity |  |
| `kf6-sonnet` | **new** | not in Hummingbird (kf6-sonnet) | kf6, qt6-qtdeclarative | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-or-later | — | 0 | parity |  |
| `kf6-syndication` | **new** | not in Hummingbird (kf6-syndication) | kf6, kf6-kcodecs | BSD-2-Clause AND CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-or-later | — | 0 | parity |  |
| `libgexiv2` | utah `packages/libgexiv2/libgexiv2.spec` | not in Hummingbird (libgexiv2) | exiv2 | GPL-2.0-or-later | — | 0 | **drop** |  |
| `libgxps` | **new** | not in Hummingbird (libgxps) | cairo, lcms2 | LGPL-2.1-or-later | — | 0 | parity |  |
| `libimobiledevice` | utah `packages/libimobiledevice/libimobiledevice.spec` | not in Hummingbird (libimobiledevice) | libimobiledevice-glue, libplist, libusbmuxd | LGPL-2.0-or-later / LGPL-2.0-or-later AND MIT AND Zlib | — | 0 | parity |  |
| `libopenmpt` | **new** | not in Hummingbird (libopenmpt) | libogg, libvorbis, mpg123 | BSD-3-Clause | — | 0 | **drop** |  |
| `libsndfile` | utah `packages/libsndfile/libsndfile.spec` | not in Hummingbird (libsndfile) | flac, gsm, lame | LGPL-2.1-or-later AND GPL-2.0-or-later AND BSD-3-Clause | — | 4 | parity |  |
| `libusbmuxd` | utah `packages/libusbmuxd/libusbmuxd.spec` | not in Hummingbird (libusbmuxd) | libimobiledevice-glue, libplist | LGPL-2.0-or-later AND GPL-2.0-or-later | — | 0 | parity |  |
| `libva` | utah `packages/libva/libva.spec` | not in Hummingbird (libva) | libXfixes, libdrm, libglvnd | MIT AND HPND-sell-variant AND ICU | Meson | 1 | parity |  |
| `libva-utils` | utah `packages/libva-utils/libva-utils.spec` | not in Hummingbird | libdrm, wayland | LicenseRef-Callaway-MIT AND LicenseRef-Callaway-BSD | Meson | 0 | parity |  |
| `mesa` | utah `packages/mesa/mesa.spec` | not in Hummingbird (mesa-dri-drivers, mesa-filesystem) | libdrm, libxshmfence, lm_sensors | MIT AND BSD-3-Clause AND SGI-B-2.0 | Meson | 0 | parity | Big meson build; mesa-va-drivers merged into mesa-dri-drivers in F44, so VA/VDPAU splitting differs from older docs. Reference build for libgbm/libEGL used by niri/quickshell. |
| `osinfo-db-tools` | **new** | not in Hummingbird (osinfo-db-tools) | json-glib, libsoup3 | GPL-2.0-or-later | — | 0 | **drop** |  |
| `pangomm2.48` | **new** | not in Hummingbird (pangomm2.48) | cairomm1.16, glibmm2.68, libsigc++30 | LGPL-2.1-or-later | — | 0 | **drop** |  |
| `pulseaudio` | utah `packages/pulseaudio/pulseaudio.spec` | not in Hummingbird (pulseaudio-libs, pulseaudio-libs-glib2) | libasyncns, libsndfile | LGPL-2.1-or-later | — | 8 | parity |  |
| `pycairo` | utah `packages/pycairo/pycairo.spec` | not in Hummingbird (python3-cairo) | cairo | LGPL-2.1-only OR MPL-1.1 | — | 0 | parity |  |
| `python-aiohttp` | **new** | not in Hummingbird (python3-aiohttp) | python-aiohappyeyeballs, python-aiosignal, python-frozenlist | Apache-2.0 | — | 0 | parity |  |
| `python-aiohttp-oauthlib` | **new** | not in Hummingbird (python3-aiohttp-oauthlib) | python-aiohttp, python-oauthlib | ISC | — | 0 | parity |  |
| `python-icalendar` | **new** | not in Hummingbird (python3-icalendar) | python-dateutil, pytz | BSD-2-Clause | — | 0 | parity |  |
| `qca` | **new** | not in Hummingbird (qca-qt6, qca-qt6-ossl) | qt6-qt5compat | LGPL-2.1-only | — | 0 | parity |  |
| `qt5-qtgraphicaleffects` | **new** | not in Hummingbird (qt5-qtgraphicaleffects) | qt5-qtdeclarative | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | — | 0 | **drop** |  |
| `qt5-qtquickcontrols` | **new** | not in Hummingbird (qt5-qtquickcontrols) | qt5-qtdeclarative | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | — | 0 | **drop** |  |
| `qt5-qtquickcontrols2` | **new** | not in Hummingbird (qt5-qtquickcontrols2) | qt5-qtdeclarative, qt5-qtgraphicaleffects | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | — | 0 | **drop** |  |
| `qt5-qtwayland` | **new** | not in Hummingbird (qt5-qtwayland) | libglvnd, qt5-qtdeclarative, wayland | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | — | 0 | **drop** |  |
| `qt6-qt5compat` | **new** | not in Hummingbird (qt6-qt5compat) | qt6-qtdeclarative, qt6-qtshadertools | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | 0 | parity |  |
| `qt6-qtquick3d` | **new** | not in Hummingbird (qt6-qtquick3d) | assimp, libglvnd, openxr | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | 0 | parity |  |
| `samba` | utah `packages/samba/samba.spec` | not in Hummingbird (libldb, libsmbclient) | libtalloc, libtdb, libtevent | GPL-3.0-or-later AND LGPL-3.0-or-later / LGPL-3.0-or-later | — | 0 | parity |  |


### Wave 3 (31 recipes)

| Package | Reuse / New | Why owned | Key deps | License | Build | Patches | Scope | Hazards / notes |
|---|---|---|---|---|---|---:|---|---|
| `greetd` | **new** | not in Hummingbird (greetd, greetd-selinux) | — | GPL-3.0-only AND Apache-2.0 AND MIT AND Unlicense (mixed) | Rust (cargo) | 0 | core | Rust; ships greetd-selinux subpackage; mixed GPL/Apache/MIT/Unlicense. PAM file must stay stock (hand-written /etc/pam.d/greetd broke the bus in the archived pluto). |
| `gupnp` | utah `packages/gupnp/gupnp.spec` | not in Hummingbird (gupnp) | gssdp, libsoup3 | LGPL-2.1-or-later | Meson | 0 | core |  |
| `gupnp-igd` | utah `packages/gupnp-igd/gupnp-igd.spec` | not in Hummingbird (gupnp-igd) | gssdp, gupnp | LGPL-2.1-or-later | — | 0 | core |  |
| `kf6-kiconthemes` | **new** | not in Hummingbird (kf6-kiconthemes) | hicolor-icon-theme, kf6-breeze-icons, kf6-karchive | CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | core |  |
| `libnice` | utah `packages/libnice/libnice.spec` | not in Hummingbird (libnice) | gupnp-igd | LGPL-2.1-or-later OR MPL-1.1 | — | 1 | core |  |
| `niri` | **new** | not in Hummingbird | cairo, libdisplay-info, libinput | GPL-3.0-or-later | Rust (cargo) | 0 | core | Rust: needs %cargo_prep/%generate_buildrequires retries and vendored crates. Hard Requires xwayland-satellite. Links libgbm/libEGL from our mesa. |
| `pipewire` | utah `packages/pipewire/pipewire.spec` | not in Hummingbird (pipewire-libs) | pipewire-media-session, rtkit, wireplumber | MIT AND GPL-2.0-or-later AND BSD-2-Clause AND LGPL-2.0-or-later | Meson | 1 | core | ABI core for DMS/Quickshell; keep wireplumber in lockstep. |
| `qt6-qtdeclarative` | **new** | not in Hummingbird (qt6-qtdeclarative) | — | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | 0 | core | Qt6 ABI must match Hummingbird's qt6-qtbase exactly; build from Fedora dist-git at the same minor as the base. |
| `qt6-qtwayland` | **new** | not in Hummingbird (qt6-qtwayland) | libglvnd, qt6-qtdeclarative, wayland | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | 0 | core | Qt6 ABI coupling to qt6-qtbase + qt6-qtdeclarative. |
| `quickshell` | **new** | not in Hummingbird | libdrm, libglvnd, pipewire | LGPL-3.0-only AND GPL-3.0-only | — | 0 | core | C++/Qt6, links Qt6 PRIVATE API (qt6-qtbase-private-devel, present in Hummingbird). Must match the qt6-qtbase version Hummingbird ships AND the qt6-qtdeclarative we build. DMS 1.6.x expects 0.3.x; Fedora's 0.2.1 snapshot is likely too old. cpptrace crash handler optional. |
| `wireplumber` | utah `packages/wireplumber/wireplumber.spec` | not in Hummingbird (wireplumber, wireplumber-libs) | pipewire | MIT | Meson | 0 | core | Must match pipewire minor. |
| `xwayland-satellite` | **new** | not in Hummingbird (xwayland-satellite) | open-sans-fonts, xcb-util-cursor, xorg-x11-server-Xwayland | MPL-2.0 | Rust (cargo) | 0 | core | Rust/cargo vendoring. |
| `cava` | **new** | not in Hummingbird | iniparser, pulseaudio | MIT | — | 0 | parity |  |
| `kf5-kirigami2` | **new** | not in Hummingbird (kf5-kirigami2) | qt5-qtdeclarative, qt5-qtquickcontrols, qt5-qtquickcontrols2 | BSD-2-Clause AND BSD-3-Clause AND CC0-1.0 AND FSFAP AND GPL-2.0-or-later AND GPL-2.1-or-later AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) AND MIT AND LGPL-2.1-or-later | — | 0 | **drop** |  |
| `kf5-solid` | **new** | not in Hummingbird (kf5-solid) | kf5, libimobiledevice, libplist | BSD-3-Clause AND CC0-1.0 AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf6-frameworkintegration` | **new** | not in Hummingbird (kf6-frameworkintegration, kf6-frameworkintegration-libs) | kf6, kf6-ki18n, kf6-knewstuff | CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | parity |  |
| `kf6-kconfigwidgets` | **new** | not in Hummingbird (kf6-kconfigwidgets) | kf6, kf6-kcodecs, kf6-kcolorscheme | CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | parity |  |
| `kf6-kjobwidgets` | **new** | not in Hummingbird (kf6-kjobwidgets) | kf6, kf6-kcoreaddons, kf6-knotifications | CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later | — | 0 | parity |  |
| `kf6-knewstuff` | **new** | not in Hummingbird (kf6-knewstuff) | kf6, kf6-attica, kf6-karchive | BSD-2-Clause AND CC0-1.0 AND GPL-2.0-only AND GPL-3.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | parity |  |
| `kf6-knotifications` | **new** | not in Hummingbird (kf6-knotifications) | kf6-kconfig, libcanberra, qt6-qtdeclarative | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | parity |  |
| `kf6-kwallet` | **new** | not in Hummingbird (kf6-kwallet, kf6-kwallet-libs) | kf6, kf6-kcolorscheme, kf6-kconfig | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-or-later AND LGPL-3.0-or-later | — | 0 | parity |  |
| `kf6-kxmlgui` | **new** | not in Hummingbird (kf6-kxmlgui) | kf6, kf6-kconfig, kf6-kconfigwidgets | BSD-2-Clause AND CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | parity |  |
| `kf6-qqc2-desktop-style` | **new** | not in Hummingbird | kf6-kcolorscheme, kf6-kconfig, kf6-kiconthemes | CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND LGPL-2.0-or-later AND LGPL-2.1-or-later AND LGPL-3.0-only AND LicenseRef-KFQF-Accepted-GPL | CMake | 0 | parity | QtQuick Controls style DMS/qt6ct use; KF6 coupling. |
| `kf6-solid` | **new** | not in Hummingbird (kf6-solid) | kf6, libimobiledevice, libplist | LGPL-2.1-or-later AND LGPL-2.1-only AND CCO-1.0 AND BSD-3-Clause AND LGPL-3.0-only | — | 0 | parity |  |
| `khal` | **new** | not in Hummingbird | python-click, python-click-log, python-configobj | MIT | — | 0 | parity |  |
| `libcanberra` | utah `packages/libcanberra/libcanberra.spec` | not in Hummingbird (libcanberra, libcanberra-gtk3) | gstreamer1, libtdb, libvorbis | LGPL-2.1-or-later | — | 2 | parity |  |
| `libosinfo` | **new** | not in Hummingbird (libosinfo) | gobject-introspection, hwdata, libsoup3 | LGPL-2.1-or-later | — | 0 | **drop** |  |
| `polkit-kde` | **new** | not in Hummingbird | kf6-kcoreaddons, kf6-kcrash, kf6-kdbusaddons | GPL-2.0-or-later AND CC0-1.0 | — | 0 | parity |  |
| `qt6-qtmultimedia` | **new** | not in Hummingbird | ffmpeg, gstreamer1, gstreamer1-plugins-bad-free | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | 0 | parity | DMS Recommends; pulls gstreamer/ffmpeg closure. |
| `qt6-qtshadertools` | **new** | not in Hummingbird (qt6-qtshadertools) | spirv-tools | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | 0 | parity | Build dep of quickshell/DMS QtQuick; Qt6 ABI coupling. |
| `vdirsyncer` | **new** | not in Hummingbird (python3-vdirsyncer, vdirsyncer) | python-aiohttp-oauthlib, python-click, python-click-log | BSD-3-Clause | — | 0 | parity |  |


### Wave 4 (70 recipes)

| Package | Reuse / New | Why owned | Key deps | License | Build | Patches | Scope | Hazards / notes |
|---|---|---|---|---|---|---:|---|---|
| `cliphist` | **new** | not in Hummingbird | wl-clipboard, xdg-utils | BSD-3-Clause AND GPL-3.0-only AND MIT | Go | 0 | core |  |
| `dankcalendar-git` | **new** | absent from Fedora 44 and Hummingbird | quickshell, libsecret, qt6-qtdeclarative | MIT | Go (prebuilt) + QML | 0 | core | Optional DMS calendar. git channel carries an epoch and a rolling version; only build if wanted. |
| `danksearch` | **new** | not in Hummingbird | — | Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND MIT | Go | 0 | core | Go; prebuilt binary downloaded at build; F44 version 0.1.2 is far behind 1.6.0. |
| `dgop` | **new** | not in Hummingbird | — | Apache-2.0 AND BSD-3-Clause AND ISC AND MIT | Go | 0 | core | Epoch:1 in the COPR spec; prebuilt Go binary downloaded at build. |
| `dms` | **new** | absent from Fedora 44 and Hummingbird | quickshell, accountsservice, dms-cli, dgop | MIT | Go (prebuilt) + QML data | 0 | core | Spec downloads a prebuilt dms-cli Go binary from GitHub releases (supply chain). Source0 is a generated dms-qml.tar.gz. |
| `dms-cli` | **new** | absent from Fedora 44 and Hummingbird | glibc | MIT | Go (prebuilt) | 0 | core | Subpackage of dms; shipped as a downloaded Go binary, not compiled in the factory. |
| `dms-greeter` | **new** | absent from Fedora 44 and Hummingbird | greetd, quickshell, policycoreutils-python-utils | MIT | Go | 0 | core | Go binary (compiled; BuildRequires golang >= 1.24); Requires (quickshell-git or quickshell) + greetd; Suggests niri/hyprland/sway. |
| `gdk-pixbuf2` | utah `packages/gdk-pixbuf2/gdk-pixbuf2.spec` | not in Hummingbird (gdk-pixbuf2) | glycin, shared-mime-info | LGPL-2.1-or-later | Meson | 3 | core |  |
| `ghostty` | **new** | absent from Fedora 44 and Hummingbird | gtk4, libadwaita, gtk4-layer-shell, fontconfig, harfbuzz, oniguruma, pixman | MIT | Zig | 0 | core | ExclusiveArch x86_64/aarch64; needs zig >= %{zig_minimum_version} (F44 ships zig 0.15.2). Gtk4 stack must be built first. |
| `glycin` | utah `packages/glycin/glycin.spec` | not in Hummingbird (glycin-gtk4-libs, glycin-libs) | cairo, gtk4 | (MPL-2.0 OR LGPL-2.1-or-later) AND Apache-2.0 WITH LLVM-exception AND BSD-3-Clause AND CC0-1.0 AND GPL-3.0-or-later AND IJG AND ISC AND MIT AND Unicode-3.0 AND Unicode-DFS-2016 AND (0BSD OR MIT OR Apache-2.0) AND (Apache-2.0 OR MIT) AND (Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT) AND (BSD-2-Clause OR Apache-2.0 OR MIT) AND (BSD-3-Clause OR Apache-2.0) AND (MIT OR Apache-2.0 OR Zlib) AND (Unlicense OR MIT) | Rust (cargo) | 5 | core |  |
| `gstreamer1-plugins-bad-free` | utah `packages/gstreamer1-plugins-bad-free/gstreamer1-plugins-bad-free.spec` | not in Hummingbird (gstreamer1-plugins-bad-free-libs) | gstreamer1, gstreamer1-plugins-base, libdrm | LGPL-2.1-or-later AND LGPL-2.0-or-later AND (MIT OR LGPL-2.1-or-later) AND MPL-1.1 AND BSD-2-Clause AND BSD-3-Clause AND BSD-2-Clause-Views AND (BSD-2-Clause AND DOC) AND MIT-Festival AND (LGPL-2.0-or-later AND LicenseRef-Fedora-Public-Domain) AND (MPL-1.1 OR LGPL-2.0-or-later OR MIT) AND BSD-3-Clause WITH AdditionRef-Dart AND MIT AND GPL-2.0-only WITH Linux-syscall-note | — | 1 | core |  |
| `gtk3` | utah `packages/gtk3/gtk3.spec` | not in Hummingbird (gtk-update-icon-cache, gtk3) | adwaita-icon-theme, at-spi2-core, cairo | LGPL-2.0-or-later | Meson | 2 | core |  |
| `gtk4` | utah `packages/gtk4/gtk4.spec` | not in Hummingbird (gtk4) | adwaita-icon-theme, cairo, colord | LGPL-2.0-or-later AND LGPL-2.1-or-later AND Apache-2.0 AND CC0-1.0 AND MIT AND MIT-open-group AND HPND-sell-variant AND GPL-2.0-or-later AND GPL-3.0-or-later AND OFL-1.1 | Meson | 0 | core | Quickshell does not need GTK4; ghostty and libadwaita do. Build only if ghostty/nautilus ship. |
| `libdecor` | utah `packages/libdecor/libdecor.spec` | not in Hummingbird (libdecor) | cairo, gtk3, pango | MIT | — | 0 | core |  |
| `material-symbols-fonts` | **new** | absent from Fedora 44 and Hummingbird | fontpackages-filesystem | Apache-2.0 | data/noarch | 0 | core | Source0 is a raw GitHub URL with no sha512 in the COPR spec; pluto must pin a checksum. |
| `matugen` | **new** | not in Hummingbird (matugen) | — | GPL-2.0-only AND Apache-2.0 AND Apache-2.0 WITH LLVM-exception AND BSD-2-Clause AND BSD-3-Clause AND MIT AND MPL-2.0 AND Unicode-DFS-2016 AND Zlib AND (0BSD OR MIT OR Apache-2.0) AND (Apache-2.0 OR BSL-1.0) AND (Apache-2.0 OR MIT) AND (Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT) AND (BSD-2-Clause OR Apache-2.0 OR MIT) AND (MIT OR Apache-2.0 OR NCSA) AND (MIT OR Apache-2.0 OR Zlib) AND (Unlicense OR MIT) | Rust (cargo) | 0 | core | Rust/cargo; F44 3.1.0 vs upstream 4.2.0. |
| `qt6ct` | **new** | not in Hummingbird | kf6-kcolorscheme, kf6-kconfig, kf6-kiconthemes | BSD-2-Clause | — | 0 | core |  |
| `uupd` | **new** | absent from Fedora 44 and Hummingbird | glibc | Apache-2.0 | Go | 0 | core | Single Go binary; currently installed from ublue-os/packages COPR. Trivial second target. |
| `xorg-x11-server-Xwayland` | utah `packages/xorg-x11-server-Xwayland/xorg-x11-server-Xwayland.spec` | not in Hummingbird (xorg-x11-server-Xwayland) | libXdmcp, libXfont2, libdecor | MIT | — | 0 | core | Needed only through xwayland-satellite; meson build. |
| `chromaprint` | **new** | not in Hummingbird (libchromaprint) | ffmpeg | GPL-2.0-or-later | — | 0 | **drop** |  |
| `ffmpeg` | utah `packages/ffmpeg/ffmpeg.spec` | not in Hummingbird (libavcodec-free, libavformat-free) | aribb24, cairo, codec2 | GPL-3.0-or-later | — | 5 | parity |  |
| `flatpak` | utah `packages/flatpak/flatpak.spec` | not in Hummingbird (flatpak-selinux, flatpak-session-helper) | appstream, dconf, gdk-pixbuf2 | LGPL-2.1-or-later | — | 2 | parity | Only needed for Bluefin parity; adds a large closure (bubblewrap, xdg-dbus-proxy). |
| `gcr3` | utah `packages/gcr3/gcr3.spec` | not in Hummingbird (gcr3, gcr3-base) | cairo, gdk-pixbuf2, gtk3 | LGPL-2.1-or-later AND LicenseRef-Fedora-Public-Domain AND FSFULLRWD AND (LGPL-3.0-or-later OR CC-BY-SA-3.0) AND (MPL-1.1 OR GPL-2.0-or-later OR LGPL-2.1-or-later) AND GCR-docs | — | 0 | parity |  |
| `geoclue2` | utah `packages/geoclue2/geoclue2.spec` | not in Hummingbird (geoclue2) | ModemManager, json-glib, libnotify | GPL-2.0-or-later | — | 0 | parity |  |
| `gnome-autoar` | utah `packages/gnome-autoar/gnome-autoar.spec` | not in Hummingbird (gnome-autoar) | gtk3 | LGPL-2.1-or-later | — | 0 | parity |  |
| `gnome-desktop3` | utah `packages/gnome-desktop3/gnome-desktop3.spec` | not in Hummingbird (gnome-desktop3, gnome-desktop4) | cairo, gdk-pixbuf2, gsettings-desktop-schemas | GPL-2.0-or-later AND LGPL-2.0-or-later / GPL-2.0-or-later AND LGPL-2.0-or-later AND GFDL-1.1-or-later | — | 0 | parity |  |
| `gnome-disk-utility` | **new** | not in Hummingbird | at-spi2-core, cairo, gdk-pixbuf2 | GPL-2.0-or-later AND CC0-1.0 | — | 0 | parity |  |
| `gnome-keyring` | utah `packages/gnome-keyring/gnome-keyring.spec` | not in Hummingbird (gnome-keyring) | gcr3 | GPL-2.0-only AND GPL-2.0-or-later AND LGPL-2.1-or-later AND ((GPL-2.0-or-later OR LGPL-3.0-or-later) OR BSD-3-Clause) AND (MPL-1.1 OR GPL-2.0-or-later OR LGPL-2.1-or-later) | Meson | 2 | parity |  |
| `gtkmm4.0` | **new** | not in Hummingbird (gtkmm4.0) | cairo, cairomm1.16, gdk-pixbuf2 | LGPL-2.1-or-later | — | 0 | **drop** |  |
| `gtksourceview4` | utah `packages/gtksourceview4/gtksourceview4.spec` | not in Hummingbird (gtksourceview4) | at-spi2-core, cairo, fribidi | LicenseRef-Callaway-LGPLv2+ | — | 1 | parity |  |
| `input-remapper` | utah `packages/input-remapper/input-remapper.spec` | not in Hummingbird | gobject-introspection, gtk3, gtksourceview4 | GPL-3.0-or-later | — | 0 | parity |  |
| `kde-settings` | utah `packages/kde-settings/kde-settings.spec` | not in Hummingbird (kde-settings, qt-settings) | kde-filesystem, kf6-breeze-icons, shared-mime-info | MIT | — | 0 | parity |  |
| `kf5-frameworkintegration` | **new** | not in Hummingbird (kf5-frameworkintegration, kf5-frameworkintegration-libs) | kf5-ki18n, kf5-knewstuff, kf5-knotifications | CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kcompletion` | **new** | not in Hummingbird (kf5-kcompletion) | kf5-kconfig, kf5-kwidgetsaddons | CC0-1.0 AND LGPL-2.0-or-later AND LGPL-2.1-or-later | — | 0 | **drop** |  |
| `kf5-kconfig` | **new** | not in Hummingbird (kf5-kconfig-core, kf5-kconfig-gui) | kde-settings, qt5-qtdeclarative | BSD-2-Clause AND BSD-3-Clause AND CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) AND MIT | — | 0 | **drop** |  |
| `kf5-kconfigwidgets` | **new** | not in Hummingbird (kf5-kconfigwidgets) | kf5-kauth, kf5-kcodecs, kf5-kconfig | CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) AND MIT | — | 0 | **drop** |  |
| `kf5-kiconthemes` | **new** | not in Hummingbird (kf5-kiconthemes) | hicolor-icon-theme, kf5-karchive, kf5-kconfig | CC0-1.0 AND GPL-2.0-only AND GPL-3.0-only AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kinit` | **new** | not in Hummingbird (kf5-kinit) | kf5-kconfig, kf5-kcoreaddons, kf5-kcrash | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-only AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kio` | **new** | not in Hummingbird (kf5-kio-core, kf5-kio-core-libs) | kf5-karchive, kf5-kauth, kf5-kconfig | BSD-2-Clause AND BSD-3-Clause AND CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND (LGPL-2.1-only OR LGPL-3.0-only) AND MIT | — | 0 | **drop** |  |
| `kf5-knewstuff` | **new** | not in Hummingbird (kf5-knewstuff) | kf5-attica, kf5-karchive, kf5-kcompletion | BSD-2-Clause AND CC0-1.0 AND GPL-2.0-only AND GPL-3.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-knotifications` | **new** | not in Hummingbird (kf5-knotifications) | dbusmenu-qt, kf5-kconfig, kf5-kcoreaddons | BSD-3-Clause AND CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kservice` | **new** | not in Hummingbird (kf5-kservice) | kf5-kconfig, kf5-kcoreaddons, kf5-kdbusaddons | CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-ktextwidgets` | **new** | not in Hummingbird (kf5-ktextwidgets) | kf5-kcompletion, kf5-kconfig, kf5-kconfigwidgets | CC0-1.0 AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf5-kxmlgui` | **new** | not in Hummingbird (kf5-kxmlgui) | kf5-kconfig, kf5-kconfigwidgets, kf5-kcoreaddons | BSD-2-Clause AND CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | **drop** |  |
| `kf6-kcmutils` | **new** | not in Hummingbird (kf6-kcmutils) | kf6, kf6-kconfig, kf6-kconfigwidgets | BSD-2-Clause AND BSD-3-Clause AND CC0-1.0 AND GPL-2.0-or-later AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) | — | 0 | parity |  |
| `kf6-kio` | **new** | not in Hummingbird (kf6-kio-core, kf6-kio-core-libs) | kf6, kf6-karchive, kf6-kconfig | BSD-2-Clause AND BSD-3-Clause AND CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND LGPL-2.0-only AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND (LGPL-2.1-only OR LGPL-3.0-only) AND MIT | — | 0 | parity |  |
| `libadwaita` | utah `packages/libadwaita/libadwaita.spec` | not in Hummingbird (libadwaita) | appstream, fribidi, graphene | LGPL-2.1-or-later AND MIT | Meson | 1 | parity | Needed by ghostty and nautilus; GTK4-coupled. |
| `libappindicator` | utah `packages/libappindicator/libappindicator.spec` | not in Hummingbird | at-spi2-core, cairo, gdk-pixbuf2 | LicenseRef-Callaway-LGPLv2 AND LGPL-3.0-only | — | 0 | parity |  |
| `libayatana-appindicator` | utah `packages/libayatana-appindicator/libayatana-appindicator.spec` | not in Hummingbird (libayatana-appindicator-gtk3) | gtk3, libayatana-indicator, libdbusmenu | GPL-3.0-only AND LGPL-3.0-only AND (LGPL-3.0-only OR LGPL-2.1-only) | — | 0 | parity |  |
| `libayatana-ido` | utah `packages/libayatana-ido/libayatana-ido.spec` | not in Hummingbird (libayatana-ido-gtk3) | cairo, gdk-pixbuf2, gtk3 | LGPL-2.0-or-later AND GPL-3.0-only AND (LGPL-3.0-only OR LGPL-2.1-only) | — | 0 | parity |  |
| `libayatana-indicator` | utah `packages/libayatana-indicator/libayatana-indicator.spec` | not in Hummingbird (libayatana-indicator-gtk3) | gdk-pixbuf2, gtk3, libayatana-ido | GPL-3.0-only | — | 0 | parity |  |
| `libgsf` | **new** | not in Hummingbird (libgsf) | gdk-pixbuf2 | LGPL-2.1-only | — | 0 | parity |  |
| `libhandy` | **new** | not in Hummingbird (libhandy) | at-spi2-core, cairo, fribidi | LGPL-2.1-or-later | — | 0 | **drop** |  |
| `libnma` | utah `packages/libnma/libnma.spec` | not in Hummingbird (libnma, libnma-common) | cairo, gcr, gtk3 | GPL-2.0-or-later AND LGPL-2.1-or-later | — | 1 | parity |  |
| `libnotify` | utah `packages/libnotify/libnotify.spec` | not in Hummingbird (libnotify) | gdk-pixbuf2 | LGPL-2.1-or-later | — | 0 | parity |  |
| `librsvg2` | utah `packages/librsvg2/librsvg2.spec` | not in Hummingbird (librsvg2) | cairo, gdk-pixbuf2, pango | LGPL-2.1-or-later AND Apache-2.0 AND BSD-3-Clause AND MIT AND MPL-2.0 AND Unicode-3.0 AND Unicode-DFS-2016 AND (0BSD OR MIT OR Apache-2.0) AND (Apache-2.0 OR MIT) AND (BSD-3-Clause OR Apache-2.0) AND (MIT OR Apache-2.0 OR Zlib) AND (Unlicense OR MIT) | — | 3 | parity |  |
| `localsearch` | utah `packages/localsearch/localsearch.spec` | not in Hummingbird (localsearch) | exempi, ffmpeg, giflib | GPL-2.0-or-later AND LGPL-2.1-or-later | — | 0 | parity |  |
| `nautilus` | utah `packages/nautilus/nautilus.spec` | not in Hummingbird (nautilus-extensions) | cairo, gdk-pixbuf2, glycin | LGPL-2.1-or-later | — | 2 | parity | Pulls gvfs/glycin/localsearch/appstream; drop it to shrink the closure. |
| `network-manager-applet` | **new** | not in Hummingbird | gtk3, libnma | GPL-2.0-or-later | — | 0 | parity |  |
| `pavucontrol` | **new** | not in Hummingbird | glibmm2.68, gtk4, gtkmm4.0 | GPL-2.0-or-later | — | 0 | parity |  |
| `plasma-breeze` | **new** | not in Hummingbird (plasma-breeze-common, plasma-breeze-qt5) | kdecoration, kf6-kcmutils, kf6-kcolorscheme | BSD-3-Clause AND CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND MIT | CMake | 0 | parity | Plasma 6 KF6/KF5 mix: builds breeze-qt5 + breeze-qt6 subpackages, dragging the kf5 closure. Consider dropping if niri does not need Breeze decorations. |
| `sdl2-compat` | utah `packages/sdl2-compat/sdl2-compat.spec` | not in Hummingbird (sdl2-compat) | SDL3 | Zlib | — | 1 | parity |  |
| `SDL3` | utah `packages/SDL3/SDL3.spec` | not in Hummingbird (SDL3) | libdecor | Zlib AND MIT AND Apache-2.0 AND (Apache-2.0 OR MIT) | — | 0 | parity |  |
| `tslib` | **new** | not in Hummingbird (tslib) | sdl2-compat | LGPL-2.1-only | — | 0 | **drop** |  |
| `usbauth-notifier` | **new** | not in Hummingbird (usbauth-notifier) | libnotify, libusbauth-configparser, usbauth | GPL-2.0-only | — | 0 | **drop** |  |
| `wl-mirror` | **new** | not in Hummingbird | libdecor, libdrm, libglvnd | GPL-3.0-or-later | — | 0 | parity |  |
| `xdg-desktop-portal` | utah `packages/xdg-desktop-portal/xdg-desktop-portal.spec` | not in Hummingbird (xdg-desktop-portal) | gdk-pixbuf2, geoclue2, gstreamer1 | LGPL-2.1-or-later | Meson | 0 | parity |  |
| `xdg-desktop-portal-gnome` | utah `packages/xdg-desktop-portal-gnome/xdg-desktop-portal-gnome.spec` | not in Hummingbird | cairo, gdk-pixbuf2, gnome-desktop3 | LGPL-2.1-or-later | Meson | 0 | parity |  |
| `xdg-desktop-portal-gtk` | utah `packages/xdg-desktop-portal-gtk/xdg-desktop-portal-gtk.spec` | not in Hummingbird | gdk-pixbuf2, gsettings-desktop-schemas, gtk3 | LGPL-2.0-or-later | Meson | 0 | parity |  |
| `xdg-user-dirs` | utah `packages/xdg-user-dirs/xdg-user-dirs.spec` | not in Hummingbird (xdg-user-dirs) | usbauth-notifier | GPL-2.0-or-later AND MIT | — | 0 | parity |  |


### Recommended drops / deferrals (121 recipes)

These are real entries of the raw closure but should **not** be built for a
niri + DMS image:

- **Alternative providers** chosen only because `dnf` lists every provider:
  `moby-engine` (podman is in Hummingbird), `iwd` (use `wpa_supplicant`, utah
  recipe), `pipewire-media-session` (replaced by `wireplumber`), `libell`
  (pulled only by `iwd`), `usbauth*`.
- **GNOME/desktop-adjacent** pulled transitively and not needed by niri/DMS:
  `nautilus` pulls `gvfs`, `glycin`, `localsearch`, `gnome-autoar`,
  `gnome-desktop3`, `appstream`, `libosinfo`/`osinfo-db`, `PackageKit-Qt`.
  Keeping nautilus keeps all of them; dropping it removes ~20 recipes.
- **Qt5 / KF5**: `qt5-*` and `kf5-*` are the previous toolkit generation, pulled
  by `plasma-breeze`'s `breeze-qt5` subpackage. A Qt6-only DMS desktop does not
  need them (verify DMS's Qt5 fallbacks first).
- **Media/image transitive deps** of gstreamer/ffmpeg/nautilus: `libdvdnav`,
  `libdvdread`, `libbluray`, `libopenmpt`, `game-music-emu`, `openexr`,
  `openjph`, `LibRaw`, `assimp`, `libgexiv2`, `libhandy`, …
- **Language fonts**: Hummingbird already ships the full `google-noto-*` and
  `default-fonts` set; the extra scripts (`madan-*`, `rit-*`, `sil-padauk`,
  `vazirmatn`, `open-sans`) are optional.
- **ISO/OS plumbing**: `grub2` (utah builds it for its live ISO; a bootc image
  does not), `fedora-release`/`generic-release`/`grubby`/`sdubby`/`os-prober`
  (method artefacts of the provider expansion).


## Pluto-distinctive recipes (full detail)

The packages that decide whether the desktop works, with the detail that matters.

| Package | Wave | Reuse / New | Key deps | License | Build | Hazards / notes |
|---|---|---|---|---|---|---|
| `niri` | 3 | **new** | cairo, libdisplay-info, libinput | GPL-3.0-or-later | Rust (cargo) | Rust: needs %cargo_prep/%generate_buildrequires retries and vendored crates. Hard Requires xwayland-satellite. Links libgbm/libEGL from our mesa. |
| `xwayland-satellite` | 3 | **new** | open-sans-fonts, xcb-util-cursor, xorg-x11-server-Xwayland | MPL-2.0 | Rust (cargo) | Rust/cargo vendoring. |
| `greetd` | 3 | **new** | — | GPL-3.0-only AND Apache-2.0 AND MIT AND Unlicense (mixed) | Rust (cargo) | Rust; ships greetd-selinux subpackage; mixed GPL/Apache/MIT/Unlicense. PAM file must stay stock (hand-written /etc/pam.d/greetd broke the bus in the archived pluto). |
| `quickshell` | 3 | **new** | libdrm, libglvnd, pipewire | LGPL-3.0-only AND GPL-3.0-only | — | C++/Qt6, links Qt6 PRIVATE API (qt6-qtbase-private-devel, present in Hummingbird). Must match the qt6-qtbase version Hummingbird ships AND the qt6-qtdeclarative we build. DMS 1.6.x expects 0.3.x; Fedora's 0.2.1 snapshot is likely too old. cpptrace crash handler optional. |
| `dms` | 4 | **new** | quickshell, accountsservice, dms-cli, dgop | MIT | Go (prebuilt) + QML data | Spec downloads a prebuilt dms-cli Go binary from GitHub releases (supply chain). Source0 is a generated dms-qml.tar.gz. |
| `dms-cli` | 4 | **new** | glibc | MIT | Go (prebuilt) | Subpackage of dms; shipped as a downloaded Go binary, not compiled in the factory. |
| `dms-greeter` | 4 | **new** | greetd, quickshell, policycoreutils-python-utils | MIT | Go | Go binary (compiled; BuildRequires golang >= 1.24); Requires (quickshell-git or quickshell) + greetd; Suggests niri/hyprland/sway. |
| `dgop` | 4 | **new** | — | Apache-2.0 AND BSD-3-Clause AND ISC AND MIT | Go | Epoch:1 in the COPR spec; prebuilt Go binary downloaded at build. |
| `matugen` | 4 | **new** | — | GPL-2.0-only AND Apache-2.0 AND Apache-2.0 WITH LLVM-exception AND BSD-2-Clause AND BSD-3-Clause AND MIT AND MPL-2.0 AND Unicode-DFS-2016 AND Zlib AND (0BSD OR MIT OR Apache-2.0) AND (Apache-2.0 OR BSL-1.0) AND (Apache-2.0 OR MIT) AND (Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT) AND (BSD-2-Clause OR Apache-2.0 OR MIT) AND (MIT OR Apache-2.0 OR NCSA) AND (MIT OR Apache-2.0 OR Zlib) AND (Unlicense OR MIT) | Rust (cargo) | Rust/cargo; F44 3.1.0 vs upstream 4.2.0. |
| `danksearch` | 4 | **new** | — | Apache-2.0 AND BSD-2-Clause AND BSD-3-Clause AND MIT | Go | Go; prebuilt binary downloaded at build; F44 version 0.1.2 is far behind 1.6.0. |
| `material-symbols-fonts` | 4 | **new** | fontpackages-filesystem | Apache-2.0 | data/noarch | Source0 is a raw GitHub URL with no sha512 in the COPR spec; pluto must pin a checksum. |
| `cliphist` | 4 | **new** | wl-clipboard, xdg-utils | BSD-3-Clause AND GPL-3.0-only AND MIT | Go | not in Hummingbird |
| `qt6ct` | 4 | **new** | kf6-kcolorscheme, kf6-kconfig, kf6-kiconthemes | BSD-2-Clause | — | not in Hummingbird |
| `ghostty` | 4 | **new** | gtk4, libadwaita, gtk4-layer-shell, fontconfig, harfbuzz, oniguruma, pixman | MIT | Zig | ExclusiveArch x86_64/aarch64; needs zig >= %{zig_minimum_version} (F44 ships zig 0.15.2). Gtk4 stack must be built first. |
| `uupd` | 4 | **new** | glibc | Apache-2.0 | Go | Single Go binary; currently installed from ublue-os/packages COPR. Trivial second target. |
| `cpptrace` | 0 | **new** | libdwarf, libzstd | MIT | C++/CMake | BUILD-ONLY: quickshell's crash handler links it; absent from Fedora 44. Alternatively build quickshell with -DCRASH_HANDLER=OFF and skip cpptrace. |
| `qt6-qtdeclarative` | 3 | **new** | — | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | Qt6 ABI must match Hummingbird's qt6-qtbase exactly; build from Fedora dist-git at the same minor as the base. |
| `qt6-qtwayland` | 3 | **new** | libglvnd, qt6-qtdeclarative, wayland | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | Qt6 ABI coupling to qt6-qtbase + qt6-qtdeclarative. |
| `qt6-qtshadertools` | 3 | **new** | spirv-tools | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | Build dep of quickshell/DMS QtQuick; Qt6 ABI coupling. |
| `qt6-qtmultimedia` | 3 | **new** | ffmpeg, gstreamer1, gstreamer1-plugins-bad-free | LGPL-3.0-only OR GPL-3.0-only WITH Qt-GPL-exception-1.0 | CMake | DMS Recommends; pulls gstreamer/ffmpeg closure. |
| `kf6-kirigami` | 1 | **new** | qt6-qtdeclarative | BSD-3-Clause AND CC0-1.0 AND FSFAP AND GPL-2.0-or-later AND LGPL-2.0-or-later AND LGPL-2.1-only AND LGPL-2.1-or-later AND LGPL-3.0-only AND (LGPL-2.1-only OR LGPL-3.0-only) AND MIT | CMake | KF6 version must match the rest of the kf6 closure. |
| `kf6-qqc2-desktop-style` | 3 | **new** | kf6-kcolorscheme, kf6-kconfig, kf6-kiconthemes | CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND LGPL-2.0-or-later AND LGPL-2.1-or-later AND LGPL-3.0-only AND LicenseRef-KFQF-Accepted-GPL | CMake | QtQuick Controls style DMS/qt6ct use; KF6 coupling. |
| `kf6-kimageformats` | 2 | **new** | LibRaw, imath, jpegxl | LGPLv2+ | — | DMS 'doctor' expects it; KF6 coupling. |
| `plasma-breeze` | 4 | **new** | kdecoration, kf6-kcmutils, kf6-kcolorscheme | BSD-3-Clause AND CC0-1.0 AND GPL-2.0-only AND GPL-2.0-or-later AND GPL-3.0-only AND (GPL-2.0-only OR GPL-3.0-only) AND MIT | CMake | Plasma 6 KF6/KF5 mix: builds breeze-qt5 + breeze-qt6 subpackages, dragging the kf5 closure. Consider dropping if niri does not need Breeze decorations. |
| `mesa` | 2 | utah `packages/mesa/mesa.spec` | libdrm, libxshmfence, lm_sensors | MIT AND BSD-3-Clause AND SGI-B-2.0 | Meson | Big meson build; mesa-va-drivers merged into mesa-dri-drivers in F44, so VA/VDPAU splitting differs from older docs. Reference build for libgbm/libEGL used by niri/quickshell. |
| `libdrm` | 2 | utah `packages/libdrm/libdrm.spec` | libpciaccess | MIT | Meson | not in Hummingbird (libdrm) |
| `libglvnd` | 0 | utah `packages/libglvnd/libglvnd.spec` | — | MIT-feh AND MIT-Modern-Variant AND BSD-1-Clause AND BSD-3-Clause AND GPL-3.0-or-later WITH Autoconf-exception-macro | CMake | not in Hummingbird (libglvnd, libglvnd-egl) |
| `libepoxy` | 0 | utah `packages/libepoxy/libepoxy.spec` | — | MIT | CMake | not in Hummingbird (libepoxy) |
| `vulkan-loader` | 0 | utah `packages/vulkan-loader/vulkan-loader.spec` | — | Apache-2.0 | Meson | not in Hummingbird (vulkan-loader) |
| `wayland` | 0 | utah `packages/wayland/wayland.spec` | — | MIT | Meson | Provides libwayland-client/server/cursor/egl; everything Wayland below niri links it. |
| `libinput` | 2 | utah `packages/libinput/libinput.spec` | libevdev, libwacom, mtdev | MIT | Meson | ABI-sensitive; niri links it. Rebuild against Hummingbird's libevdev/mtdev. |
| `libdisplay-info` | 0 | utah `packages/libdisplay-info/libdisplay-info.spec` | — | MIT | Meson | not in Hummingbird (libdisplay-info) |
| `pipewire` | 3 | utah `packages/pipewire/pipewire.spec` | pipewire-media-session, rtkit, wireplumber | MIT AND GPL-2.0-or-later AND BSD-2-Clause AND LGPL-2.0-or-later | Meson | ABI core for DMS/Quickshell; keep wireplumber in lockstep. |
| `wireplumber` | 3 | utah `packages/wireplumber/wireplumber.spec` | pipewire | MIT | Meson | Must match pipewire minor. |
| `xorg-x11-server-Xwayland` | 4 | utah `packages/xorg-x11-server-Xwayland/xorg-x11-server-Xwayland.spec` | libXdmcp, libXfont2, libdecor | MIT | — | Needed only through xwayland-satellite; meson build. |
| `accountsservice` | 0 | utah `packages/accountsservice/accountsservice.spec` | — | GPL-3.0-or-later | Meson | Hard Requires of dms; GPL-3.0-or-later; absent from Hummingbird. |
| `libsecret` | 0 | utah `packages/libsecret/libsecret.spec` | — | LGPL-2.1-or-later AND Apache-2.0 AND (GPL-2.0-or-later OR TGPPL-1.0) AND LicenseRef-Fedora-Public-Domain AND GCR-docs | Meson | not in Hummingbird (libsecret) |
| `gnome-keyring` | 4 | utah `packages/gnome-keyring/gnome-keyring.spec` | gcr3 | GPL-2.0-only AND GPL-2.0-or-later AND LGPL-2.1-or-later AND ((GPL-2.0-or-later OR LGPL-3.0-or-later) OR BSD-3-Clause) AND (MPL-1.1 OR GPL-2.0-or-later OR LGPL-2.1-or-later) | Meson | not in Hummingbird (gnome-keyring) |
| `upower` | 1 | utah `packages/upower/upower.spec` | gobject-introspection | GPL-2.0-or-later | Meson | not in Hummingbird (upower-libs) |
| `udisks2` | 1 | utah `packages/udisks2/udisks2.spec` | libblockdev, libgudev | GPL-2.0-or-later / LGPL-2.0-or-later | Meson | not in Hummingbird (libudisks2, udisks2) |
| `libblockdev` | 0 | utah `packages/libblockdev/libblockdev.spec` | — | LGPL-2.1-or-later | — | not in Hummingbird (libblockdev, libblockdev-crypto) |
| `bluez` | 0 | utah `packages/bluez/bluez.spec` | — | GPL-2.0-or-later | Meson | not in Hummingbird (bluez-libs) |
| `colord` | 2 | utah `packages/colord/colord.spec` | lcms2, libgusb | GPL-2.0-or-later AND LGPL-2.1-or-later | Meson | not in Hummingbird (colord-libs) |
| `rtkit` | 0 | utah `packages/rtkit/rtkit.spec` | — | GPL-3.0-or-later AND MIT | Meson | not in Hummingbird (rtkit) |
| `xdg-desktop-portal` | 4 | utah `packages/xdg-desktop-portal/xdg-desktop-portal.spec` | gdk-pixbuf2, geoclue2, gstreamer1 | LGPL-2.1-or-later | Meson | not in Hummingbird (xdg-desktop-portal) |
| `xdg-desktop-portal-gnome` | 4 | utah `packages/xdg-desktop-portal-gnome/xdg-desktop-portal-gnome.spec` | cairo, gdk-pixbuf2, gnome-desktop3 | LGPL-2.1-or-later | Meson | not in Hummingbird |
| `xdg-desktop-portal-gtk` | 4 | utah `packages/xdg-desktop-portal-gtk/xdg-desktop-portal-gtk.spec` | gdk-pixbuf2, gsettings-desktop-schemas, gtk3 | LGPL-2.0-or-later | Meson | not in Hummingbird |
| `gtk4` | 4 | utah `packages/gtk4/gtk4.spec` | adwaita-icon-theme, cairo, colord | LGPL-2.0-or-later AND LGPL-2.1-or-later AND Apache-2.0 AND CC0-1.0 AND MIT AND MIT-open-group AND HPND-sell-variant AND GPL-2.0-or-later AND GPL-3.0-or-later AND OFL-1.1 | Meson | Quickshell does not need GTK4; ghostty and libadwaita do. Build only if ghostty/nautilus ship. |
| `libadwaita` | 4 | utah `packages/libadwaita/libadwaita.spec` | appstream, fribidi, graphene | LGPL-2.1-or-later AND MIT | Meson | Needed by ghostty and nautilus; GTK4-coupled. |

---

## First wave recommendation

Do **not** start with DMS. Start with the packages that have nothing to depend on
in-closure and that the rest of the stack links against. Concretely, the first
pull request should build a small, high-leverage slice of **Wave 0** and prove the
factory end to end (`spec → rpmbuild in quay.io/fedora/fedora:44 + Hummingbird
overlay → createrepo_c → ghcr.io/siddhj2206/pluto-packages@digest → bind-mount`):

1. **`wayland`** and **`wayland-protocols`** — every compositor/portal/toolkit
   links them; both are utah recipes already seeded from Fedora rawhide.
2. **`libdrm`**, **`libdisplay-info`**, **`libxcvt`**, **`pixman`** — the
   graphics base, no in-closure deps.
3. **`libevdev`** + **`mtdev`** + **`libwacom`**, then **`libinput`** — niri's
   input path (libinput depends on the other three).
4. **`mesa`** — the single highest-leverage graphics build; provides
   `libgbm`/`libEGL`/`libGL`. It is wave-0 by the graph because its own deps
   (`libdrm`, `libxshmfence`, `spirv-tools`) are the preceding items. Build it
   early because niri and quickshell both hard-require it.
5. **`libglvnd`**, **`libepoxy`**, **`vulkan-loader`** — close out the GL stack.

Only after that slice installs cleanly on the Hummingbird base (and
`bootc container lint` stays green) should you add **wave 3** (`qt6-qtdeclarative`,
`qt6-qtwayland`, `qt6-qtshadertools`, `kf6-*`, `pipewire`, `wireplumber`,
`niri`, `quickshell`) and then the **wave 4 DMS stack**. The first *DMS* recipe to
land should be `dms`+`dms-cli` (the package-inventory report's slice 1), but that
is a *proof of the factory*, not the start of the closure — without wayland, mesa
and Qt6 declarative there is nothing for DMS to run on. Build the base first; DMS
is the finish line.

**Minimum viable order to a booting niri session** (ignoring parity):

```
wayland, wayland-protocols, libdrm, libdisplay-info, pixman, libxshmfence, libxcvt
  → libinput, libevdev, mtdev, libei, libseat(seatd), libX11/libxcb/X utils
  → mesa, libglvnd, libepoxy, vulkan-loader, spirv-tools
  → niri, xwayland-satellite, xorg-x11-server-Xwayland
  → pipewire, wireplumber, rtkit, alsa UCM
  → fonts/adwaita icon theme, xdg-desktop-portal(+gtk/gnome), xdg-dbus-proxy
  → qt6-qtdeclarative, qt6-qtshadertools, qt6-qtwayland, kf6 core
  → quickshell → dms + dms-cli → dms-greeter + greetd → accountsservice, dgop
```

---

## Hazards

1. **Qt6 private API / version coupling.** `quickshell` `BuildRequires:
   qt6-qtbase-private-devel` and links Qt6 private headers
   (`specs/quickshell.spec:52`). Hummingbird ships `qt6-qtbase` and its
   `-private-devel`, so the private symbols exist — but **the `qt6-qtdeclarative`
   / `qt6-qtwayland` / `qt6-qtshadertools` that pluto builds must be the exact
   same Qt minor as Hummingbird's `qt6-qtbase`**, and DMS's QML must match
   quickshell's QML API. Fedora 44's `quickshell` is `0.2.1^git20260209`, while
   the DMS/danklinux line is `0.3.1`–`0.3.2`; **assume Fedora's snapshot will not
   satisfy DMS** (`docs/research/package-inventory.md` B.1.2). Pin the Qt minor in
   the factory's allow-list and rebuild the Qt6 set whenever Hummingbird moves.
2. **Prebuilt-binary specs (supply chain).** The stable danklinux specs do not
   compile their Go programs: `dms` downloads
   `releases/latest/download/dms-distropkg-${{ARCH_SUFFIX}}.gz`
   (`specs/dms.spec:69`), `dgop` downloads `dgop-linux-amd64.gz`
   (`specs/dgop.spec:13`), and `material-symbols-fonts` downloads a raw font URL
   with no sha512 (`specs/material-symbols-fonts.spec:8`). For a "build the closure
   ourselves" project this is a contradiction: either compile from the Go source
   (preferred; upstream repos are MIT) or pin the download with a SHA-512 in
   `packages/config/upstream-sources.json` and treat it as a repackaged binary,
   documented as such.
3. **Rust `%generate_buildrequires`/cargo vendoring.** `niri`,
   `xwayland-satellite`, `greetd`, `matugen` are Rust. Expect
   `%cargo_prep` + bounded `rpmbuild -br` retries exactly as Kestrel's
   `build-stage.yml` does. Vendored crates must be staged and checksummed.
4. **`cpptrace` is not in Fedora 44.** quickshell's Fedora branch enables the
   crash handler and requires `cpptrace-devel` + `libdwarf-devel`
   (`specs/quickshell.spec:15-22`). `libdwarf` is in F44; `cpptrace` is not, so
   either add `cpptrace` as a Wave-0 recipe or build quickshell with
   `-DCRASH_HANDLER=OFF`.
5. **Epoch hazards from the COPRs.** `dgop` carries `Epoch: 1`
   (`specs/dgop.spec:5`), `dms`/`dms-greeter` git channels carry epoch 2. If any
   user previously installed the `avengemedia` packages, a pluto build without a
   matching epoch will not upgrade over them. Under Model 3 the COPRs are not
   enabled, so this only matters for migration; still, give the factory a
   `precedence` gate (utah/Kestrel pattern) and record the epoch.
6. **`mesa-va-drivers` is merged into `mesa-dri-drivers` in F44.** Old
   manifests that name `mesa-va-drivers` separately are stale; VA-API/VDPAU now
   come from the merged `mesa-dri-drivers` (`pluto-archive:build/packages/base.toml:26`).
   Do not split them.
7. **KF6 closure.** niri does not need KF6, but `dms` Recommends/Suggests and
   `qt6ct-kde`/`plasma-breeze` BuildRequire it (`specs/qt6ct-kde.spec:24-27`).
   `kf6-*` is ~30 recipes (a large, coupled wave-3 block) and `plasma-breeze`
   additionally drags `kf5-*` + `qt5-*`. Decide whether Breeze decorations are
   actually needed before committing to that closure.
8. **Buildroot Fedora repos.** The factory builds in `fedora:44` + the
   Hummingbird Pulp overlay, so **build** deps (`cmake`, `meson`, `zig`, `go`,
   `gcc`, `fontpackages-devel`, `libdwarf-devel`, `cli11-devel`) come from Fedora
   at build time; they are not shipped. Do not add build-only tools to the image.
9. **`greetd-selinux` and PAM.** `greetd` ships a `greetd-selinux` subpackage
   (utah does not carry either; both are Fedora packages to seed). Keep
   `/etc/pam.d/greetd` stock — the archived pluto's hand-written copy broke the
   session bus (`utah-hummingbird-niri-dms.md` §6.2).

---

## Uncertainties / human decisions

1. **Does Fedora 44's `quickshell` 0.2.1 satisfy DMS 1.6.x?** Almost certainly
   not, which forces `quickshell` into the owned set from day one. **UNVERIFIED**
   without a live build.
2. **Which DMS channel:** stable `1.6.2` vs `dms-git` (epoch 2). Stable is the
   sane default; the package-inventory report chose stable.
3. **Redistribution of the `avengemedia`/`danklinux` specs.** Repos declare MIT,
   but there is no separate redistribution grant. Importing spec text under MIT
   with attribution is the defensible reading; **a human should confirm before
   publishing rebuilt RPMs**.
4. **Scope of parity.** nautilus/gvfs/glycin/localsearch/flatpak/distrobox are in
   the archived pluto but not required for DMS. Dropping nautilus removes ~20
   recipes. **Decide what "only niri and DMS" means.**
5. **Firmware and multimedia are deliberately absent from the 479.** The archived
   pluto shipped `linux-firmware` + vendor splits (~245 MB) and a negativo17
   multimedia layer; neither is needed to *boot* niri/DMS and neither is in the
   seed set. If pluto wants Bluefin parity, add them as a separate manifest and
   budget the extra recipes.
6. **`plasma-breeze` Qt5/KF5 tail.** Confirm whether DMS needs Breeze window
   decorations on niri (which has no server-side decorations) before taking the
   kf5/qt5 closure.
7. **Over-approximation.** The raw closure is a `dnf` provider expansion; the
   core/parity/drop split is judgement. The exact recipe count that ships will be
   smaller once nautilus/flatpak/Qt5 are dropped.

---

## Sources

### Measured inputs (this session, 2026-09-23, host Fedora 44)

```bash
# Hummingbird index: 3,514 binary names (prior report rounds to 3,515)
wc -l /tmp/opencode/pkg-inventory/hummingbird-names.txt          # 3514

# transitive runtime closure of the seed set, mapped to SRPMs
dnf5 repoquery --providers-of=requires --recursive --qf '%{name}\n' <seeds>
dnf5 repoquery --qf '%{name}|%{sourcerpm}|%{license}\n' <seeds>

# per-package direct requires (dependency graph for wave assignment)
dnf5 repoquery --providers-of=requires --qf '%{name}\n' <pkg>

# Fedora 44 availability
dnf5 repoquery --available --qf '%{name} %{evr}\n' niri xwayland-satellite greetd \
  quickshell matugen danksearch dgop cliphist qt6ct
```

- `projectbluefin/utah-packages` — `packages/*/` (352 recipe dirs),
  `config/upstream-sources.json` (stage map, 352 entries), patch inventory (154
  recipes carry 821 patches). Clone at `/tmp/opencode/pkg-inventory/utah-packages`.
- Archived pluto manifests: `pluto-archive:build/packages/{{base,niri}}.toml`.
- Upstream specs fetched to `/tmp/opencode/specs/`: `dms.spec`, `dms-greeter.spec`,
  `quickshell.spec`, `quickshell-git.spec`, `dgop.spec`, `matugen.spec`,
  `danksearch.spec`, `material-symbols-fonts.spec`, `cliphist.spec`,
  `ghostty.spec`, `qt6ct-kde.spec`, `dankcalendar-git.spec`.

### Upstream / URLs

- Hummingbird base: https://packages.redhat.com · `quay.io/hummingbird-community/bootc-os`
- utah-packages: https://github.com/projectbluefin/utah-packages
- utah: https://github.com/projectbluefin/utah
- DMS: https://github.com/AvengeMedia/DankMaterialShell
- danklinux specs: https://github.com/AvengeMedia/danklinux
- dank-greeter: https://github.com/AvengeMedia/dank-greeter
- quickshell: https://github.com/quickshell-mirror/quickshell
- niri: https://github.com/niri-wm/niri
- xwayland-satellite: https://github.com/Supreeeme/xwayland-satellite
- greetd: https://sr.ht/~kennylevinsen/greetd/
- ghostty: https://github.com/ghostty-org/ghostty
- uupd: https://github.com/ublue-os/uupd
- COPRs (reference only; Model 3 does not consume them):
  `https://copr.fedorainfracloud.org/coprs/avengemedia/dms`,
  `.../avengemedia/danklinux`, `.../scottames/ghostty`, `.../ublue-os/packages`

### Repo files cited

- `docs/research/utah-hummingbird-niri-dms.md`, `docs/research/package-inventory.md`,
  `docs/research/kestrel-monorepo.md`, `docs/research/packit-monorepo-packages.md`
- `packages/README.md`, `packages/config/factory-contract.json` (scaffold this
  manifest feeds)
