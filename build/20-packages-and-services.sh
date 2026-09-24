#!/usr/bin/env bash

set -euo pipefail

###############################################################################
# Default packages and services
###############################################################################
# This phase owns RPM installation. The package set is declared in
# build/packages/image.toml — readable and reviewable — and consumed here via
# build/scripts/read-packages + build/scripts/package-lib.sh.
#
# Two section kinds:
#   [hummingbird]              packages from the base image's own repository
#   ["pluto-packages:<role>"]  packages from pluto's factory OCI repo, installed
#                              from the bind-mounted image at /var/pluto-packages
#
# Packages are installed here, never in 10-overlay.sh, so a filesystem-overlay
# change cannot invalidate this layer.
###############################################################################

# shellcheck source=/dev/null
source /ctx/build/local-packages-helpers.sh
# shellcheck source=/dev/null
source /ctx/build/scripts/package-lib.sh

MANIFEST=/ctx/build/packages/image.toml

# Enable nullglob for all glob operations to prevent failures on empty matches
shopt -s nullglob

echo "::group:: Install Default Packages"

install_hummingbird_section "${MANIFEST}" "Base packages"

mapfile -t sections < <("${READ_PKGS}" "${MANIFEST}" --sections "pluto-packages:")
for section in "${sections[@]}"; do
	install_pluto_packages_section "${MANIFEST}" "${section}" "pluto ${section#pluto-packages:}"
done

echo "::endgroup::"

echo "::group:: Enable update services"

# Enable explicitly rather than relying on the shipped preset, matching how
# 10-overlay.sh enables the Brew and Flatpak units.
systemctl enable uupd.timer
systemctl enable uupd-resume.timer

echo "::endgroup::"

# Restore default glob behavior
shopt -u nullglob
