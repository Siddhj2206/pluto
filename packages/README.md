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
├── packages/<group>/<name>/<name>.spec   # recipes, grouped for readability
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

## Caching

Caching is aggressive by design: a package rebuilds only when one of its inputs
changes.

- **Content-hash cache keys.** `packages/tools/cache_key.py` hashes the spec,
  every file in the recipe directory, the pinned source sha512s, the buildroot
  digest, the disttag, and the build script. Change any of them and the key
  changes.
- **Skip unchanged.** The published repository image carries
  `.pluto-cache.json`, mapping each package to its cache key and the RPMs it
  produced. `packages/tools/plan.py` rebuilds only packages whose key changed or
  whose RPMs are missing; everything else is reused.
- **Incremental publish.** `packages/tools/manifest.py` merges prior and new
  packages, prunes the RPMs a rebuild replaced, and carries other waves forward,
  so the repository accumulates instead of being rebuilt from scratch.
- **Buildroot dnf layer.** The build jobs persist `/var/cache/libdnf5` through
  `actions/cache`, keyed by the pinned buildroot digest and package.

The buildroot is pinned by digest in `factory-contract.json`, so a cache key is
meaningful across runs.

## Build flow

`.github/workflows/build-packages.yml` (manual dispatch, `wave` input):

1. **Plan** — pull the published repository, compare each package's cache key,
   and split the wave into build and reuse.
2. **Build** — for stale packages only: fetch and sha512-verify sources
   (`source_pipeline.py`), then
   `rpmbuild -ba <spec> --define "dist .hum1.pluto"` in the pinned Fedora 44 +
   Hummingbird Pulp overlay.
3. **Publish** — merge prior and new RPMs, update `.pluto-cache.json`,
   `createrepo_c`, and push `ghcr.io/siddhj2206/pluto-packages`.

Every wave but the first also mounts the published repository, so later waves
resolve build dependencies against earlier waves' RPMs.

The image side — a `FROM ghcr.io/siddhj2206/pluto-packages@sha256:… AS packages`
stage, bind-mounted into the package phase — is **not wired up yet**. When it
lands, Renovate owns the digest pin.

## Local commands

```bash
just packages-list 0        # list wave 0
just packages-validate      # contract + allow-list + recipe layout + spec sources
just packages-test          # tool unit tests
python3 packages/tools/cache_key.py --all      # current cache keys
python3 packages/tools/plan.py --wave 0 --prior <dir>   # what would rebuild
```

## Status

Two groups are seeded:

- `core/` — 16 recipes for the Wayland/graphics/input base, from
  `projectbluefin/utah-packages` (Apache-2.0) and Fedora dist-git.
- `base/` — the tools the image build itself invokes: `rsync`, `just` (`rust-just`),
  `gum`, `fzf`, `uupd`. Hummingbird provides `curl`, `dnf5`, `dnf5-plugins`,
  `jq`, `findutils`, `util-linux`, `sed`, `grep`, `coreutils`, `bootc`,
  `systemd`, `bash`, `tar`, `gzip`, `xz`; these five it does not.

Each recipe carries `.hummingbird-upstream.json` provenance. Later waves follow
`docs/research/pluto-closure-manifest.md`.

### Build-time normalisation

`build-in-container.sh` applies four adaptations for dist-git-seeded recipes:

- **`--nocheck`** — several upstream test suites need xattrs, SELinux, or
  network the build container does not have (rsync's xattrs tests fail on
  `security.selinux`). The factory builds packages; it does not run their tests.
- **`%autorelease`/`%autochangelog`** — rpmautospec expands these from git
  history, which the factory does not carry, so a literal release and a
  synthetic changelog entry are substituted.
- **Go vendoring** — recipes with a `go-vendor-tools.toml` get their
  `*-vendor.tar.*` source regenerated from the module graph before rpmbuild.
- **`rand_core` placeholder** — Fedora's `rust-rand_core-devel` 0.10.1 omits the
  `README.md` its crate includes; a placeholder is written so Rust builds that
  pull it succeed. Remove when Fedora fixes the package.
