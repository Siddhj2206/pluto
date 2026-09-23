#!/usr/bin/env bash

set -euo pipefail

###############################################################################
# Default packages and services
###############################################################################
# This phase owns RPM and COPR installation. Packages are installed here, never
# in 10-overlay.sh, so a filesystem-overlay change cannot invalidate the
# expensive package layer above it.
#
# The default set keeps the reference image functional on first boot:
#   just  - the ujust entry point; base Fedora ships no just binary
#   gum   - interactive prompts used by the shared and custom ujust recipes
#   fzf   - ujust --choose, without a first-use Homebrew download
#   jq    - the ublue setup hooks and several recipes
#   uupd  - background update policy, from the ublue-os/packages COPR
###############################################################################

# Source helper functions
# shellcheck source=/dev/null
source /ctx/build/local-packages-helpers.sh

# Enable nullglob for all glob operations to prevent failures on empty matches
shopt -s nullglob

echo "::group:: Install Default Packages"

# jq ships in Hummingbird. just, gum and fzf are built by pluto's factory
# (packages/packages/base/) and installed from the bind-mounted repository image.
dnf5 install -y jq
local_packages_install /var/pluto-packages just gum fzf

# uupd is deferred: it hard-Requires libnotify, which needs gdk-pixbuf2 ->
# glycin-libs + shared-mime-info, and glycin's loaders pull lcms2, libexif,
# libjxl and librsvg2. Build that closure in the factory before enabling uupd.
# local_packages_install /var/pluto-packages uupd

echo "::endgroup::"

echo "::group:: Enable update services"

# Enable explicitly rather than relying on the shipped preset, matching how
# 10-overlay.sh enables the Brew and Flatpak units.
systemctl enable uupd.timer
systemctl enable uupd-resume.timer

echo "::endgroup::"

# Restore default glob behavior
shopt -u nullglob
