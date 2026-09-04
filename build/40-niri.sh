#!/usr/bin/bash

set -euo pipefail

###############################################################################
# Niri Layer — WM-SPECIFIC (compositor + DMS + greeter)
###############################################################################
# Installs the compositor stack from packages/niri.toml and wires the dynamic
# parts of the wm config. All STATIC system files (greetd config, PAM,
# systemd units/presets/wants, theme gschema) live as real files in
# custom/files/ (rsynced to / by 10-build.sh) — NOT as heredocs here.
#
# The wm-agnostic layers (10/20/25) never need to change for a compositor
# swap: replacing niri with hyprland = new packages/niri.toml + a
# 40-hyprland.sh written from this file's skeleton + files in custom/files.
###############################################################################

# Source helper functions
# shellcheck source=/dev/null
source /ctx/build/scripts/package-lib.sh

PKGS_TOML=/ctx/build/packages/niri.toml

echo "::group:: Install Niri Stack Packages"

install_fedora_section "${PKGS_TOML}" "niri fedora packages"
install_copr_sections "${PKGS_TOML}"

echo "::endgroup::"

echo "::group:: Greeter Wiring"

# greetd is enabled in 20-base.sh (wm-agnostic).
# The greeter session config (/etc/greetd/config.toml + baseline
# /etc/greetd/niri/config.kdl) is shipped as files via custom/files/ —
# dms-greeter install/enable are DISABLED by policy on ostree/bootc systems,
# so the config is baked rather than run. The greeter user is recreated each
# boot from the RPM's sysusers/tmpfiles — nothing to do here.
systemctl set-default graphical.target

echo "::endgroup::"

echo "::group:: Enable First-Boot Units"

# flatpak theming overrides + masks (unit file shipped via custom/files/):
# runs the classic `flatpak override --filesystem=xdg-data/themes` and
# `flatpak mask org.gtk.Gtk3theme.adw-gtk3{-dark}` on first boot
# (/var/lib/flatpak is ephemeral, so build-time runs would be lost).
systemctl enable flatpak-theming.service

echo "::endgroup::"

echo "::group:: DMS Autostart"

# custom/files/ ships both the niri.service.wants/dms.service symlink and the
# 90-pluto-dms.preset (single autostart path — NEVER also add
# spawn-at-startup "dms" "run" to the niri config: double start).
# Runs HERE (not in 10-build.sh) on purpose: 10-build's --global preset-all
# runs before the DMS/niri user units exist in the image (they land via the
# COPR installs above), so a preset-all with no unit files would enable
# nothing for them.
systemctl --global preset-all 2>/dev/null || true

echo "::endgroup::"

echo "::group:: Compile Theme Schemas"

# zz0-pluto-theme.gschema.override ships via custom/files/; compile it in so
# gsettings defaults (prefer-dark, adw-gtk3, Adwaita icons) apply. DMS/matugen
# takes over color-scheme theming at runtime.
glib-compile-schemas /usr/share/glib-2.0/schemas

echo "::endgroup::"

echo "Niri layer complete!"
