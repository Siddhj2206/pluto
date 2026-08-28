#!/usr/bin/bash

set -euo pipefail

###############################################################################
# Multimedia — FULL codec coverage (FFOSS + non-FOSS), bluefin pattern
###############################################################################
# Enables the negativo17 fedora-multimedia repo (priority 90), swaps mesa and
# friends for the "less-crippled" negativo17 builds (distro-sync + versionlock),
# then installs the full ffmpeg/gstreamer stack — non-FOSS codecs included.
#
# Manifest of record: /ctx/build/packages/multimedia.toml
#   [multimedia_overrides] -> distro-sync from fedora-multimedia + versionlock
#   [fedora]               -> one dnf5 install transaction
#
# The fedora-multimedia repo is intentionally KEPT enabled in the image
# (runtime codec updates) — same as bluefin, whose validate-repos.sh expects
# the repo file present.
###############################################################################

# Source helper functions
# shellcheck source=/dev/null
source /ctx/build/copr-helpers.sh

READ_PKGS=/ctx/build/scripts/read-packages
PKGS_TOML=/ctx/build/packages/multimedia.toml

echo "::group:: Enable Multimedia Repo"

# Enable or install the negativo17 fedora-multimedia repofile, then raise its
# priority above the default repos (bluefin uses priority 90).
if ! grep -q fedora-multimedia <(dnf5 repolist); then
    dnf5 config-manager addrepo --from-repofile="https://negativo17.org/repos/fedora-multimedia.repo"
fi
dnf5 config-manager setopt fedora-multimedia.priority=90

echo "::endgroup::"

echo "::group:: Replace Mesa/VA Overrides"

# Swap mesa and friends for the negativo17 builds (fuller VA-API/overlay
# support), then versionlock so future Fedora updates don't clobber them.
readarray -t OVERRIDES < <("${READ_PKGS}" "${PKGS_TOML}" multimedia_overrides)
dnf5 distro-sync --skip-unavailable -y --repo='fedora-multimedia' "${OVERRIDES[@]}"
dnf5 versionlock add "${OVERRIDES[@]}"

echo "::endgroup::"

echo "::group:: Install Multimedia Packages"

readarray -t MULTIMEDIA_PACKAGES < <("${READ_PKGS}" "${PKGS_TOML}" fedora)
dnf5 install -y --enablerepo='fedora-multimedia' "${MULTIMEDIA_PACKAGES[@]}"

echo "::endgroup::"

echo "::group:: Verify Multimedia Packages"

MISSING=()
for pkg in "${MULTIMEDIA_PACKAGES[@]}"; do
    rpm -q "${pkg}" >/dev/null 2>&1 || MISSING+=("${pkg}")
done
if [[ ${#MISSING[@]} -gt 0 ]]; then
    echo "ERROR: the following multimedia packages failed to install: ${MISSING[*]}" >&2
    exit 1
fi
echo "All ${#MULTIMEDIA_PACKAGES[@]} multimedia packages present."

echo "::endgroup::"

echo "Multimedia layer complete!"