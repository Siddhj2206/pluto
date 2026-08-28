#!/usr/bin/bash

set -euo pipefail

###############################################################################
# Base Packages — WM-AGNOSTIC desktop foundation
###############################################################################
# Installs everything a desktop needs that the Hummingbird bootc-os base does
# NOT ship (no fonts, no graphics, no audio, no portals, no flatpak, ...).
#
# Manifest of record:     /ctx/build/packages/base.toml   (bluefin-style TOML)
#   [fedora]              -> install_fedora_section (one transaction + assert)
#   ["copr:<owner>/<project>"] -> install_copr_sections (COPRs disabled by
#                             clean-stage.sh in the final image — rule 3)
#
# Every listed package is verified present after install; the build FAILS
# with the missing names otherwise (assert gate in package-lib.sh — do not
# remove).
#
# Keep this layer wm-agnostic: compositor-specific packages and config
# (niri, DMS, greeter) live in 40-niri.sh with packages/niri.toml, so a
# hyprland swap never touches this file.
###############################################################################

# Source helper functions
# shellcheck source=/dev/null
source /ctx/build/scripts/package-lib.sh

PKGS_TOML=/ctx/build/packages/base.toml

echo "::group:: Install Base Packages"

install_fedora_section "${PKGS_TOML}" "base packages"

echo "::endgroup::"

echo "::group:: Install COPR Packages"

# ghostty from the scottames/ghostty COPR (base.toml ["copr:..."] section).
install_copr_sections "${PKGS_TOML}"

echo "::endgroup::"

echo "::group:: Configure Flathub Remote"

# flatpak-preinstall.service (shipped via the common overlay in 10-build.sh)
# needs a remote defined; the config lands in /etc/flatpak/remotes.d and
# persists in the image.
flatpak remote-add --if-not-exists --system flathub https://flathub.org/repo/flathub.flatpakrepo

echo "::endgroup::"

echo "::group:: Enable Display Manager"

# greetd is wm-agnostic — any compositor needs a login manager.
# The greeter/session wiring (dms-greeter) is wm-specific: see 40-niri.sh.
systemctl enable greetd.service

echo "::endgroup::"

echo "::group:: ZRAM + Power"

# Compressed swap — Zirconium/tunaOS pattern: zram0 sized min(ram, 8192).
# System location so it survives /etc reset; users can override in /etc.
cat >/usr/lib/systemd/zram-generator.conf <<'EOF'
[zram0]
zram-size = min(ram, 8192)
EOF

# Laptop power profiles — the desktop-standard daemon (tuned is
# server-oriented and not part of either the base or Workstation).
systemctl enable power-profiles-daemon

echo "::endgroup::"

echo "Base layer complete!"
