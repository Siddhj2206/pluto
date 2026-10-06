# Testing

## Unit tests (CI)

```sh
gofmt -l .    # must print nothing
go vet ./...
go test ./...
```

These run on every push and pull request — see
[.github/workflows/ci.yml](../.github/workflows/ci.yml). Unit tests live in
`_test` packages next to the code they cover and fake their seams (`runner`,
`agent`, `system`, and the image builder's `Shell`), so they need no KVM,
network, or systemd.

## Image build (CI, no KVM)

[.github/workflows/image-build.yml](../.github/workflows/image-build.yml)
builds the base image with the Go builder and asserts the artifact imports. It
runs on pull requests that touch the image or build inputs — `images/**`,
`cmd/pluto-image-builder/**`, `internal/imagebuilder/**`, `go.mod`, `go.sum`, or
the workflow itself — and on pushes to `master` and `m3/platform-refresh`. No
KVM is needed: the job builds but never boots.

The job installs `faketime`. `ubuntu-latest` (24.04) ships e2fsprogs 1.47.0,
whose `mkfs.ext4` predates `SOURCE_DATE_EPOCH` support, so the builder falls
back to running mkfs under `faketime` at the pinned epoch. The two timestamp
paths — native `SOURCE_DATE_EPOCH` for e2fsprogs ≥ 1.47.1 and the `faketime`
fallback — are unit-tested through the shell seam by `TestAssemble` /
`TestAssembleFaketimeFallback`.

The job runs, in order:

```sh
go run ./cmd/pluto-image-builder                     # build images/out
go test ./internal/imagebuilder -run Mismatch -v     # the checksum path
# copy pins.yaml, corrupt a sha256, run the builder; require exit non-zero
# and a "checksum mismatch" message
PLUTO_IMAGE_ARTIFACT=images/out \
  go test ./internal/runner -run TestImportBuiltArtifact -v   # the artifact imports
```

`TestImportBuiltArtifact` drives the same path as `pluto image import`, which
re-verifies every hash against the manifest, so a truncated or substituted
artifact fails the job. It skips unless `PLUTO_IMAGE_ARTIFACT` is set, keeping
`go test ./...` hermetic. `TestBuildRejectsKernelChecksumMismatch` proves the
checksum code path, and the job also feeds a genuinely bad pin to the real
builder: it copies `images/pins.yaml` to a temp file, corrupts the kernel's
`sha256`, and runs the builder, requiring a non-zero exit and a `checksum
mismatch`. Verification happens before the expensive podman build, so the step
is a download and a hash rather than a second rootfs build.

Two caches keep the job practical: `images/out/cache` holds the kernel and
Firecracker downloads, keyed on `images/pins.yaml`, so a pin-only PR skips both
downloads; `~/.local/share/containers` holds the rootless podman base image and
apt layer, keyed on the Containerfile and pins. `actions/setup-go` caches the Go
module and build caches.

The import check copies the ~2 GiB rootfs into a temp state dir, so a by-hand
run needs that much room under `TMPDIR` (`/tmp` is often a small tmpfs); on
GitHub's runners it lives on the root disk.

## Host-only tests (not in CI)

Host-only tests need unprivileged user namespaces, rootless podman, and
network; tests that boot a real box additionally need a writable `/dev/kvm`
and `/dev/net/tun` ([images/README.md](../images/README.md) lists the full
set). The image determinism test is gated behind the `host` build tag so CI
(no podman, no KVM) never runs it:

```sh
go run ./cmd/pluto-image-builder   # build the base image artifact
images/boot.sh    # boot it and verify ssh + egress
go test -tags host ./internal/imagebuilder/ -run TestDoubleBuildDeterminism \
  -v -timeout 30m  # two clean builds must be byte-identical (#71)
scripts/e2e-m1.sh # the M1 demo end to end: a schedule fires, the job lands
                  # in history, auto-pause sleeps the box, attach sees the
                  # work, and `pluto --device` drives the daemon over ssh
scripts/e2e-m2.sh # the M2 demo end to end: a declared `[sessions.agent]`
                  # starts under tmux and survives a client detach, keeps the
                  # box awake while it works, the box auto-pauses when it goes
                  # idle, wake restarts it from its on-disk state, and
                  # `pluto --device` attaches into it from a second machine
```

`scripts/e2e-m1.sh` and `scripts/e2e-m2.sh` run their loops against a scratch
repo with their own pluto binary, state dir, socket, D-Bus session, systemd
user manager, and throwaway sshd (an `sshd` binary is enough; no running
system ssh service or passwordless ssh to localhost is required). The M2
script also runs a hermetic stand-in agent declared as `[sessions.agent]` and
a throwaway `git daemon` serving a scratch bare origin the box pushes to.
Both exit non-zero naming the failed check, destroy their boxes, and remove
their scratch directories, so re-running is safe. Build the image from the
branch under test first (`go run ./cmd/pluto-image-builder`) — the guest agent
is baked in.

Scratch directories default under `${XDG_CACHE_HOME:-$HOME/.cache}/pluto-e2e`
— on the user's disk rather than a small tmpfs (`/tmp` is often size-limited
and cannot hold the image and box disks, ~2GB each). Keep `PLUTO_E2E_DIR`
short: Firecracker's API socket is a unix socket under the scratch dir and
caps the path near 107 bytes, so the script fails early with a clear message
if the resolved path is too long. `PLUTO_E2E_KEEP=1` keeps the scratch dir and
processes for debugging.

GitHub-hosted runners have no KVM, so box and end-to-end tests never run in
CI; run them on the host before merging changes to the runner, image, guest
agent, or box lifecycle.
