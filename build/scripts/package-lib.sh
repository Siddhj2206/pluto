#!/usr/bin/bash
###############################################################################
# Shared package-manifest helpers (sourced by layer scripts).
###############################################################################
# Depends on the read-packages script (default below; override before
# sourcing if the path ever moves).
READ_PKGS="${READ_PKGS:-/ctx/build/scripts/read-packages}"

# Fedora chroot for `dnf5 copr` commands. The Hummingbird base's os-release
# ID/VERSION_ID is "hummingbird/20251124", which dnf5 copr's auto-detection
# turns into a bogus "hummingbird-20251124-x86_64" chroot (the plugin reads
# os-release directly, not /etc/dnf/vars). Feed it the real chroot, built
# from the releasever pinned in STEP 9 (FEDORA_MAJOR_VERSION ARG).
copr_chroot() {
	printf 'fedora-%s-%s' "$(cat /etc/dnf/vars/releasever)" "$(uname -m)"
}

# Install a manifest's [fedora] section in one dnf5 transaction and assert
# every listed package landed (the build fails listing missing names).
# Trailing arguments are passed through to dnf5 (e.g. --enablerepo).
install_fedora_section() {
	local manifest="$1"
	local label="$2"
	shift 2
	local -a packages

	readarray -t packages < <("${READ_PKGS}" "${manifest}" fedora)
	dnf5 install -y "$@" "${packages[@]}"
	assert_packages_present "${label}" "${packages[@]}"
}

# Install every ["copr:<owner>/<project>"] section of a manifest. COPRs are
# enabled only for the build layers that install from them; clean-stage.sh
# disables them in the final image (AGENTS.md rule 3).
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
		# Explicit chroot — the base's os-release would make auto-detection
		# produce a bogus "hummingbird-20251124-x86_64" chroot.
		dnf5 -y copr enable "${copr_id}" "$(copr_chroot)"
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

# Assert every package argument is provided by the given RPM VENDOR string —
# catches a repo-priority slip silently swapping in a different build.
assert_vendor() {
	local label="$1"
	local vendor="$2"
	shift 2
	local missing=()
	local pkg

	for pkg in "$@"; do
		rpm -q --qf "%{NAME} %{VENDOR}\n" "${pkg}" | grep -q "${vendor}" || missing+=("${pkg}")
	done

	if [[ ${#missing[@]} -gt 0 ]]; then
		echo "ERROR: ${label} not sourced from ${vendor}: ${missing[*]}" >&2
		return 1
	fi
	echo "${label}: all $# packages from ${vendor}."
}
