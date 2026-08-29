#!/usr/bin/bash

set -euo pipefail

###############################################################################
# Multimedia — full codecs via negativo17 (priority 90, kept enabled for
# runtime updates; bluefin disables — pluto intentionally diverges)
###############################################################################

PKGS_TOML=/ctx/build/packages/multimedia.toml

# Source helper functions (install_fedora_section, assert_vendor)
# shellcheck source=/dev/null
source /ctx/build/scripts/package-lib.sh

echo "::group:: Enable Multimedia Repo"
if ! grep -q fedora-multimedia <(dnf5 repolist); then
	dnf5 config-manager addrepo --from-repofile="https://negativo17.org/repos/fedora-multimedia.repo"
fi
dnf5 config-manager setopt fedora-multimedia.priority=90

echo "::endgroup::"

echo "::group:: Replace Mesa/VA Overrides"
# plain install (not distro-sync --repo): minimal base lacks intel-mediasdk etc,
# and --repo would block cross-repo deps (x265-libs -> numactl-libs); epoch 1: wins
readarray -t OVERRIDES < <("${READ_PKGS}" "${PKGS_TOML}" multimedia_overrides)
dnf5 install -y --enablerepo='fedora-multimedia' "${OVERRIDES[@]}"
dnf5 versionlock add "${OVERRIDES[@]}"

echo "::endgroup::"

echo "::group:: Install Multimedia Packages"

install_fedora_section "${PKGS_TOML}" "multimedia packages" --enablerepo='fedora-multimedia'

echo "::endgroup::"

echo "::group:: Rebuild gdk-pixbuf Loader Cache"
/usr/bin/gdk-pixbuf-query-loaders-64 --update-cache

echo "::endgroup::"

echo "::group:: Verify Negative17 Vendor"
readarray -t VENDOR_PKGS < <("${READ_PKGS}" "${PKGS_TOML}" vendor_assert)
assert_vendor "multimedia" "negativo17.org" "${VENDOR_PKGS[@]}"

echo "::endgroup::"

echo "Multimedia layer complete!"
