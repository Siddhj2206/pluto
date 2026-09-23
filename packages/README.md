# pluto packages

The package factory for pluto. pluto is a Hummingbird-based image that ships
with **no Fedora repositories enabled**, so it builds its own desktop closure
and publishes it as an OCI repository image:

```text
ghcr.io/siddhj2206/pluto-packages
```

The image is a `createrepo_c` repository consumed **by digest**; the digest is
the trust anchor (RPMs are not individually signed yet). Nothing third-party is
enabled in the shipped image.

## Why a factory

- **Hummingbird has no desktop.** It is a ~3,500-package base OS overlay with no
  GTK, mesa, wayland, pipewire, Qt6, portals, or niri. The desktop closure has
  to come from somewhere.
- **No Fedora repos means owning the closure.** Every package the niri + DMS
  desktop needs that Hummingbird does not provide is built here.
- **Buildroot fidelity.** Packages are built in `quay.io/fedora/fedora:44` with
  the Hummingbird overlay, so they match the base the image ships.

Packit is intentionally absent. It builds in Fedora chroots (the wrong
buildroot) and needs FAS, Copr, and the GitHub App. A generated `.packit.yaml`
with no `jobs:` earns nothing; add one only when a concrete need appears.

## Layout

```text
packages/
├── config/
│   ├── factory-contract.json     # registry, disttag suffix, paths
│   └── upstream-sources.json     # allow-list: the only packages that may build
├── packages/<name>/<name>.spec   # one recipe directory per owned package
├── tests/                        # pytest for the tools
└── tools/                        # source fetch/verify, audit, validation, matrix
```

Only packages in `config/upstream-sources.json` may build or publish. The
allow-list is the front door: each entry pins `version`, `url`, `filename`, and
`sha512`, plus its dependency `wave` and `license`.

## Waves

The closure is built bottom-up. A package's `wave` is the longest dependency
depth inside the closure:

| Wave | Contents |
| ---- | -------- |
| 0 | Wayland/graphics/input base: `wayland`, `libdrm`, `pixman`, `libinput`, `mesa`, `libglvnd`, `libepoxy`, `vulkan-loader`, … |
| 1–2 | Supporting libraries |
| 3 | Qt6, KF6, pipewire, niri, quickshell |
| 4 | The DMS stack and applications |

`python3 packages/tools/packages.py --wave N` (or `just packages-list N`) lists a
wave. The full closure and its rationale live in
`docs/research/pluto-closure-manifest.md`.

## Build flow

`.github/workflows/build-packages.yml` (manual dispatch, `wave` input):

1. Fetch and sha512-verify every source (`source_pipeline.py`); fail closed.
2. `rpmbuild -ba <spec> --define "dist .hum1.pluto"` in Fedora 44 + the
   Hummingbird Pulp overlay.
3. Collect the RPMs, `createrepo_c`, publish `ghcr.io/siddhj2206/pluto-packages`.

Every wave but the first also mounts the already-published repository image, so
later waves resolve build dependencies against earlier waves' RPMs.

The image side — a `FROM ghcr.io/siddhj2206/pluto-packages@sha256:… AS packages`
stage, bind-mounted into the package phase — is **not wired up yet**. When it
lands, Renovate owns the digest pin.

## Local commands

```bash
just packages-list 0        # list wave 0
just packages-validate      # contract + allow-list + recipe layout + spec sources
just packages-test          # tool unit tests
```

## Status

Wave 0 is seeded: 16 recipes copied from `projectbluefin/utah-packages`
(Apache-2.0) and Fedora dist-git, with provenance in each recipe's
`.hummingbird-upstream.json`. Later waves follow the closure manifest.
