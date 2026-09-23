#!/usr/bin/bash
###############################################################################
# Shared package-manifest helpers (sourced by layer scripts).
###############################################################################
# Adapted from neptuno's build/scripts/package-lib.sh. pluto is Model 3: it runs
# on Hummingbird and owns its desktop closure, so the only repository beyond the
# base is pluto's own factory (ghcr.io/<owner>/pluto-packages), bind-mounted at
# /var/pluto-packages. There are no Fedora or COPR sections.
#
# Depends on the read-packages script and on local_packages_install from
# build/local-packages-helpers.sh.
READ_PKGS="${READ_PKGS:-/ctx/build/scripts/read-packages}"
PLUTO_PACKAGES_DIR="${PLUTO_PACKAGES_DIR:-/var/pluto-packages}"

# Install a manifest's [hummingbird] section (the base image's own repository)
# in one dnf5 transaction and assert every listed package landed.
install_hummingbird_section() {
	local manifest="$1"
	local label="$2"
	local -a packages

	readarray -t packages < <("${READ_PKGS}" "${manifest}" hummingbird)
	if [[ ${#packages[@]} -eq 0 ]]; then
		echo "${label}: no packages."
		return 0
	fi
	dnf5 install -y "${packages[@]}"
	assert_packages_present "${label}" "${packages[@]}"
}

# Install a manifest's ["pluto-packages:<name>"] section from the factory repo.
# local_packages_install already asserts each package landed.
install_pluto_packages_section() {
	local manifest="$1"
	local section="$2"
	local label="$3"
	local -a packages

	readarray -t packages < <("${READ_PKGS}" "${manifest}" "${section}")
	if [[ ${#packages[@]} -eq 0 ]]; then
		echo "${label}: no packages."
		return 0
	fi
	local_packages_install "${PLUTO_PACKAGES_DIR}" "${packages[@]}"
}

# Assert every package argument is installed; first arg is a human label.
assert_packages_present() {
	local label="$1"
	shift
	local missing=()
	local pkg

	for pkg in "$@"; do
		rpm -q "${pkg}" >/dev/null 2>&1 || missing+=("${pkg}")
	done

	if [[ ${#missing[@]} -gt 0 ]]; then
		echo "ERROR: ${label} failed to install: ${missing[*]}" >&2
		return 1
	fi
	echo "${label}: $# packages present."
}

# Assert every package argument is absent; first arg is a human label.
assert_packages_absent() {
	local label="$1"
	shift
	local remaining=()
	local package

	for package in "$@"; do
		rpm -q "${package}" >/dev/null 2>&1 && remaining+=("${package}")
	done

	if [[ ${#remaining[@]} -gt 0 ]]; then
		echo "ERROR: ${label} failed to remove: ${remaining[*]}" >&2
		return 1
	fi
	echo "${label}: $# packages absent."
}
