###############################################################################
# PROJECT NAME CONFIGURATION
###############################################################################
# Name: pluto
#
# The authoritative name at publish time is the repository name: build-image.yml
# derives IMAGE_NAME from ${{ github.event.repository.name }} and pushes the
# GHCR package under it. This value is the fallback for local `just build` and
# the image identity metadata.
#
# Two other files carry the name as a literal: the Justfile's IMAGE_NAME default
# and artifacthub-repo.yml's repositoryID. tests/contract/identity_test.bats
# fails when the three disagree. See "Quick start" in README.md.
###############################################################################

###############################################################################
# MULTI-STAGE BUILD ARCHITECTURE
###############################################################################
# This Containerfile follows the Bluefin architecture pattern as implemented in
# @projectbluefin/distroless. The architecture layers OCI containers together:
#
# 1. Context Stage (ctx) - Combines resources from:
#    - Local build scripts and custom files
#    - @projectbluefin/common - The shared desktop configuration and plumbing
#    - @ublue-os/brew - Homebrew integration
#
# 2. Base Image Options (edit the FROM line below):
#    - `quay.io/fedora-ostree-desktops/silverblue` (Fedora, GNOME desktop)
#    - `quay.io/fedora-ostree-desktops/base-main` (Fedora, no desktop)
#    - `quay.io/centos-bootc/centos-bootc:stream10` (CentOS-based)
#    - `quay.io/hummingbird-community/bootc-os` (Hummingbird-based, minimal)
#
# See: https://docs.projectbluefin.io/contributing/ for architecture diagram
###############################################################################

# OCI context images - imported below and pinned directly in their FROM lines.
# The base image is pinned in the FROM line below and updated by Renovate.
FROM ghcr.io/projectbluefin/common:latest@sha256:9ece33fb29d01c3d88140e8130ea1e35d2781e9de79e5660cc15e6758b712ea8 AS common
FROM ghcr.io/ublue-os/brew:latest@sha256:e9a72571b7644b6277f0638b6a3c5e497e265e1098ab91224567acbdeb8b74ea AS brew

# pluto's own RPM factory, published as an OCI repository image. Renovate owns
# the digest; the install phases read it read-only and never ship it.
FROM ghcr.io/siddhj2206/pluto-packages:latest@sha256:80ff9148c2ed66f06e16c29d27f1b9bd48021b4b5ff6529757827260a29c4a44 AS packages

# Context stage - combine local and imported OCI container resources
FROM scratch AS ctx

COPY build /build
COPY custom /custom

# Copy from OCI containers to distinct subdirectories to avoid conflicts
COPY --from=common /system_files /oci/common
COPY --from=brew /system_files /oci/brew

# Base Image - Hummingbird bootc-os: a minimal Fedora-based OS with no desktop,
# no fonts and no graphics. pluto assembles the rest on top from its factory.
# Renovate keeps the digest pin up to date.
FROM quay.io/hummingbird-community/bootc-os:latest@sha256:9d69f6f33f5af87c76b0d7f49387bc4b969271a8eb788970396d6eab2b5af8a2

# Image identity - these define how bootc, fastfetch, and the ublue ecosystem
# recognize your image. Change these to match your project name.
ARG IMAGE_NAME="pluto"
ARG IMAGE_VENDOR="projectbluefin"
ARG UBLUE_IMAGE_TAG="stable"
# Supplied by `just build` from the base image's FROM line.
ARG BASE_IMAGE_NAME=""
ARG VERSION=""

### MODIFICATIONS
## Make modifications desired in your image and install packages by modifying the build scripts.
## The following RUN directives mount the ctx stage which includes:
##   - Local build scripts from /build
##   - Local custom files from /custom
##   - Files from @projectbluefin/common at /oci/common (includes branding/artwork content)
##   - Files from @ublue-os/brew at /oci/brew
## Scripts run in the order of the RUN blocks below: image identity, runtime
## overlays, default packages and services, then cleanup. An activated example
## gets its own block between the package phase and the cleanup phase.

RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/00-image-info.sh

# Hummingbird ships dnf5 without its plugin commands, and the build scripts use
# config-manager and versionlock. Install the plugin from the base's own repo.
RUN --mount=type=cache,dst=/var/cache/libdnf5 \
    dnf5 install -y --setopt=install_weak_deps=False dnf5-plugins

# Set dnf options before build scripts (persists across subsequent RUN layers)
RUN cp /etc/dnf/dnf.conf /etc/dnf/dnf.conf.tmp \
    && mv /etc/dnf/dnf.conf.tmp /etc/dnf/dnf.conf \
    && dnf5 config-manager setopt keepcache=1 install_weak_deps=0

### BOOTSTRAP
## Hummingbird carries no rsync, which the overlay phase uses. Install it from
## pluto's factory, bind-mounted read-only at /var/pluto-packages.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=bind,from=packages,source=/repository,target=/var/pluto-packages,ro \
    --mount=type=cache,dst=/var/cache/libdnf5 \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/05-bootstrap.sh

### RUNTIME OVERLAYS
## Overlays Common's shared runtime layer and the Brew integration files, then
## copies the template's custom declarations (Brewfiles, ujust recipes, Flatpak
## preinstalls, /etc/skel seeds) and enables the units that consume them.
## This phase installs no packages; see the package phase below.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=cache,dst=/var/cache/libdnf5 \
    --mount=type=cache,dst=/var/cache/rpm-ostree \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/10-overlay.sh

### DEFAULT PACKAGES AND SERVICES
## Installs the image's default packages from pluto's factory and enables the
## services they provide. Packages live here, not in the overlay phase, so
## overlay edits cannot invalidate the package layer.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=bind,from=packages,source=/repository,target=/var/pluto-packages,ro \
    --mount=type=cache,dst=/var/cache/libdnf5 \
    --mount=type=cache,dst=/var/cache/rpm-ostree \
    --mount=type=tmpfs,dst=/boot \
    --mount=type=tmpfs,dst=/tmp \
    /ctx/build/20-packages-and-services.sh

### CLEANUP
## Finalises package and Flatpak sources, then prunes build artifacts before
## linting. /run is deliberately not mounted as tmpfs here: the script must
## remove image-layer files such as /run/dnf so bootc lint's nonempty-run-tmp
## check passes. It tolerates busy Buildah bind mounts while clearing contents.
RUN --mount=type=bind,from=ctx,source=/,target=/ctx \
    --mount=type=tmpfs,dst=/tmp \
    --mount=type=tmpfs,dst=/boot \
    /ctx/build/90-cleanup.sh

### /opt
## Makes /opt writeable by default. Needs to be here to make the main image
## build strict (no /opt there). This is for downstream images/stuff like k0s.
## If you need /opt as an immutable real directory for build-time packages
## (e.g. google-chrome, docker-desktop), replace the next line with:
##   RUN rm /opt && mkdir /opt
RUN rm -rf /opt && ln -s /var/opt /opt

### IMAGE METADATA
## The Containerfile owns the metadata schema baked into every image. Local
## builds and CI supply the dynamic values through `just build`; keeping these
## ARGs late prevents a new version or timestamp from invalidating package and
## overlay layers above.
ARG IMAGE_DESC="My Customized Universal Blue Image"
ARG IMAGE_CREATED=""
ARG IMAGE_LOGO_URL="https://avatars.githubusercontent.com/u/120078124?s=200&v=4"
ARG IMAGE_KEYWORDS="bootc,ublue,universal-blue"
ARG IMAGE_REF="main"
## The commit the image was built from. It is declared here, with the other
## volatile metadata, so a new commit only invalidates the label layer.
## Declaring it before 00-image-info.sh would invalidate the package and overlay
## layers on every commit, which is why os-release does not carry it.
ARG SHA_HEAD_SHORT=""

LABEL org.opencontainers.image.title="${IMAGE_NAME}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${SHA_HEAD_SHORT}" \
      org.opencontainers.image.description="${IMAGE_DESC}" \
      org.opencontainers.image.source="https://github.com/${IMAGE_VENDOR}/${IMAGE_NAME}/blob/${IMAGE_REF}/Containerfile" \
      org.opencontainers.image.url="https://github.com/${IMAGE_VENDOR}/${IMAGE_NAME}" \
      org.opencontainers.image.vendor="${IMAGE_VENDOR}" \
      org.opencontainers.image.created="${IMAGE_CREATED}" \
      io.artifacthub.package.readme-url="https://raw.githubusercontent.com/${IMAGE_VENDOR}/${IMAGE_NAME}/refs/heads/main/README.md" \
      io.artifacthub.package.logo-url="${IMAGE_LOGO_URL}" \
      io.artifacthub.package.keywords="${IMAGE_KEYWORDS}" \
      io.artifacthub.package.license="Apache-2.0" \
      io.artifacthub.package.deprecated="false" \
      containers.bootc="1"

### INIT
## Required for bootc images
CMD ["/sbin/init"]

### LINTING
## Verify final image and contents are correct. --fatal-warnings catches issues.
RUN bootc container lint --fatal-warnings
