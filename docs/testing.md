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
`agent`, `system`), so they need no KVM, network, or systemd.

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
