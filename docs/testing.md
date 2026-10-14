# Testing

## Unit tests (CI)

```sh
gofmt -l .    # must print nothing
go vet ./...
go test ./...
```

These run on every push and pull request — see
[.github/workflows/ci.yml](../.github/workflows/ci.yml). Unit tests live in
`_test` packages next to the code they cover and fake their seams (the
container engine and the vcs adapter, plus the daemon's box lifecycle), so they
need no container engine, network, or systemd.

CI also checks that generated files are current:

```sh
go generate ./...
git status --porcelain   # must be empty
```

`pluto.schema.json` and `pluto.yml.schema.json` are generated from
`internal/contract`; regenerate and commit them after changing those types.

## Real-engine smoke tests (opt-in)

The behavioral suite fakes the container engine. A small smoke test exercises
the real engine instead, gated behind the `container` build tag so it never
runs in CI:

```sh
go test -tags container ./internal/container \
  -run TestSmokeCreatesContainerAndRunsCommand -v
```

It uses `PLUTO_CONTAINER_ENGINE` (default: the first of `podman`/`docker` on
`PATH`) and `PLUTO_SMOKE_IMAGE` (default `ubuntu:24.04`). It creates a box,
runs a command inside it, and tears the box and its volume down. The in-box
engine tests (#149) use the same tag and additionally require an image that
carries a working rootless engine (`PLUTO_SMOKE_ENGINE_IMAGE`) and a host that
permits nested user namespaces.
