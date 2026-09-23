#!/usr/bin/env bats
# Unit tests for build/20-packages-and-services.sh.
#
# pluto's package phase reads build/packages/image.toml and installs the
# [hummingbird] section from the base image and each ["pluto-packages:*"]
# section from pluto's factory repository (build/local-packages-helpers.sh
# writes a temporary file:// repo from the bind-mounted image). The tests
# rewrite a throwaway copy to a sandbox context and stub dnf5, systemctl,
# rsync and rpm.
#
# Run with: bats tests/template/20-packages-and-services_test.bats

SCRIPT_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BUILD_SRC="${REPO_ROOT}/build/20-packages-and-services.sh"

setup() {
	TEST_ROOT="${BATS_TEST_TMPDIR:-${BATS_TMPDIR}}/20-packages.${BATS_TEST_NUMBER:-0}.$$"
	CTX="${TEST_ROOT}/ctx"
	STUB_BIN="${TEST_ROOT}/stub-bin"
	SCRIPT="${TEST_ROOT}/20-packages-and-services.sh"
	REPO_DIR="${TEST_ROOT}/yum.repos.d"

	DNF5_LOG="${TEST_ROOT}/logs/dnf5.log"
	SYSTEMCTL_LOG="${TEST_ROOT}/logs/systemctl.log"
	RSYNC_LOG="${TEST_ROOT}/logs/rsync.log"
	RPM_LOG="${TEST_ROOT}/logs/rpm.log"

	mkdir -p "${STUB_BIN}" "${TEST_ROOT}/logs" \
		"${CTX}/build/scripts" "${CTX}/build/packages" "${REPO_DIR}"

	# The real helpers, reader and manifest are used verbatim so a break in any
	# of them fails this suite too.
	cp "${REPO_ROOT}/build/local-packages-helpers.sh" "${CTX}/build/local-packages-helpers.sh"
	cp "${REPO_ROOT}/build/scripts/package-lib.sh" "${CTX}/build/scripts/package-lib.sh"
	cp "${REPO_ROOT}/build/scripts/read-packages" "${CTX}/build/scripts/read-packages"
	chmod +x "${CTX}/build/scripts/read-packages"
	cp "${REPO_ROOT}/build/packages/image.toml" "${CTX}/build/packages/image.toml"

	sed -e "s#/ctx/#${CTX}/#g" "${BUILD_SRC}" >"${SCRIPT}"

	export PATH="${STUB_BIN}:${PATH}"
	export DNF5_LOG SYSTEMCTL_LOG RSYNC_LOG RPM_LOG
	# Keep the temporary repo file out of the real /etc/yum.repos.d, and point
	# the helpers at the sandboxed reader and manifest.
	export PLUTO_PACKAGES_REPO_DIR="${REPO_DIR}"
	export READ_PKGS="${CTX}/build/scripts/read-packages"

	for tool in dnf5 systemctl rsync rpm; do
		local log_var
		log_var="$(printf '%s' "${tool}" | tr '[:lower:]' '[:upper:]')_LOG"
		cat >"${STUB_BIN}/${tool}" <<EOF
#!/usr/bin/bash
printf '%s\n' "\$*" >> "\${${log_var}}"
exit 0
EOF
		chmod +x "${STUB_BIN}/${tool}"
	done
}

teardown() {
	rm -rf "${TEST_ROOT}"
}

@test "20-packages-and-services: sandbox rewrite left no writes to the host filesystem" {
	# Guards the rewrite above: if the script's paths change, the sed no longer
	# matches and the suite would write to the real filesystem.
	run grep -nE '(^|[^-[:alnum:]])/ctx/' "${SCRIPT}"
	[ "$status" -ne 0 ]

	grep -q "source ${CTX}/build/local-packages-helpers.sh" "${SCRIPT}"
}

@test "20-packages-and-services: completes successfully" {
	run bash "${SCRIPT}"
	[ "$status" -eq 0 ]
}

@test "20-packages-and-services: emits GitHub Actions group markers" {
	run bash "${SCRIPT}"
	[ "$status" -eq 0 ]
	[[ "$output" == *"::group:: Install Default Packages"* ]]
	[[ "$output" == *"::group:: Enable update services"* ]]
	[[ "$output" == *"::endgroup::"* ]]
}

@test "20-packages-and-services: installs the [hummingbird] section from the base image" {
	run bash "${SCRIPT}"
	[ "$status" -eq 0 ]

	mapfile -t calls <"${DNF5_LOG}"
	[ "${calls[0]}" = "install -y jq" ]
}

@test "20-packages-and-services: installs each pluto-packages section from the factory repo" {
	run bash "${SCRIPT}"
	[ "$status" -eq 0 ]

	mapfile -t calls <"${DNF5_LOG}"
	printf '%s\n' "${calls[@]}" | grep -q 'install --enablerepo=pluto-packages uupd microcode_ctl'
	printf '%s\n' "${calls[@]}" | grep -q -- '--enablerepo=pluto-packages.*linux-firmware'
	# The one-shot repo file is removed before the layer is committed.
	[ ! -e "${REPO_DIR}/pluto-packages.repo" ]
}

@test "20-packages-and-services: enables the update timers" {
	run bash "${SCRIPT}"
	[ "$status" -eq 0 ]

	mapfile -t calls <"${SYSTEMCTL_LOG}"
	[ "${#calls[@]}" -eq 2 ]
	[ "${calls[0]}" = "enable uupd.timer" ]
	[ "${calls[1]}" = "enable uupd-resume.timer" ]
}

@test "20-packages-and-services: performs no overlays or service enablement beyond its own" {
	# Boundary guard for the phase split: the filesystem overlays belong to
	# 10-overlay.sh, so a package change never invalidates them.
	run bash "${SCRIPT}"
	[ "$status" -eq 0 ]

	[ ! -e "${RSYNC_LOG}" ]
}

@test "20-packages-and-services: sources local-packages-helpers.sh so local_packages_install is available" {
	cat >>"${SCRIPT}" <<'EOF'
declare -F local_packages_install >/dev/null && echo "HELPER_PRESENT"
EOF
	run bash "${SCRIPT}"
	[ "$status" -eq 0 ]
	[[ "$output" == *"HELPER_PRESENT"* ]]
}

@test "20-packages-and-services: fails fast when local-packages-helpers.sh is missing from the context" {
	rm -f "${CTX}/build/local-packages-helpers.sh"
	run bash "${SCRIPT}"
	[ "$status" -ne 0 ]
}

@test "20-packages-and-services: restores default glob behaviour before finishing" {
	cat >>"${SCRIPT}" <<'EOF'
shopt -q nullglob || echo "NULLGLOB_OFF"
EOF
	run bash "${SCRIPT}"
	[ "$status" -eq 0 ]
	[[ "$output" == *"NULLGLOB_OFF"* ]]
}
