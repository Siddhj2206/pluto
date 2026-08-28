#!/usr/bin/bash

set -euo pipefail

###############################################################################
# Multimedia — FULL codec coverage (FOSS + non-FOSS), bluefin pattern
###############################################################################
# Enables the negativo17 fedora-multimedia repo (priority 90), swaps mesa and
# friends for the "less-crippled" negativo17 builds (distro-sync + versionlock),
# then installs the full ffmpeg/gstreamer stack — non-FOSS codecs included.
#
# Manifest of record: /ctx/build/packages/multimedia.toml
#   [multimedia_overrides] -> distro-sync from fedora-multimedia + versionlock
#   [fedora]               -> one dnf5 install transaction (--enablerepo)
#   [vendor_assert]        -> negativo17.org vendor check (assert_vendor)
#
# The fedora-multimedia repo is intentionally KEPT enabled in the image
# (runtime codec updates) — same as bluefin, whose validate-repos.sh expects
# the repo file present. (COPR repos are the ones clean-stage.sh disables.)
###############################################################################

PKGS_TOML=/ctx/build/packages/multimedia.toml

# Source helper functions (install_fedora_section, assert_vendor)
# shellcheck source=/dev/null
source /ctx/build/scripts/package-lib.sh

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

install_fedora_section "${PKGS_TOML}" "multimedia packages" --enablerepo='fedora-multimedia'

echo "::endgroup::"

echo "::group:: Rebuild gdk-pixbuf Loader Cache"

# Register freshly installed image loaders (libheif, libjxl, ...) — without
# this, apps cannot decode those formats (ported from neptuno practice).
/usr/bin/gdk-pixbuf-query-loaders-64 --update-cache

echo "::endgroup::"

echo "::group:: Verify Negative17 Vendor"

# The [vendor_assert] manifest section lists the names that exist only in the
# fedora-multimedia repo — verify the vendor so a repo-priority slip can't
# silently swap them for Fedora's -free builds.
readarray -t VENDOR_PKGS < <("${READ_PKGS}" "${PKGS_TOML}" vendor_assert)
assert_vendor "multimedia" "negativo17.org" "${VENDOR_PKGS[@]}"

echo "::endgroup::"

echo "Multimedia layer complete!"
