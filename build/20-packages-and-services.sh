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

# jq ships in Hummingbird. Everything else is built by pluto's factory
# (packages/packages/) and installed from the bind-mounted repository image.
dnf5 install -y jq
local_packages_install /var/pluto-packages \
	uupd microcode_ctl \
	linux-firmware linux-firmware-whence \
	amd-gpu-firmware intel-gpu-firmware nvidia-gpu-firmware amd-ucode-firmware \
	atheros-firmware brcmfmac-firmware iwlegacy-firmware \
	iwlwifi-dvm-firmware iwlwifi-mvm-firmware iwlwifi-mld-firmware \
	libertas-firmware mediatek-firmware mt7xxx-firmware nxpwireless-firmware \
	realtek-firmware qcom-wwan-firmware tiwilink-firmware liquidio-firmware \
	mlxsw_spectrum-firmware mrvlprestera-firmware netronome-firmware \
	qcom-accel-firmware qcom-firmware qed-firmware intel-vsc-firmware \
	cirrus-audio-firmware intel-audio-firmware dvb-firmware \
	alsa-firmware alsa-sof-firmware \
	ddcutil pciutils v4l-utils upower udisks2 lm_sensors ntfs-3g \
	wireless-regdb iw wireguard-tools wpa_supplicant bluez \
	alsa-utils alsa-tools rtkit sbc \
	libva libva-utils libvdpau intel-gmmlib intel-mediasdk intel-vpl-gpu-rt

echo "::endgroup::"

echo "::group:: Enable update services"

# Enable explicitly rather than relying on the shipped preset, matching how
# 10-overlay.sh enables the Brew and Flatpak units.
systemctl enable uupd.timer
systemctl enable uupd-resume.timer

echo "::endgroup::"

# Restore default glob behavior
shopt -u nullglob
