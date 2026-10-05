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

Tests that boot a real box need a Linux host with a writable `/dev/kvm`,
unprivileged user namespaces, `/dev/net/tun`, and rootless podman
([images/README.md](../images/README.md) lists the full set):

```sh
images/build.sh   # build the base image artifact
images/boot.sh    # boot it and verify ssh + egress
```

GitHub-hosted runners have no KVM, so box and end-to-end tests never run in
CI; run them on the host before merging changes to the runner, image, guest
agent, or box lifecycle.
