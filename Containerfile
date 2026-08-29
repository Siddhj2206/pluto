###############################################################################
# PROJECT NAME CONFIGURATION
###############################################################################
# Name: pluto
#
# IMPORTANT: Change "pluto" above if you rename the project again.
# This name should be used consistently throughout the repository in:
#   - Justfile: export IMAGE_NAME := env("IMAGE_NAME", "your-name-here")
#   - README.md: # your-name-here (title)
#   - artifacthub-repo.yml: repositoryID: your-name-here
#   - custom/ujust/README.md: localhost/your-name-here:stable (in bootc switch example)
#
# The project name defined here is the single source of truth for your
# custom image's identity. When changing it, update all references above
# to maintain consistency.
###############################################################################

###############################################################################
# MULTI-STAGE BUILD ARCHITECTURE
###############################################################################
# This Containerfile follows the Bluefin architecture pattern as implemented in
# @projectbluefin/distroless. The architecture layers OCI containers together:
#
# 1. Context Stage (ctx) - Combines resources from:
#    - Local build scripts and custom files
#    - @projectbluefin/common - Desktop configuration shared with Aurora
#    - @ublue-os/brew - Homebrew integration
#
# 2. Base Image Options (edit the FROM line below):
#    - `quay.io/hummingbird-community/bootc-os` (Fedora Hummingbird minimal bootc OS — pluto's base)
#    - `quay.io/fedora-ostree-desktops/silverblue:44` (Fedora 44 and GNOME)
#    - `quay.io/centos-bootc/centos-bootc:stream10` (CentOS-based)
#
# See: https://docs.projectbluefin.io/contributing/ for architecture diagram
###############################################################################

# OCI context images - imported below and pinned directly in their FROM lines.
# The base image is pinned in the FROM line below and updated by Renovate.
FROM ghcr.io/projectbluefin/common:latest@sha256:be657eddde945b42c2e631b9e17f1786f948b757380a1e2ba504d826d0a0a8b1 AS common
FROM ghcr.io/ublue-os/brew:latest@sha256:e3b6878ed7b5ca963fd3f54ce44e6ab83da7533b28c83b2a11b92a5fedaa4adb AS brew

# Context stage - combine local and imported OCI container resources
FROM scratch AS ctx

COPY build /build
COPY custom /custom

# Copy from OCI containers to distinct subdirectories to avoid conflicts
COPY --from=common /system_files /oci/common
COPY --from=brew /system_files /oci/brew

# Base Image - Fedora Hummingbird bootc-os (minimal F44-era bootc OS, no desktop:
# the wm-agnostic layer is assembled by build/20-base.sh, the compositor
# layer by build/40-niri.sh). Rolling :latest — Renovate batches digest bumps.
FROM quay.io/hummingbird-community/bootc-os:latest@sha256:ad50d8ad73f21b639956d20f0891ccf0bf67e1809a1a8f58b76f4de5fb0e04d7

# Image identity - these define how bootc, fastfetch, and the ublue ecosystem
# recognize your image. Change these to match your project name.
ARG IMAGE_NAME="pluto"
ARG IMAGE_VENDOR="siddhj2206"
ARG UBLUE_IMAGE_TAG="stable"
# BASE_IMAGE_NAME / FEDORA_MAJOR_VERSION mirror the Hummingbird bootc-os base:
# the base (rolling :latest) carries Fedora-44-era content — the pulp repo
# tracks F44 versions (dnf5 5.4.x, gcc 16, fedora-gpg-keys 44) — but its
# os-release VERSION_ID is the hum build number, so it cannot
# self-report a Fedora releasever. FEDORA_MAJOR_VERSION IS the releasever
# (/etc/dnf/vars/releasever at build): single source of truth for pluto's
# Fedora stream, used by the added fedora repos and by COPRs. Bump it when
# the base rolls to the next Fedora stream.
ARG BASE_IMAGE_NAME="hummingbird"
ARG FEDORA_MAJOR_VERSION="44"
ARG VERSION=""
ARG SHA_HEAD_SHORT=""

### MODIFICATIONS
## Make modifications desired in your image and install packages by modifying the build scripts.
## The following RUN directives mount the ctx stage which includes:
##   - Local build scripts from /build
##   - Local custom files from /custom
##   - Files from @projectbluefin/common at /oci/common (includes branding/artwork content)
##   - Files from @ublue-os/brew at /oci/brew
## Scripts are run in numerical order (10-build.sh, 20-example.sh, etc.)

RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/00-image-info.sh

# Set dnf options before build scripts (persists across subsequent RUN layers).
# The Hummingbird base ships ONLY its pulp repo (curated subset) and its
# os-release VERSION_ID is the hum build number — so pluto adds the Fedora
# repos (custom/files/, copied via the ctx mount) and feeds $releasever from
# the FEDORA_MAJOR_VERSION ARG (single source of truth for the Fedora
# stream; repo files use $releasever, giving COPR repo URLs the right
# chroot too). dnf5-plugins provides config-manager + copr (bare dnf5 in
# the base has neither). rsync is needed by the very first overlay step.
#
# fedora-gpg-keys is bootstrapped in the SAME transaction with --nogpgcheck:
# the base's rpmdb inherited only the F43 signing key, and Fedora stopped
# serving per-release key files over HTTPS (static.fedoraproject.org 404s
# for every release) — so the key package must install before gpgcheck can
# work. This one chicken-egg transaction skips signature checks (the mkosi
# bootstrap pattern); the package then provides
# /etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-<rel>-primary and every later
# transaction verifies normally via the repo files' file:// gpgkey — the
# same pattern as the base's own hummingbird.repo.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,dst=/var/cache/libdnf5 \
    cp /etc/dnf/dnf.conf /etc/dnf/dnf.conf.tmp \
    && mv /etc/dnf/dnf.conf.tmp /etc/dnf/dnf.conf \
    && mkdir -p /etc/dnf/vars \
    && printf '%s\n' "${FEDORA_MAJOR_VERSION}" > /etc/dnf/vars/releasever \
    && cp -v /ctx/custom/files/etc/yum.repos.d/fedora.repo /etc/yum.repos.d/ \
    && cp -v /ctx/custom/files/etc/yum.repos.d/fedora-updates.repo /etc/yum.repos.d/ \
    && dnf5 install -y --nogpgcheck dnf5-plugins rsync fedora-gpg-keys \
    && dnf5 config-manager setopt keepcache=1 install_weak_deps=0

RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,dst=/var/cache/libdnf5 \
    --mount=type=cache,dst=/var/cache/rpm-ostree \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/10-build.sh

### BASE PACKAGES
## Wm-agnostic desktop foundation (fonts, graphics, audio, portals, flatpak,
## display manager, ...). Manifest of record: build/packages/base.toml.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,dst=/var/cache/libdnf5 \
    --mount=type=cache,dst=/var/cache/rpm-ostree \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/20-base.sh

### MULTIMEDIA
## Full multimedia (ffmpeg + non-FOSS codecs) from the negativo17
## fedora-multimedia repo + mesa/VA overrides (bluefin pattern).
## Manifest of record: build/packages/multimedia.toml.
## Repo stays enabled in the image for runtime codec updates.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,dst=/var/cache/libdnf5 \
    --mount=type=cache,dst=/var/cache/rpm-ostree \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/25-multimedia.sh

### NIRI COMPOSITOR LAYER
## Wm-specific: niri + DMS stack (COPRs, stay enabled) + greeter/PAM/theme/
## flatpak-override wiring. Manifest of record: build/packages/niri.toml.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,dst=/var/cache/libdnf5 \
    --mount=type=cache,dst=/var/cache/rpm-ostree \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/40-niri.sh

### DX LAYER
## Developer experience stack: docker-ce daemon (third-party repo, removed
## after install), adb, minimal libvirt/qemu host daemon. Daemons are
## socket-activated. Manifest of record: build/packages/dx.toml.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,dst=/var/cache/libdnf5 \
    --mount=type=cache,dst=/var/cache/rpm-ostree \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/45-dx.sh

### CLEANUP
## Use Bluefin's clean-stage.sh to remove build artifacts before linting.
## /run is deliberately not mounted as tmpfs here: clean-stage.sh must remove
## image-layer files such as /run/dnf so bootc lint's nonempty-run-tmp check
## passes. The script tolerates busy Buildah bind mounts while clearing contents.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=tmpfs,dst=/tmp \
    --mount=type=tmpfs,dst=/boot \
    /ctx/build/clean-stage.sh

### /opt
## Makes /opt writeable by default. Needs to be here to make the main image
## build strict (no /opt there). This is for downstream images/stuff like k0s.
## If you need /opt as an immutable real directory for build-time packages
## (e.g. google-chrome, docker-desktop), replace the next line with:
##   RUN rm /opt && mkdir /opt
RUN rm -rf /opt && ln -s /var/opt /opt

### INIT
## Required for bootc images
CMD ["/sbin/init"]

### LINTING
## Verify final image and contents are correct. --fatal-warnings catches issues.
RUN bootc container lint --fatal-warnings
