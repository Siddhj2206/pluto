#!/usr/bin/bash

set -euo pipefail

###############################################################################
# Base Packages — WM-AGNOSTIC desktop foundation: everything a desktop needs
# that Hummingbird base lacks (fonts, graphics, audio, portals, flatpak).
# Manifest of record: build/packages/base.toml (assert-gated — the build
# FAILS on missing names). Compositor bits live in 40-niri.sh, so a
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

# flatpak-preinstall.service (common overlay) needs a defined remote.
flatpak remote-add --if-not-exists --system flathub https://flathub.org/repo/flathub.flatpakrepo

echo "::endgroup::"

echo "::group:: Enable Display Manager"

# greetd is wm-agnostic; the dms-greeter session wiring is wm-specific.
systemctl enable greetd.service

echo "::endgroup::"

echo "::group:: Enable ublue Setup Framework"

# No presets for these (bluefin enables them per-build) — enabled here so
# the hooks run for every user on login, rebasers included.
systemctl enable ublue-system-setup.service
systemctl --global enable ublue-user-setup.service

echo "::endgroup::"

echo "::group:: ZRAM + Power"

# Compressed swap — zram0 sized min(ram, 8192).
# System location so it survives /etc reset; users can override in /etc.
cat >/usr/lib/systemd/zram-generator.conf <<'EOF'
[zram0]
zram-size = min(ram, 8192)
EOF

# Laptop power profiles — the desktop-standard daemon (tuned is
# server-oriented and not part of either the base or Workstation).
systemctl enable power-profiles-daemon

# LVFS firmware metadata refresh (fwupd.service itself is D-Bus activated).
# Explicit enable: the server base has no desktop preset enabling this.
systemctl enable fwupd-refresh.timer

echo "::endgroup::"

echo "Base layer complete!"
