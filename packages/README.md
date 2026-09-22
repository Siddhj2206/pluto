# pluto packages

The package factory for pluto. It lives in this repository beside the image so
the two cannot drift: a package's spec, its pinned upstream source, the CI that
builds it, and the image that installs it are all reviewed in one place.

pluto is a Hummingbird-based image. Hummingbird's buildroot differs from
Fedora's, so packages that must be built against it cannot come from Copr. This
factory builds them against the Hummingbird buildroot itself and publishes the
result as an OCI repository image:

```text
ghcr.io/siddhj2206/pluto-packages
```

The image is consumed **by digest**. Its content is a `createrepo_c` repository;
the image digest is the trust anchor (RPMs are not individually signed yet).

## Why not Copr or Packit-as-a-Service

- **Hummingbird ABI.** Copr builds against Fedora chroots, which is the wrong
  buildroot for this image.
- **No external accounts.** No Fedora Account System, no Copr project, no Packit
  GitHub App, no API tokens. GitHub Actions and GHCR are the whole dependency.
- **No drift.** Factory and image are one repository, so a spec change and the
  install change land in the same PR.

Packit configuration is intentionally absent. A generated `.packit.yaml` with no
`jobs:` earns nothing; add one only when a concrete need appears.

## Layout

```text
packages/
├── config/
│   ├── factory-contract.json     # registry, disttag suffix, paths
│   └── upstream-sources.json     # allow-list of owned packages and their sources
├── packages/<name>/<name>.spec   # one recipe directory per owned package
├── tests/                        # pytest for the tools and the specs
└── tools/                        # source fetch/verify, audit, validation
```

Only packages listed in `config/upstream-sources.json` may build or publish. The
allow-list is the front door; a spec with no matching entry is ignored.

## Conventions

- **Disttag:** every build appends `--define "dist .hum1.pluto"`, matching
  `rpm_suffix` in `config/factory-contract.json`.
- **Buildroot:** specs are built in `quay.io/fedora/fedora:44` plus the
  Hummingbird package overlay, so the resulting RPMs match the image's base.
- **Provenance:** each recipe records where its spec came from (Fedora dist-git
  commit) so a refresh is reviewable.
- **License:** confirm redistribution rights before importing any upstream spec
  or tarball; record the license in the allow-list entry.

## Lifecycle

1. Add an entry to `config/upstream-sources.json` (name, version, source URL,
   `sha512`, license, `renovate` tracking).
2. Add `packages/<name>/<name>.spec`.
3. Build and publish with the package workflow; confirm the OCI image and its
   digest.
4. Pin the digest in the image's `Containerfile` (added when the image half is
   wired up) and install via `build/local-packages-helpers.sh`.

## Status

Scaffold only. No recipes or workflows yet; the allow-list is empty. See
`docs/research/` for the design reports.
