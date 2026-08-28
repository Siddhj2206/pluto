#!/usr/bin/bash
###############################################################################
# Shared package-manifest helpers (sourced by layer scripts).
###############################################################################
# Depends on READ_PKGS being set to the read-packages script path.

# Install every ["copr:<owner>/<project>"] section of a manifest.
# COPRs are enabled and deliberately KEPT enabled for runtime updates.
# Exits non-zero if any listed package did not install.
install_copr_sections() {
	local manifest="$1"
	local missing=()
	local copr_section copr_id
	local -a packages

	while IFS= read -r copr_section; do
		copr_id="${copr_section#copr:}"
		readarray -t packages < <("${READ_PKGS}" "${manifest}" "${copr_section}")
		echo "Installing ${packages[*]} from COPR ${copr_id}"
		dnf5 -y copr enable "${copr_id}"
		dnf5 -y install "${packages[@]}"
		for pkg in "${packages[@]}"; do
			rpm -q "${pkg}" >/dev/null 2>&1 || missing+=("${pkg}")
		done
	done < <("${READ_PKGS}" "${manifest}" --sections copr:)

	if [[ ${#missing[@]} -gt 0 ]]; then
		echo "ERROR: COPR packages failed to install: ${missing[*]}" >&2
		return 1
	fi
	echo "All COPR packages present."
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