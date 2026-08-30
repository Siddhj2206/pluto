#!/usr/bin/bash

set -euo pipefail

###############################################################################
# DX Layer — developer experience stack (docker, adb, virtualization)
###############################################################################
# Installs the host-level dev tooling that belongs in the image: the docker-ce
# daemon stack (third-party repo), android-tools, and a minimal libvirt/qemu
# host daemon. User-level CLI tools stay in the Brewfile (finpilot-packages
# rule); virt-manager is a Flathub flatpak (user installs it).
#
# Manifest of record:     /ctx/build/packages/dx.toml
#   [fedora]              -> install_fedora_section (one transaction + assert)
#   [docker]              -> official docker-ce repo enabled for the install,
#                            then REMOVED (the bootc model: updates ride image
#                            rebuilds; multimedia.toml is the deliberate
#                            exception that keeps a repo enabled).
#
# Daemons are socket-activated (docker.socket + the six libvirt modular
# daemon sockets) — nothing runs at boot until first use.
#
# Group membership (docker/libvirt) is a per-user step, modeled on bluefin's
# devmode setup (pkexec append-group from /usr/lib/group + usermod): run
# `ujust setup-groups` after first login.
###############################################################################

# Source helper functions
# shellcheck source=/dev/null
source /ctx/build/scripts/package-lib.sh

PKGS_TOML=/ctx/build/packages/dx.toml

echo "::group:: Install DX Fedora Packages"

install_fedora_section "${PKGS_TOML}" "dx fedora packages"

echo "::endgroup::"

echo "::group:: Install Docker (docker-ce stable repo)"

# docker is not in Fedora repos — enable the official repo, install, then
# remove the repo file so the final image ships no third-party repo state
# (AGENTS.md rule 3 spirit; updates arrive with each image rebuild).
cat >/etc/yum.repos.d/docker-ce.repo <<'EOF'
[docker-ce-stable]
name=Docker CE Stable - $basearch
baseurl=https://download.docker.com/linux/fedora/$releasever/$basearch/stable
enabled=1
gpgcheck=1
gpgkey=https://download.docker.com/linux/fedora/gpg
EOF

readarray -t DOCKER_PKGS < <("${READ_PKGS}" "${PKGS_TOML}" docker)
dnf5 install -y --enablerepo=docker-ce-stable "${DOCKER_PKGS[@]}"
rm -f /etc/yum.repos.d/docker-ce.repo
# Presence assert only — vendor asserting is deliberately skipped for now
# (containerd.io's %{VENDOR} is empty while docker-ce*'s is "Docker").
# The --enablerepo=docker-ce-stable pin keeps the install scoped to the
# official repo.
assert_packages_present "docker packages" "${DOCKER_PKGS[@]}"

echo "::endgroup::"

echo "::group:: Enable Socket-Activated Daemons"

# docker + the libvirt MODULAR daemons start on first socket use — no
# daemons at boot. libvirtd.socket no longer exists on F34+ (modular
# split); the host-proven set (neptuno, virsh qemu:///system working) is
# virtqemud + virtnetworkd (default NAT network) + virtnodedevd +
# virtstoraged + virtsecretd + virtproxyd (legacy-socket compat). All six
# unit files arrive via libvirt-daemon-qemu's Requires chain (incl.
# libvirt-daemon-proxy) — no extra packages needed.
systemctl enable docker.socket
systemctl enable virtqemud.socket virtnetworkd.socket virtnodedevd.socket virtstoraged.socket virtsecretd.socket virtproxyd.socket

echo "::endgroup::"

echo "DX layer complete!"
