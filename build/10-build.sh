#!/usr/bin/bash

set -euo pipefail

###############################################################################
# Overlay & Wiring Layer
###############################################################################
# Pulls the OCI context overlays into the image root and copies pluto's own
# custom tree. All layers here are wm-agnostic.
#
# Overlays:
#   /ctx/oci/brew/         -> ublue-os/brew: homebrew tarball + setup/update
#                             services + profile.d (full overlay rsync)
#   /ctx/oci/common/shared/ -> projectbluefin/common shared layer: ujust and
#                             ublue binaries, flatpak-preinstall.service,
#                             brew-preinstall (user unit+preset), uupd timers,
#                             polkit ChairLift/bootc actions, udev rules,
#                             OEM hooks, profile.d, skel fragment.
#                             (bluefin/ layer is NOT rsynced — GNOME-specific —
#                             except 00-entry.just, which ujust requires.)
#
# Consumers that make the custom/ tree functional (previously dead weight):
#   custom/flatpaks/*.preinstall   -> flatpak-preinstall.service (system, enabled below)
#   custom/brew/*.Brewfile         -> brew-preinstall.service (user unit,
#                                     enabled via 01-brew-preinstall.preset +
#                                     graphical-session.target; verify in boot test)
###############################################################################

# Source helper functions
# shellcheck source=/dev/null
source /ctx/build/copr-helpers.sh

# Enable nullglob for all glob operations to prevent failures on empty matches
shopt -s nullglob

echo "::group:: Overlay Brew Integration Files"

# Brew integration files from @ublue-os/brew OCI (tarball, systemd services, shell integration)
rsync -rvKl /ctx/oci/brew/ /

echo "::endgroup::"

echo "::group:: Overlay Common Shared Files"

# Shared layer of @projectbluefin/common — designed for "any downstream fork".
# -l keeps symlinks if upstream adds any.
rsync -rvKl /ctx/oci/common/shared/ /

# ujust's hardcoded entry point lives in the bluefin/GNOME layer — without it,
# `ujust` breaks. Copy it selectively; nothing else from that layer is wanted.
install -Dm0644 \
    /ctx/oci/common/bluefin/usr/share/ublue-os/just/00-entry.just \
    /usr/share/ublue-os/just/00-entry.just

# Apply the presets the overlay ships (flatpak-appstream-refresh, uupd,
# rechunker-group-fix, brew-preinstall) — safe to run without systemd running.
systemctl preset-all >/dev/null 2>&1 || true
systemctl --global preset-all >/dev/null 2>&1 || true

# flatpak-preinstall.service has NO preset — enable it explicitly. It consumes
# /usr/share/flatpak/preinstall.d/ (populated below from custom/flatpaks) on
# first boot after network is up.
systemctl enable flatpak-preinstall.service

echo "::endgroup::"

echo "::group:: Copy Custom Files"

# Copy Brewfiles to standard location (consumed by brew-preinstall on first login)
mkdir -p /usr/share/ublue-os/homebrew/
cp /ctx/custom/brew/*.Brewfile /usr/share/ublue-os/homebrew/

# Consolidate Just Files (ours sit next to the ujust 00-entry.just)
mkdir -p /usr/share/ublue-os/just/
find /ctx/custom/ujust -iname '*.just' -exec printf "\n\n" \; -exec cat {} \; >>/usr/share/ublue-os/just/60-custom.just

# Copy Flatpak preinstall files (consumed by flatpak-preinstall.service on first boot)
mkdir -p /usr/share/flatpak/preinstall.d/
cp /ctx/custom/flatpaks/*.preinstall /usr/share/flatpak/preinstall.d/

echo "::endgroup::"

echo "::group:: Copy System Files"

# System-level files: custom/files/ is the image ROOT (rsynced to /) —
# greetd config, PAM, systemd units/presets/wants, gsettings overrides.
# -l: preserve symlinks (e.g. niri.service.wants/dms.service).
rsync -rvKl /ctx/custom/files/ /

echo "::endgroup::"

echo "::group:: Copy User Config Defaults"

# User-level config defaults: custom/config/ is the root of /etc/skel, so a
# newly created user starts with ~/.config/{niri,DankMaterialShell,...}.
# Existing users are never overwritten. Runs LAST so it wins over the skel
# fragment shipped by the common overlay (e.g. ghostty).
# -l: preserve symlinks if added.
mkdir -p /etc/skel
rsync -rvKl /ctx/custom/config/ /etc/skel/

echo "::endgroup::"

echo "::group:: Install Packages"

# Package installation lives in the manifests of record:
#   wm-agnostic foundation -> build/20-base.sh  + build/packages/base.toml
#   multimedia layer       -> build/25-multimedia.sh + build/packages/multimedia.toml
#   compositor layer       -> build/40-niri.sh  + build/packages/niri.toml

echo "::endgroup::"

# Restore default glob behavior
shopt -u nullglob

echo "Overlay layer complete!"