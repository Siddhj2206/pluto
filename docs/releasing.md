# Releasing

Release artifacts are built by
[.github/workflows/release.yml](../.github/workflows/release.yml) when a
`v*` tag is pushed. Cutting the tag is the last step of a milestone: merge
the milestone branch to `main` first, then tag the merge commit, so the
artifacts and the default branch agree.

## Cut `v0.1.5`

The M3 platform refresh lands on the `m3/platform-refresh` branch and is
integrated by PR #81. After that PR is merged to `main`:

```sh
git checkout main
git pull
git tag v0.1.5
git push origin v0.1.5
```

The tag push triggers the workflow, which builds `pluto-linux-amd64`, writes
`checksums.txt`, and publishes both on the GitHub Release for the tag. Watch
the run under the repository's Actions tab, then download the binary and
confirm it reports the tag:

```sh
./pluto-linux-amd64 version   # pluto v0.1.5
```

An earlier `v0.1.0` cut followed the same procedure on `m2/agent-sessions`
(PR #62).

## What a release contains

- `pluto-linux-amd64` — the host CLI; `go install
  github.com/Siddhj2206/pluto/cmd/pluto@v0.1.5` builds the same binary from
  the module.
- `checksums.txt` — SHA-256 sums.

The base image (`cmd/pluto-image-builder`) is not published; it is built
locally from [`images/pins.yaml`](../images/pins.yaml) and stays that way for
now. Boxes are Firecracker microVMs on Linux x86_64, so there are no darwin or
arm64 binaries.

## How the version is stamped

The workflow injects the tag with
`-ldflags "-X github.com/Siddhj2206/pluto/internal/cli.Version=<tag>"`, so a
release binary reports it directly. A `go install ...@vX.Y.Z` binary has no
link-time override and reads the tag from its Go build info instead. Both
paths meet in `internal/cli/version.go`.
