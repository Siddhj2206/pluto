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

# jq ships in Hummingbird. just, gum, fzf and uupd are built by pluto's factory
# (packages/packages/base/) and installed from the bind-mounted repository image.
# uupd owns the update policy; Common's shared layer (already overlaid) supplies
# /etc/uupd/config.json, the AC-connect udev rule and service, the post-suspend
# timer, and the ConditionACPower drop-in.
dnf5 install -y jq
local_packages_install /var/pluto-packages just gum fzf uupd

echo "::endgroup::"

echo "::group:: Enable update services"

# Enable explicitly rather than relying on the shipped preset, matching how
# 10-overlay.sh enables the Brew and Flatpak units.
systemctl enable uupd.timer
systemctl enable uupd-resume.timer

echo "::endgroup::"

# Restore default glob behavior
shopt -u nullglob
