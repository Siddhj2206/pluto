#!/usr/bin/bash
set -euo pipefail

###############################################################################
# Local package helpers
###############################################################################
# pluto builds its own RPMs (see packages/) and publishes them as the OCI
# repository image ghcr.io/<owner>/pluto-packages. The Containerfile bind-mounts
# that image read-only at /var/pluto-packages for the install steps, so the
# repository never lands in an image layer.
#
# The RPMs are not individually signed; the OCI image digest is the trust
# anchor, so the temporary repository uses gpgcheck=0. The file is removed
# before the layer is committed, and 90-cleanup.sh asserts it is gone.
###############################################################################

local_packages_install() {
	local repo_conf_dir="${PLUTO_PACKAGES_REPO_DIR:-/etc/yum.repos.d}"
	local repo_file="${repo_conf_dir}/pluto-packages.repo"

	local rpm_repo="${1:?local_packages_install: rpm repo dir required}"
	shift
	local packages=("$@")

	if [[ ${#packages[@]} -eq 0 ]]; then
		echo "ERROR: local_packages_install needs at least one package" >&2
		return 1
	fi

	printf '[pluto-packages]\nname=pluto packages\nbaseurl=file://%s\nenabled=1\ngpgcheck=0\npriority=1\n' \
		"${rpm_repo}" >"${repo_file}"

	echo "Installing from pluto-packages: ${packages[*]}"
	dnf5 -y install --enablerepo=pluto-packages "${packages[@]}"
	rm -f "${repo_file}"

	for package in "${packages[@]}"; do
		rpm -q "${package}" >/dev/null || {
			echo "ERROR: ${package} did not install" >&2
			return 1
		}
	done
}
