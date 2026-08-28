#!/usr/bin/bash

set -euo pipefail

###############################################################################
# Base Packages — WM-AGNOSTIC desktop foundation
###############################################################################
# Installs everything a desktop needs that the Hummingbird bootc-os base does
# NOT ship (no fonts, no graphics, no audio, no portals, no flatpak, ...).
#
# Manifest of record:     /ctx/build/packages/base.toml   (bluefin-style TOML)
#   [fedora]              -> one dnf5 install transaction
#   [excluded]            -> removed post-install
#
# Every listed package is verified present after install; the build FAILS
# with the missing names otherwise (assert gate — do not remove).
#
# Keep this layer wm-agnostic: compositor-specific packages and config
# (niri, DMS, greeter) live in 40-niri.sh with packages/niri.toml, so a
# hyprland swap never touches this file.
###############################################################################

# Source helper functions
# shellcheck source=/dev/null
source /ctx/build/copr-helpers.sh

READ_PKGS=/ctx/build/scripts/read-packages
PKGS_TOML=/ctx/build/packages/base.toml

echo "::group:: Install Base Packages"

readarray -t BASE_PACKAGES < <("${READ_PKGS}" "${PKGS_TOML}" fedora)
dnf5 install -y "${BASE_PACKAGES[@]}"

echo "::endgroup::"

echo "::group:: Remove Excluded Packages"

if readarray -t EXCLUDED_PACKAGES < <("${READ_PKGS}" "${PKGS_TOML}" excluded 2>/dev/null); then
    for pkg in "${EXCLUDED_PACKAGES[@]:-}"; do
        rpm -q "${pkg}" >/dev/null 2>&1 && dnf5 remove -y "${pkg}"
    done
fi

echo "::endgroup::"

echo "::group:: Configure Flathub Remote"

# flatpak-preinstall.service (shipped via the common overlay in 10-build.sh)
# needs a remote defined; the config lands in /etc/flatpak/remotes.d and
# persists in the image.
flatpak remote-add --if-not-exists --system flathub https://flathub.org/repo/flathub.flatpakrepo

echo "::endgroup::"

echo "::group:: Verify Package Install"

# Assert gate: every manifest package must be installed, or the build fails
# with the list of missing names. Catches typos and repo drifts immediately.
MISSING=()
for pkg in "${BASE_PACKAGES[@]}"; do
    rpm -q "${pkg}" >/dev/null 2>&1 || MISSING+=("${pkg}")
done
if [[ ${#MISSING[@]} -gt 0 ]]; then
    echo "ERROR: the following packages failed to install: ${MISSING[*]}" >&2
    exit 1
fi
echo "All ${#BASE_PACKAGES[@]} base packages present."

echo "::endgroup::"

echo "::group:: Enable Display Manager"

# greetd is wm-agnostic — any compositor needs a login manager.
# The greeter/session wiring (dms-greeter) is wm-specific: see 40-niri.sh.
systemctl enable greetd.service

echo "::endgroup::"

echo "::group:: Install COPR Packages"

# COPR-sourced packages live in ["copr:<owner>/<project>"] sections of the
# manifest. Each COPR is enabled (and deliberately KEPT enabled so its
# packages receive runtime updates — no install-time enable/disable dance)
# and installed in one transaction per COPR.
MISSING_COPR=()
while IFS= read -r copr_section; do
    copr_id="${copr_section#copr:}"
    readarray -t COPR_PACKAGES < <("${READ_PKGS}" "${PKGS_TOML}" "${copr_section}")
    echo "Installing ${COPR_PACKAGES[*]} from COPR ${copr_id}"
    dnf5 -y copr enable "${copr_id}"
    dnf5 -y install "${COPR_PACKAGES[@]}"
    for pkg in "${COPR_PACKAGES[@]}"; do
        rpm -q "${pkg}" >/dev/null 2>&1 || MISSING_COPR+=("${pkg}")
    done
done < <("${READ_PKGS}" "${PKGS_TOML}" --sections copr:)
if [[ ${#MISSING_COPR[@]} -gt 0 ]]; then
    echo "ERROR: COPR packages failed to install: ${MISSING_COPR[*]}" >&2
    exit 1
fi

# Default-terminal wiring (keybind, xdg-terminal-exec) is wm-specific
# and happens in 40-niri.sh.

echo "::endgroup::"

echo "::group:: ZRAM + Power"

# Compressed swap — Zirconium/tunaOS pattern: zram0 sized min(ram, 8192).
# System location so it survives /etc reset; users can override in /etc.
cat > /usr/lib/systemd/zram-generator.conf <<'EOF'
[zram0]
zram-size = min(ram, 8192)
EOF

# Laptop power profiles — the desktop-standard daemon (tuned is
# server-oriented and not part of either the base or Workstation).
systemctl enable power-profiles-daemon

echo "::endgroup::"

echo "Base layer complete!"