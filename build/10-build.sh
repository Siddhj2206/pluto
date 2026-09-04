#!/usr/bin/bash

set -euo pipefail

###############################################################################
# Overlay & Wiring — OCI overlays + custom tree (wm-agnostic)
###############################################################################

shopt -s nullglob

echo "::group:: Overlay Brew Integration Files"
rsync -rvKl /ctx/oci/brew/ /
echo "::endgroup::"

echo "::group:: Overlay Common Shared Files"
rsync -rvKl /ctx/oci/common/shared/ /

# bluefin cherry-picks (higher priority, --relative like neptuno)
rsync -rvK --relative \
	/ctx/oci/common/bluefin/./usr/share/ublue-os/just/ \
	/ctx/oci/common/bluefin/./usr/libexec/bonedigger-report \
	/ctx/oci/common/bluefin/./usr/lib/systemd/system/dconf-update.service \
	/ctx/oci/common/bluefin/./usr/share/flatpak/preinstall.d/bazaar.preinstall \
	/ctx/oci/common/bluefin/./etc/bazaar/ \
	/ctx/oci/common/bluefin/./usr/lib/systemd/user/bazaar.service \
	/ctx/oci/common/bluefin/./usr/share/ublue-os/flatpak-overrides/io.github.kolunmi.Bazaar \
	/ctx/oci/common/bluefin/./usr/lib/tmpfiles.d/bazaar-flatpak.conf \
	/

# presets for uupd, flatpak, brew
systemctl preset-all >/dev/null 2>&1 || true
systemctl --global preset-all >/dev/null 2>&1 || true
systemctl enable flatpak-preinstall.service # no preset — enabled explicitly

echo "::endgroup::"

echo "::group:: Copy Custom Files"
# brew: every Brewfile auto-installed at first login (hash-tracked)
mkdir -p /usr/share/ublue-os/homebrew/preinstall.d/
cp /ctx/custom/brew/*.Brewfile /usr/share/ublue-os/homebrew/preinstall.d/

# just: pluto recipes alongside 00-entry.just
mkdir -p /usr/share/ublue-os/just/
find /ctx/custom/ujust -iname '*.just' -exec printf "\n\n" \; -exec cat {} \; >>/usr/share/ublue-os/just/60-custom.just

# flatpak: consumed by flatpak-preinstall.service on first boot
mkdir -p /usr/share/flatpak/preinstall.d/
cp /ctx/custom/flatpaks/*.preinstall /usr/share/flatpak/preinstall.d/
echo "::endgroup::"

echo "::group:: Copy System Files"
# custom/files -> / (greetd, PAM, units, gschema)
rsync -rvKl /ctx/custom/files/ /
echo "::endgroup::"

echo "::group:: Copy User Config Defaults"
# custom/config -> /etc/skel (new users, wins over common skel)
mkdir -p /etc/skel
rsync -rvKl /ctx/custom/config/ /etc/skel/

echo "::endgroup::"

# Restore default glob behavior
shopt -u nullglob

echo "Overlay layer complete!"
