#!/usr/bin/bash
# Build one recipe's RPM inside the Hummingbird buildroot. Invoked by
# build-packages.yml with the repository at /repo, staged sources at /src, the
# previous waves' RPMs at /prior, output at /out, and PACKAGE/HB_URL set.
set -euo pipefail

PACKAGE="${PACKAGE:?PACKAGE must be set}"
HB_URL="${HB_URL:?HB_URL must be set}"

dnf install -qy --setopt=zchunk=false dnf-plugins-core rpm-build \
  || dnf install -qy --setopt=zchunk=false dnf-plugins-core rpm-build
printf '[hummingbird]\nname=hummingbird\nbaseurl=%s\nenabled=1\ngpgcheck=0\npriority=10\n' "${HB_URL}" \
  > /etc/yum.repos.d/hummingbird.repo
if compgen -G '/prior/*.rpm' >/dev/null; then
  printf '[prior]\nname=prior\nbaseurl=file:///prior\nenabled=1\ngpgcheck=0\npriority=1\n' \
    > /etc/yum.repos.d/prior.repo
fi

export RPMBUILD=/root/rpmbuild
mkdir -p "${RPMBUILD}"/{BUILD,RPMS,SOURCES,SPECS,SRPMS}

# Recipes may be grouped under any subdirectory of packages/packages (core/,
# niri/, …), so locate the recipe directory by name rather than by a fixed path.
recipe="$(dirname "$(find /repo/packages/packages -type f -path "*/${PACKAGE}/*.spec" -print -quit)")"
spec="$(find "${recipe}" -maxdepth 1 -name '*.spec' -print -quit)"
if [[ -z "${recipe}" || -z "${spec}" ]]; then
  echo "ERROR: no recipe found for ${PACKAGE}" >&2
  exit 1
fi

# Stage the verified main source, then every local file the spec references
# (patches, GPG keys, udev rules, helper scripts, license texts). The spec goes
# to SPECS separately.
cp /src/* "${RPMBUILD}/SOURCES/" 2>/dev/null || true
cp -a "${recipe}"/. "${RPMBUILD}/SOURCES/"
built_spec="${RPMBUILD}/SPECS/$(basename "${spec}")"
cp "${spec}" "${built_spec}"

# Go recipes that vendor their dependencies ship a generated *-vendor.tar.*
# source rather than committing it. Regenerate it from the module graph, which
# go.sum pins, before rpmbuild needs it.
if [[ -f "${recipe}/go-vendor-tools.toml" ]]; then
  dnf install -y --setopt=zchunk=false go-vendor-tools askalono-cli golang
  main_source="$(find "${RPMBUILD}/SOURCES" -maxdepth 1 -name '*.tar.*' ! -name '*-vendor.tar.*' -print -quit)"
  vendor_name="$(sed -n 's/^SHA512 (\(.*-vendor\.tar\.[a-z0-9]*\)).*/\1/p' "${recipe}/sources" 2>/dev/null | head -1)"
  if [[ -n "${main_source}" && -n "${vendor_name}" ]]; then
    ( cd "${RPMBUILD}/SOURCES" && go_vendor_archive create \
        -c "${recipe}/go-vendor-tools.toml" -O "${vendor_name}" \
        --compression "${vendor_name##*.}" "$(basename "${main_source}")" )
  fi
fi

# Normalize dist-git autorelease macros. rpmautospec expands %autorelease and
# %autochangelog from git history, which the factory does not carry, so pin a
# literal release and a synthetic changelog entry instead.
if grep -q '%autorelease' "${built_spec}"; then
  sed -i 's|^Release:.*%autorelease.*|Release: 1%{?dist}|' "${built_spec}"
fi
if grep -q '%autochangelog' "${built_spec}"; then
  sed -i 's|^%autochangelog.*|* Thu Jan 01 2026 pluto <pluto@example.invalid> - 1%{?dist}\n- Rebuilt by the pluto factory|' "${built_spec}"
fi

# Static buildrequires first. dnf5 builddep does not reliably install the output
# of %generate_buildrequires (the Rust crates), so emit the buildreqs SRPM and
# install from it explicitly. rpmbuild -br exits non-zero while those deps are
# still missing but writes the SRPM regardless.
dnf -y --setopt=zchunk=false builddep -D "_sourcedir ${RPMBUILD}/SOURCES" \
  "${built_spec}" \
  || dnf -y --setopt=zchunk=false builddep -D "_sourcedir ${RPMBUILD}/SOURCES" \
    "${built_spec}"
rpmbuild -br --nodeps -D "_sourcedir ${RPMBUILD}/SOURCES" \
  -D "_topdir ${RPMBUILD}" "${built_spec}" || true
if compgen -G "${RPMBUILD}/SRPMS/*.buildreqs.nosrc.rpm" >/dev/null; then
  dnf -y --setopt=zchunk=false builddep "${RPMBUILD}"/SRPMS/*.buildreqs.nosrc.rpm
fi

# Fedora's rust-rand_core-devel 0.10.1 ships no README.md although the crate
# includes it, which breaks any Rust build that pulls rand_core. Satisfy the
# include with a placeholder. Remove when Fedora fixes the package.
for dir in /usr/share/cargo/registry/*/; do
  if [[ -f "${dir}Cargo.toml" && ! -e "${dir}README.md" ]]; then
    printf '# placeholder added by the pluto factory\n' > "${dir}README.md"
  fi
done

# --nocheck: several upstream test suites need xattrs, SELinux, or network the
# build container does not have (rsync's xattrs tests fail on security.selinux).
# The factory builds packages, it does not validate upstream test suites.
rpmbuild -ba --nocheck "${built_spec}" \
  --define "_topdir ${RPMBUILD}" --define 'dist .hum1.pluto'
cp "${RPMBUILD}"/RPMS/*/*.rpm /out/
