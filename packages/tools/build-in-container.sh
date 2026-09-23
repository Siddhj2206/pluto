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
recipe="/repo/packages/packages/${PACKAGE}"
spec="$(find "${recipe}" -maxdepth 1 -name '*.spec' -print -quit)"

# Stage the verified main source, then every local file the spec references
# (patches, GPG keys, udev rules, helper scripts, license texts). The spec goes
# to SPECS separately.
cp /src/* "${RPMBUILD}/SOURCES/" 2>/dev/null || true
cp -a "${recipe}"/. "${RPMBUILD}/SOURCES/"
cp "${spec}" "${RPMBUILD}/SPECS/"

# Static buildrequires first. dnf5 builddep does not reliably install the output
# of %generate_buildrequires (the Rust crates), so emit the buildreqs SRPM and
# install from it explicitly. rpmbuild -br exits non-zero while those deps are
# still missing but writes the SRPM regardless.
dnf -y --setopt=zchunk=false builddep -D "_sourcedir ${RPMBUILD}/SOURCES" \
  "${RPMBUILD}/SPECS/$(basename "${spec}")" \
  || dnf -y --setopt=zchunk=false builddep -D "_sourcedir ${RPMBUILD}/SOURCES" \
    "${RPMBUILD}/SPECS/$(basename "${spec}")"
rpmbuild -br --nodeps -D "_sourcedir ${RPMBUILD}/SOURCES" \
  -D "_topdir ${RPMBUILD}" "${RPMBUILD}/SPECS/$(basename "${spec}")" || true
if compgen -G "${RPMBUILD}/SRPMS/*.buildreqs.nosrc.rpm" >/dev/null; then
  dnf -y --setopt=zchunk=false builddep "${RPMBUILD}"/SRPMS/*.buildreqs.nosrc.rpm
fi

rpmbuild -ba "${RPMBUILD}/SPECS/$(basename "${spec}")" \
  --define "_topdir ${RPMBUILD}" --define 'dist .hum1.pluto'
cp "${RPMBUILD}"/RPMS/*/*.rpm /out/
