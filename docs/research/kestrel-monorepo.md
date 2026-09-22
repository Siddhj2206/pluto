# Kestrel's monorepo: one repo for the image and the package factory

**Date:** 2026-09-22
**Author:** research subagent (DeepSeek V4.1 Flash via OpenCode)
**Subject:** what `HuntedRaven7/Kestrel` actually does, how its package factory and
image factory share one repository, and how pluto should adopt the pattern instead
of Packit-as-a-Service + Copr.

**Method.** Authenticated `gh api` metadata, a full recursive tree, and a
`git clone --depth 1` of `HuntedRaven7/Kestrel` into `/tmp/opencode/kestrel`
(HEAD `c05a336`, 2026-09-22). Every claim cites a file path in the clone. Exact
commands and URLs are in [Sources](#sources).

**Prior reports.** This complements, and does not repeat,
`docs/research/utah-hummingbird-niri-dms.md` and
`docs/research/packit-monorepo-packages.md`. Kestrel is the third pattern the
user asked for.

---

## 0. TL;DR

1. **Kestrel is real and it is the pattern.** `HuntedRaven7/Kestrel` — *"Project
   Kestrel is a mono repo for all things Fedora Hummingbird wether that is a Bootc
   image or a package factory!"* — is a single repository holding:
   - a **package factory** (`pigeon/`) that builds RPMs from specs and publishes
     them as an **OCI repository image**, `ghcr.io/huntedraven7/pigeon`;
   - two **bootc images** (`warbler/` desktop, `woodpecker/` server) that consume
     that image;
   - shared config, docs, agent skills, and all workflow files.

2. **There is no Copr, no Packit-as-a-Service, no Packit lane, no FAS, no
   koje, and no GitHub App.** `grep -rni copr` over the tree returns only two
   comments (one in `pigeon/tools/sync_versions.py`, one about a dead URL in
   `build-stage.yml`). The only secrets any workflow needs are
   `secrets.GITHUB_TOKEN` and `GHCR_TOKEN`. The factory is the same
   **utah-packages CLI/hand-rolled `rpmbuild` + OCI** pattern, but *vendored into
   the image's own repository* so the two cannot drift.

3. **The consumption seam is an OCI `COPY --from` + a `file://` dnf repo that is
   deleted before the image ships.** `warbler/Containerfile` copies
   `ghcr.io/huntedraven7/pigeon@sha256:...:/repository` into `/etc/pigeon`;
   `install-packages.py` writes `/etc/yum.repos.d/pigeon.repo` with
   `baseurl=file:///etc/pigeon`; `clean-stage.sh` removes the repo file. Nothing
   third-party is enabled in the shipped image, and there is no COPR to disable.

4. **The pin is the contract, and Renovate owns it.** A custom Renovate regex
   manager bumps `ARG PIGEON_IMAGE_SHA=sha256:...` in the image Containerfiles.

5. **Caveat: Kestrel is five days old and half-finished.** It was created
   2026-09-17, has 300 commits from the owner plus 21 from Renovate, and its
   docs are stubs. `README.md` describes a `packit-srpm-pilot.yml` that does not
   exist; `docs/architecture.md` and `docs/building.md` point at a `PLAN.md`
   that is not in the tree; `publish_gate.py` and `install-packages.py` have
   stubbed core logic; every `PIGEON_IMAGE_SHA` and `BASE_IMAGE_SHA` build arg is
   still the literal `sha256:TODO_...`; and **all workflows are
   `workflow_dispatch` only** — there is no automatic "package build triggers
   image build" chain. Treat Kestrel as a **blueprint and a file layout**, not as
   a turnkey engine. This matters for the adoption plan (§8).

---

## 1. What Kestrel is

| Field | Value |
|---|---|
| Repository | `HuntedRaven7/Kestrel` (public, not a fork) |
| Description | "Project Kestrel is a mono repo for all things Fedora Hummingbird wether that is a Bootc image or a package factory!" |
| Created / pushed | 2026-09-17 / 2026-09-22 |
| Default branch | `main` |
| Language / license | C / Apache-2.0 |
| Commits | 300 by `HuntedRaven7`, 21 by `renovate[bot]` (contributors API) |
| Base image | `quay.io/hummingbird-community/bootc-os@sha256:9d69f6f33f5af87c76b0d7f49387bc4b969271a8eb788970396d6eab2b5af8a2` (`warbler/Containerfile:6`) — the *current live* digest from the prior report, Renovate-bumped |
| Desktop | **Mango** (wlroots compositor) + **Quickshell** + rofi + ghostty + awww, **SDDM autologin** (`README.md:3,51`; `warbler/system_files/shared/etc/sddm.conf.d/10-warbler-autologin.conf`) |
| Server | Woodpecker: podman, cockpit, uupd, k0s manifests (`woodpecker/`) |

Kestrel is the consolidated successor to the owner's earlier split repos. The
same account owns `HuntedRaven7/Perch` ("A mono-repo for Fedora Hummingbird Bootc
images! Crane, Sparrow and Warbler!"), `HuntedRaven7/Velociraptor` (a
Mango+Quickshell image), `HuntedRaven7/Archaeopteryx` (a server image),
`HuntedRaven7/utah-packages-1` (a fork of `projectbluefin/utah-packages`), and
`HuntedRaven7/FeatherPilot` (a finpilot base). Kestrel folds that family into
one tree: **a package factory plus two products.**

**Relationship to projectbluefin/ublue:** Kestrel borrows the *architecture*, not
the repos. Its `pigeon/` is a copy of `projectbluefin/utah-packages`'
Packit-monorepo shape and source-verification model; its `warbler/`/`woodpecker/`
follow the `utah` bootc compose model; its `renovate.json` pins
`projectbluefin/actions`; `config/flavors.json` tracks `ublue-os/uupd`; and its
`Justfile` has a `sync-bluefin-toml` target pulling
`projectbluefin/bluefin-cli/main/base.toml`. It ships no GNOME, no niri, and no
DMS.

---

## 2. Monorepo layout

Complete top-level and second-level tree
(`gh api repos/HuntedRaven7/Kestrel/git/trees/main?recursive=1`, 1,444 entries):

```text
Kestrel/
├── AGENTS.md                     # skill router + non-negotiable safety
├── Justfile                      # check, test, build-pigeon, build-warbler, iso, srpm, import
├── README.md
├── LICENSE                       # Apache-2.0
├── renovate.json                 # custom managers for every pin
│
├── config/
│   └── flavors.json              # single source for image/flavor/stream matrix
│
├── docs/
│   ├── SKILL.md
│   ├── architecture.md           # stub
│   ├── building.md               # stub
│   └── targeting-hummingbird.md  # stub
│
├── .agents/skills/               # 8 skills, one per domain
│   ├── ci-release/SKILL.md
│   ├── mango-quickshell/SKILL.md
│   ├── pigeon-packaging/SKILL.md
│   ├── pigeon-source-verify/SKILL.md
│   ├── review/SKILL.md
│   ├── sddm-autologin/SKILL.md
│   ├── warbler-image/SKILL.md
│   └── woodpecker-server/SKILL.md
│
├── .github/
│   ├── actions/stage-sources/action.yml   # shared source staging composite
│   └── workflows/                         # 11 files (see §4)
│       ├── validate.yml
│       ├── rebuild-pigeon.yml
│       ├── build-stage.yml
│       ├── srpm.yml
│       ├── build-warbler.yml
│       ├── build-woodpecker.yml
│       ├── build-iso.yml
│       ├── import-package.yml
│       ├── recalculate-gaps.yml
│       ├── seed-dnf-cache.yml
│       └── drift-check.yml
│
├── pigeon/                       # ── PACKAGE FACTORY ──
│   ├── .packit.yaml              # generated; `packages:` map, NO `jobs:`
│   ├── config/
│   │   ├── factory-contract.json # registry + disttag suffix + allow-list path
│   │   └── upstream-sources.json # THE ALLOW-LIST: 374 entries
│   ├── packages/                 # 145 recipe dirs: <name>/<name>.spec + patches
│   ├── tests/                    # 15 pytest modules
│   └── tools/                    # 17 python tools + import shell scripts
│       ├── source_pipeline.py    # fetch + SHA-512/signature verify (fail-closed)
│       ├── audit_sources.py      # every SourceN/PatchN satisfiable before rpmbuild
│       ├── render_packit_config.py
│       ├── packit_source0.py
│       ├── packit_workflow.py    # package list + 250-job matrix chunking
│       ├── sync_versions.py      # carry Renovate lock bumps into specs
│       ├── fetch_vendored.py
│       ├── validate.py
│       ├── publish_gate.py       # precedence + transaction gate (stubbed)
│       ├── package_cache_key.py, package_inventory.py
│       ├── drift_check.py, spec_source_alias.py, factory_contract.py
│       └── bulk-import.sh, bulk-import-all.sh, batch-import-todo.sh
│
├── warbler/                      # ── DESKTOP IMAGE ──
│   ├── Containerfile
│   ├── Containerfile.kernel      # OGC kernel + NVIDIA module cache image
│   ├── contracts/desktop.toml
│   ├── packages/
│   │   ├── warbler.toml          # install contract (versioned + "*")
│   │   ├── bluefin.toml          # Bluefin parity manifest (drift-checked)
│   │   ├── hummingbird.repo
│   │   └── fedora-44.repo
│   ├── scripts/
│   │   ├── install-packages.py
│   │   ├── install-packages.sh
│   │   ├── verify-rpm-contract.py
│   │   ├── verify-desktop-contract.py
│   │   ├── configure-services.sh, configure-branding.sh
│   │   ├── clean-stage.sh
│   │   └── install-nvidia.sh, install-ogc-kernel.sh
│   ├── system_files/shared/      # SDDM autologin, mango config, awww service
│   └── iso/README.md
│
└── woodpecker/                   # ── SERVER IMAGE (same shape, no GUI) ──
    ├── Containerfile
    ├── contracts/server.toml
    ├── packages/woodpecker.toml
    ├── scripts/                  # install/verify/clean + k0s first-boot
    └── system_files/shared/
```

**The organizing idea:** one directory per *thing that is built*, and a shared
`config/` + `docs/` + `.github/` above them. The factory (`pigeon/`) and the
products (`warbler/`, `woodpecker/`) are siblings. There is no `common/` or
shared library between them yet; anything shared is either duplicated (each
product has its own `clean-stage.sh`) or lifted into `.github/actions/`
(`stage-sources`). The factory is self-contained under one directory.

---

## 3. How packages are defined and built

### 3.1 The allow-list is the front door

`pigeon/config/upstream-sources.json`:

```json
{
  "$comment": "Allow-list: a package with no entry here must not build or publish. Versions bumped by Renovate/Packit only.",
  "packages": {
    "ModemManager": {
      "version": "1.24.2",
      "url_template": "https://src.fedoraproject.org/repo/pkgs/rpms/ModemManager/ModemManager-{version}.tar.bz2/sha512/<hex>/ModemManager-{version}.tar.bz2",
      "sha512": "<hex>",
      "filename": "ModemManager-1.24.2.tar.bz2"
    },
    ...
  }
}
```

Facts measured on the clone:

- **374 allow-list entries**, but only **145 have a recipe directory** with a
  `.spec`. `packit_workflow.py packages` emits the 145 buildable ones and prints
  a stderr skip-note for the rest.
- Entries may carry `stage` (0–4), `note`, `license`, `extra_sources`
  (secondary tarballs with their own `sha512` + `track`), `vendored`, and
  `renovate: {datasource, depName}`. Only 39 entries have an explicit `stage`;
  `rebuild-pigeon.yml:64` defaults everything else to stage `2`.
- `factory-contract.json`:

  ```json
  {
    "contract": "kestrel-factory",
    "registry": "ghcr.io/huntedraven7/pigeon",
    "rpm_suffix": ".hum1.pigeon",
    "allow_list": "pigeon/config/upstream-sources.json"
  }
  ```

### 3.2 Specs

`pigeon/packages/<name>/<name>.spec` plus patches, `.sig`/keys, `sources`, and a
`.hummingbird-upstream.json` recording the Fedora dist-git provenance:

```json
{"remote": "https://src.fedoraproject.org/rpms/<pkg>.git",
 "commit": "<sha>", "tree": "<sha>", "time": <epoch>}
```

(`import-package.yml:74-81` writes this file.) Seeding is
`just import <pkg>` (dist-git rawhide → PR) or the `import-package.yml`
workflow. Disttag is `--define "dist .hum1.pigeon"` appended at build time
(`build-stage.yml`, rpmbuild step), matching `factory-contract.json.rpm_suffix`.

### 3.3 Packit is vestigial — quote the config, then note nobody runs it

`pigeon/.packit.yaml` (879 lines, generated; head verbatim):

```yaml
# Root-level Packit monorepo config for the Pigeon package factory.
# Generated by pigeon/tools/render_packit_config.py -- do not edit manually.
# Mirrors utah-packages/.packit.yaml: create-archive action stages the
# factory's already-verified Source0 archive (packit_source0.py) instead of
# letting Packit fetch upstream itself.
actions:
  create-archive:
    - bash -c 'python3 "$(git rev-parse --show-toplevel)/pigeon/tools/packit_source0.py"'
packages:
  ModemManager:
    specfile_path: pigeon/packages/ModemManager/ModemManager.spec
    upstream_package_name: ModemManager
    downstream_package_name: ModemManager
    paths:
      - pigeon/packages/ModemManager
  ...
```

- It has a top-level `actions:` and a `packages:` map and **no `jobs:`** — so
  Packit-as-a-Service would do nothing even if installed.
- `.agents/skills/pigeon-packaging/SKILL.md` is explicit: *"CI produces one per
  package on every build via `rpmbuild -br` into the stage artifacts. **No Packit
  lane exists**."* `srpm.yml` uses bare `rpmbuild -bs` in
  `quay.io/fedora/fedora:44`; `build-stage.yml` uses `rpmbuild -ba`.
- It is also in the wrong place for a real monorepo config (Packit requires the
  config at the git root; this is at `pigeon/`), and `packit_source0.py` resolves
  the git root itself. Since nothing invokes Packit, this is dead weight —
  evidence that Kestrel copied utah-packages' surface and then hand-rolled past
  it.

**The working config is `factory-contract.json` + `upstream-sources.json` + the
workflow YAML, not `.packit.yaml`.**

### 3.4 The actual binary build

`build-stage.yml` (a `workflow_call` reusable wave) does, per package:

1. checkout with `fetch-depth: 0`;
2. pull `quay.io/fedora/fedora:44` **by tag, not digest** — with a comment
   explaining that quay prunes old `fedora:44` manifests and digest pins rot
   (`build-stage.yml:44-47`);
3. restore a weekly shared libdnf5 cache (`seed-dnf-cache.yml`, single writer);
4. run the `stage-sources` composite: `fetch_vendored.py` (if `vendored`),
   `audit_sources.py --fix`, `source_pipeline.py fetch --output work/src
   --stage-into pigeon/packages`, copy vendored extras, generate `local`
   packages, then `spec_source_alias.py` so the spec's `Source0` basename
   resolves;
5. download prior-stage RPMs from the same run's artifacts (`gh run download`
   with a bounded retry loop, because `actions/download-artifact` flakes);
6. builddep against Fedora 44 **plus the Hummingbird Pulp overlay** and the prior
   wave's RPMs as a local repo:

   ```
   [hummingbird]
   baseurl=https://koji-s3-cache.hummingbird-project.io/packages.redhat.com/api/pulp-content/public-hummingbird/x86_64/
   enabled=1
   gpgcheck=0
   exclude=ruby* rubygem*
   ```

7. (`build-stage.yml:251`) comment: *"Hummingbird overlay repo. NOTE: the old
   Copr URL is dead; the live yum repo is the pulp-content endpoint below."*
8. handle `%generate_buildrequires` with a bounded retry (`rpmbuild -br`, install
   the generated buildreqs, repeat);
9. `rpmbuild -ba ... --define "dist .hum1.pigeon"`;
10. cache the result as an OCI image `ghcr.io/huntedraven7/pigeon-cache:<key>`
    and upload a stage artifact.

There is no Mock. It is raw `rpmbuild` in a pinned Fedora container, with the
Hummingbird overlay on the buildroot.

### 3.5 Stage ordering and the publish gate

`rebuild-pigeon.yml` (manual dispatch) computes a stage map from each lock
entry's `stage` field, then runs `srpm` → `rebuild0/merge0` → `rebuild1/merge1`
→ ... → `rebuild4/merge4` → `precedence` → `publish`. Each `mergeN` collapses a
wave's per-package artifacts into one `merged-stage-N` blob so later matrix jobs
make ~5 API calls instead of ~150 (`rebuild-pigeon.yml:156-160`).

- **`precedence`** runs `pigeon/tools/publish_gate.py --base-image
  quay.io/hummingbird-community/bootc-os:latest --pigeon-suffix .hum1.pigeon`.
  **This gate is stubbed**: `check_precedence` passes if any RPM exists
  (`publish_gate.py:32-39`) and `check_transaction` always passes
  (`publish_gate.py:47-50`). The real "does Pigeon outrank Hummingbird" check is
  future work.
- **`publish`** creates `~/repository`, copies every `stage-N` RPM in,
  `createrepo_c .`, signs `repodata/repomd.xml` with **cosign keyless**
  (`cosign sign-blob --yes`), then:

  ```dockerfile
  FROM scratch
  COPY repository /repository
  LABEL org.opencontainers.image.title="Pigeon RPM Repository"
  ...
  ```

  pushed as `ghcr.io/huntedraven7/pigeon:latest` (`rebuild-pigeon.yml:604-612`),
  SBOM'd with syft, SLSA-attested, and Trivy-scanned.

The published OCI image **exists**: `skopeo list-tags
docker://ghcr.io/huntedraven7/pigeon` returns `latest`, date tags, and
`sha256-<digest>.sig` entries (cosign signature blobs), e.g. digest
`sha256:6a195bbc...`. So the factory has shipped at least once.

`recalculate-gaps.yml` (every 6h) diffs the Hummingbird base + the Pigeon repo
against the Warbler/Woodpecker install contracts and reports what Pigeon still
has to absorb — the mechanism behind the README's goal that *"every package
installed in Warbler and Woodpecker will be built by Pigeon"* (`README.md:87-95`).

---

## 4. CI/CD

Eleven workflows. **All are `workflow_dispatch` or `workflow_call`; none are
triggered by push, release, or another workflow's completion.** A grep for
`workflow_run|repository_dispatch|gh workflow run` across `.github/workflows/`
finds no cross-workflow trigger. Two are reusable (`build-stage.yml` via
`workflow_call` with a required `GHCR_TOKEN` secret; `srpm.yml` via
`workflow_call`).

| Workflow | Trigger | Role |
|---|---|---|
| `validate.yml` | PR + push to `main` | `factory_contract.py`, `validate.py`, `sync_versions.py --check`, `audit_sources.py --spec-sources`, `pytest pigeon/tests` |
| `rebuild-pigeon.yml` | manual | Stage 0–4 RPM rebuild, precedence gate, publish OCI repo image |
| `build-stage.yml` | `workflow_call` | One RPM wave, `rpmbuild -ba` in `fedora:44` |
| `srpm.yml` | `workflow_call` | `rpmbuild -bs` per package, fail-fast |
| `seed-dnf-cache.yml` | weekly cron + manual | Single-writer dnf cache seed |
| `build-warbler.yml` | manual | Warbler image, kernel cache, per-flavor matrix, ISO, cosign, SBOM, SLSA, Trivy, promote |
| `build-woodpecker.yml` | manual | Same shape, server image |
| `build-iso.yml` | manual | Standalone bootc-image-builder ISO |
| `import-package.yml` | manual | Import a Fedora dist-git rawhide spec into `pigeon/packages/` and open a PR |
| `recalculate-gaps.yml` | 6h cron | Diff base + factory against image contracts |
| `drift-check.yml` | weekly cron | Compare pinned upstream commits against tracked branch HEAD |

**How they chain: they don't, automatically.** The intended flow is:

```
edit a spec / upstream-sources.json  →  PR  →  validate.yml
        │
        (human) gh workflow run rebuild-pigeon.yml
        │
        v
  publish ghcr.io/huntedraven7/pigeon:latest@sha256:NEW
        │
        (Renovate opens a PR updating ARG PIGEON_IMAGE_SHA in
         warbler/Containerfile and woodpecker/Containerfile)
        │
        (human merges; human) gh workflow run build-warbler.yml / build-woodpecker.yml
        │
        v
  ghcr.io/huntedraven7/warbler:<flavor>-testing  →  Trivy  →  :<flavor>-stable
```

`renovate.json` supplies the missing glue as a custom regex manager:

```json
{
  "customType": "regex",
  "description": "Containerfile PIGEON_IMAGE_SHA digest pin (active once pigeon publishes)",
  "managerFilePatterns": ["/warbler/Containerfile$/", "/woodpecker/Containerfile$/"],
  "matchStrings": ["ARG PIGEON_IMAGE_SHA=(?<currentDigest>sha256:[a-f0-9]+)"],
  "datasourceTemplate": "docker",
  "depNameTemplate": "ghcr.io/huntedraven7/pigeon"
}
```

**Secrets / accounts required:** none beyond GitHub.

- Actions authenticate to GHCR with the built-in `GITHUB_TOKEN`
  (`permissions: packages: write`), passed explicitly to the reusable
  `build-stage.yml` as `secrets.GHCR_TOKEN: ${{ secrets.GITHUB_TOKEN }}`
  (`rebuild-pigeon.yml:151-152`).
- Signing is **keyless cosign** on GitHub OIDC (`id-token: write`).
- SLSA provenance uses `slsa-framework/slsa-github-generator`.
- No GitHub App, no Copr token, no Fedora Account System account.

**Not yet wired:** every image build arg is a literal placeholder —
`ARG PIGEON_IMAGE_SHA=sha256:TODO_PIGEON_IMAGE_SHA`
(`warbler/Containerfile:11`, `woodpecker/Containerfile:9`) and
`BASE_IMAGE=...@sha256:TODO_BASE_IMAGE_SHA` in the workflows. The
`ghcr.io/huntedraven7/warbler` and `.../woodpecker` packages are **not published**
(anonymous `skopeo list-tags` returns 403; the API returns "Package not found"),
so the image half has never run end to end.

---

## 5. Release / consumption model

### 5.1 How the image consumes the factory

`warbler/Containerfile:6-11,25`:

```dockerfile
ARG BASE_IMAGE=quay.io/hummingbird-community/bootc-os@sha256:9d69f6f33f5af87c76b0d7f49387bc4b969271a8eb788970396d6eab2b5af8a2
ARG KERNEL_CACHE_IMAGE=ghcr.io/huntedraven7/warbler-kernel-cache@sha256:TODO_KERNEL_CACHE_SHA
ARG PIGEON_IMAGE=ghcr.io/huntedraven7/pigeon
ARG PIGEON_IMAGE_SHA=sha256:TODO_PIGEON_IMAGE_SHA
...
COPY --from=${PIGEON_IMAGE}@${PIGEON_IMAGE_SHA} /repository /etc/pigeon
```

So the factory's **entire `createrepo_c` output is copied into the image at
`/etc/pigeon`** — not bind-mounted (this differs from utah, which bind-mounts and
never copies). Then `warbler/scripts/install-packages.py:45-73` writes a repo
file and installs:

```python
repo_file = Path("/etc/yum.repos.d/pigeon.repo")
repo_file.write_text(f"""[pigeon]
name=Kestrel Pigeon Repository
baseurl=file://{args.repo}
enabled=1
gpgcheck=0
priority=5
""")

pigeon_pkgs = [f"{name}-{ver}" if ver != "*" else name for name, ver in pkgs.items() if ver != "*"]
base_pkgs   = [name for name, ver in pkgs.items() if ver == "*"]

if pigeon_pkgs:
    subprocess.run(["dnf", "install", "-y", "--repo=pigeon", *pigeon_pkgs], check=True)
if base_pkgs:
    subprocess.run(["dnf", "install", "-y", *base_pkgs], check=True)
```

`warbler/packages/warbler.toml` is the install contract: a `[warbler]` table of
`name = version` (and `name = "*"` for base-provided packages), plus an
`[unavailable]` table for tracked exclusions. A missing package fails the build.
`verify-rpm-contract.py` then asserts the installed RPM set matches the contract
(exact version match, release suffix ignored).

### 5.2 The hygiene model

There is no COPR to disable, so "no third-party repos enabled in the shipped
image" is achieved differently:

- The Pigeon repo file is created only for the install step and **deleted before
  the image ships** (`warbler/scripts/clean-stage.sh:39-41`):

  ```bash
  # Remove repo configs (Pigeon repo not needed at runtime)
  rm -f /etc/yum.repos.d/pigeon.repo
  rm -f /etc/yum.repos.d/ogc-kernel.repo
  ```

- `clean-stage.sh` also removes build tooling (`mock`, `rpm-build`, `gcc`,
  `meson`, `cmake`, ...), `dnf clean all`, `rm -rf /tmp/* /var/tmp/*`, docs,
  locales, logs, and machine-id. The Containerfile then runs
  `bootc container lint --fatal-warnings`.

**Gaps in the hygiene model, worth knowing before copying it:**

1. `/etc/pigeon` — the copied RPM repository — is **never removed.** Only the
   repo file is. So every image carries the factory's RPMs (potentially
   gigabytes) as dead weight unless a later step prunes it. utah's bind-mount
   design avoids exactly this.
2. `gpgcheck=0` on the `file://` repo. The *repomd.xml* is cosign-signed, but
   `dnf` never verifies it — no RPMs are GPG-signed and no key is installed. The
   cosign signature is provenance, not install-time verification.
3. `install-packages.py`'s `get_base_packages()` returns an empty set
   (`install-packages.py:26-30`), and wildcard packages install with a plain
   `dnf install` against whatever repos the base happens to have enabled.
   `warbler/packages/hummingbird.repo` and `fedora-44.repo` exist but the
   Containerfile copies `packages/` to `/etc/warbler/packages/`, **not** to
   `/etc/yum.repos.d/`, so those repo files are never enabled. On a minimal
   Hummingbird base this is a latent failure.
4. `COPY --from=${PIGEON_IMAGE}@${PIGEON_IMAGE_SHA}` relies on BuildKit
   expanding two ARGs concatenated with `@` inside `--from`. That is untested
   here (the SHAs are TODO). If it does not expand as expected, the fix is a
   single `ARG PIGEON_IMAGE_REF=ghcr.io/.../pigeon@sha256:...` and
   `COPY --from=${PIGEON_IMAGE_REF}`.

---

## 6. Compared to the other two patterns

| Axis | **utah-packages** (CLI factory) | **Packit-Service → Copr** (prior report's recommendation) | **Kestrel** (monorepo) |
|---|---|---|---|
| Repos | 2 (`utah` + `utah-packages`) | 2 (image + `.packit.yaml` in same repo, but builds leave to Copr) | **1** (factory + all images) |
| RPM build engine | Packit CLI for SRPMs; hand-rolled `rpmbuild` bands for binaries | Packit Service `copr_build` jobs | hand-rolled `rpmbuild -ba` in `fedora:44` (`build-stage.yml`); `.packit.yaml` present but **unused** |
| Binary publication | **OCI repo image** `ghcr.io/projectbluefin/utah-packages` | **Copr repo** (`copr.fedorainfracloud.org`) | **OCI repo image** `ghcr.io/huntedraven7/pigeon` |
| Consumer seam | `COPY --from=...@digest`, bind-mounted at install | `copr_install_isolated` (enable→disable→`--enablerepo`) | `COPY --from=...@digest` + `file://` repo file, deleted pre-ship |
| Third-party account needed | GitHub only (Copr explicitly *not* a dependency) | **FAS account + Copr project + Packit GitHub App** | **GitHub only** |
| Signing | cosign keyless | Copr's own signing | cosign keyless + SLSA + Trivy |
| Disttag | `.hum1.bfin` | Fedora `fcNN` (not Hummingbird) | `.hum1.pigeon` (Hummingbird-native buildroot) |
| Build root | Fedora 44 + Hummingbird Pulp overlay | Fedora chroot only (ABI risk for linked libs) | Fedora 44 + **Hummingbird Pulp overlay** |
| Coupling factory ↔ image | digest pin, Renovate | Copr repo, no pin | digest pin, Renovate; **same commit graph** |
| Automation | staged waves | Packit webhooks | staged waves, **manual triggers only** |
| Maturity | production-ish, 352 recipes, open design questions | standard Packit | **5 days old, stubs, image never published** |

**What Kestrel does differently, in one sentence each:**

- **vs utah-packages:** same OCI factory idea, but colocated with the image so a
  spec change and the image contract that consumes it are one PR. It also drops
  Packit entirely (utah-packages still runs Packit CLI for SRPM validation).
- **vs Packit-Service → Copr:** no external service, no Fedora account, no Copr
  namespace, no webhook. Everything is git + Actions + GHCR. It builds against
  the **Hummingbird overlay buildroot**, so it produces `hum1.pigeon` disttags
  rather than Fedora `fcNN` — which is the ABI-correctness problem the first
  report flagged and Packit cannot solve.

**Trade-offs for a one-person project:**

- *Attractive:* one repo, one clone, one CI surface; no FAS/Copr/App onboarding;
  the image installs packages by digest from a registry the owner already owns;
  `just check`/`just test` cover the factory locally; the whole supply chain
  (verify → build → sign → attest → scan) lives in-repo.
- *Costly:* you maintain the pipeline. Kestrel's own git log is a wall of
  "fix: artifact download 403", "fix: makedir under work/", "fix: harden
  build-stage" — the workflow is 600+ lines with hand-rolled retry loops and a
  shared dnf cache because the naive version exhausted the GitHub API rate limit
  with ~140 parallel matrix jobs. Budget real time for CI, not just specs.
- *Costly:* no Copr UI, no `copr-cli`, no automatic rebuild-on-build via
  webhook. Rebuilds are manual dispatches (or a cron you write). Renovate moves
  the digest pin; it does not trigger the image build.

---

## 7. What Kestrel is *not*

- **Not a niri or DMS source.** `grep -rniE 'niri|dankmaterial|danklinux|dms-cli|greetd'`
  over the tree returns nothing. The desktop is **Mango + Quickshell**, login is
  **SDDM autologin**, not greetd.
- **Not Packit-as-a-Service.** No `jobs:` anywhere; no App.
- **Not Copr.** Confirmed by grep; the only COPR mentions are a dead-URL comment
  and a Mock/Copr parser note.
- **Not finished.** See §0.5 and §9.

---

## 8. Adoption plan for pluto

Goal: graft Kestrel's *shape* — factory and image in one repo, RPMs published as
an OCI repo image, consumed by digest — onto pluto's existing finpilot layout,
without inheriting Kestrel's stubs. Keep the finpilot phase discipline:
`00-image-info.sh` → `10-overlay.sh` (files) → `20-packages-and-services.sh`
(packages) → `90-cleanup.sh` (hygiene + lint).

### 8.0 Model choice and naming

- Factory directory: **`packages/`** at the repo root (short; the finpilot
  template owns `build/`, `custom/`, `iso/`, `tests/`). Registry:
  `ghcr.io/siddhj2206/pluto-packages`. Disttag suffix: `.hum1.pluto`.
- Keep `.packit.yaml` **out** until there is a reason for it. Kestrel's proves
  a generated Packit map with no jobs earns nothing.
- Do **not** copy Kestrel's `publish_gate.py` stub; write the precedence check
  for real or skip it with a comment.

### 8.1 Files to add

```text
pluto/
├── packages/                              # NEW — the factory
│   ├── config/
│   │   ├── factory-contract.json          # registry, suffix, allow-list path
│   │   └── upstream-sources.json          # allow-list (start with 2 entries)
│   ├── packages/<name>/<name>.spec        # one recipe dir per owned package
│   ├── tests/test_*.py                    # ported from Kestrel (Apache-2.0)
│   └── tools/                             # ported from Kestrel, path-adjusted
│       ├── source_pipeline.py
│       ├── audit_sources.py
│       ├── spec_source_alias.py
│       ├── fetch_vendored.py
│       ├── validate.py
│       ├── sync_versions.py
│       └── packit_workflow.py             # matrix chunking (rename ok)
├── build/
│   └── local-packages-helpers.sh          # NEW — sibling of copr-helpers.sh
├── .github/
│   ├── actions/stage-sources/action.yml   # NEW — ported
│   └── workflows/
│       ├── validate-packages.yml          # NEW — factory checks on PR
│       ├── build-package.yml              # NEW — reusable rpmbuild wave
│       └── rebuild-packages.yml           # NEW — stages → publish OCI
└── docs/research/kestrel-monorepo.md      # this file
```

Do **not** move `build/`, `custom/`, `iso/`, `tests/` — reintroducing a
`warbler/`-style wrapper around the whole image would fight the finpilot
Justfile and `build-image.yml`, which expect those paths at the root.

### 8.2 `Containerfile` change (bind-mount, not `COPY`)

Keep the literal un-aliased base `FROM` (`just build` parses it,
`Justfile:150-157`). Add one aliased factory stage next to the existing context
stages (`Containerfile:38-49`):

```dockerfile
# pluto's own RPM factory, published as an OCI repo image. Renovate owns the
# digest; 20-packages-and-services.sh reads it read-only and never ships it.
FROM ghcr.io/siddhj2206/pluto-packages:latest@sha256:REPLACE_WITH_FIRST_PUBLISH AS packages
```

Then add `--mount=type=bind,from=packages,source=/repository,target=/var/pluto-packages,ro`
to the package phase's RUN (`Containerfile:101-106`) alongside the existing
`from=ctx` mount. This is **better than Kestrel's `COPY --from`**: the repo never
lands in a layer, which removes its gap #1 in §5.2.

> `just build` is unaffected: the base `FROM` is still the first un-aliased one,
> and `ghcr.io/siddhj2206/pluto-packages` is aliased.

### 8.3 `build/local-packages-helpers.sh` (new)

Model it on `build/copr-helpers.sh` so `20` reads the same way:

```bash
#!/usr/bin/bash
set -euo pipefail

# Install pluto's self-built RPMs from the factory repo image that the
# Containerfile bind-mounts at /var/pluto-packages (never copied into a layer).
# Writes a temp repo file, installs only from it, then removes it. gpgcheck=0
# because pluto does not sign its RPMs; the OCI image is cosign-signed and the
# digest is the trust anchor.
local_packages_install() {
	local repo_dir="${1:?repo dir}"; shift
	local packages=("$@")
	[[ ${#packages[@]} -gt 0 ]] || { echo "ERROR: no packages given" >&2; return 1; }

	local repo_file=/etc/yum.repos.d/pluto-packages.repo
	printf '[pluto-packages]\nname=pluto packages\nbaseurl=file://%s\nenabled=1\ngpgcheck=0\npriority=5\n' \
		"$repo_dir" > "$repo_file"
	dnf5 -y install --enablerepo=pluto-packages "${packages[@]}"
	rm -f "$repo_file"        # nothing points at /var/pluto-packages at runtime

	for pkg in "${packages[@]}"; do
		rpm -q "$pkg" >/dev/null || { echo "ERROR: $pkg did not install" >&2; return 1; }
	done
}
```

### 8.4 `build/20-packages-and-services.sh` change

Replace the third-party DMS/ghostty COPR lines (when the first slice lands) with:

```bash
source /ctx/build/local-packages-helpers.sh
local_packages_install /var/pluto-packages dms dms-cli
```

Keep `copr_install_isolated "ublue-os/packages" uupd` until `uupd` is owned too
(it is a clean second target: `ublue-os/uupd` is a single Go binary).

The Hummingbird `copr_chroot` fix from the first report is **independent** of
this work and still needed for anything left on COPR.

### 8.5 `build/90-cleanup.sh` — hygiene still holds

Add `pluto-packages` to the named-repo disable loop (`90-cleanup.sh:34`) and add
a fail-loud assertion that it is not enabled:

```bash
disable_repo_file "${REPOS_DIR}/pluto-packages.repo"
...
if grep -qE '^enabled=1' "${REPOS_DIR}/pluto-packages.repo" 2>/dev/null; then
	echo "::error::pluto-packages repo still enabled" >&2
	exit 1
fi
```

Because `local_packages_install` already `rm`s the file, this is a backstop, not
the mechanism — exactly the relationship `copr_install_isolated` has to
`90-cleanup.sh` today. Also update the "no third-party repository still enabled"
loop (`90-cleanup.sh:39-45`) to include `pluto-packages.repo`, so a future
regression fails lint rather than shipping.

### 8.6 Factory CI (minimum viable, not a Kestrel port)

**Start with one workflow, not five.** Kestrel's `build-stage.yml` +
`rebuild-pigeon.yml` + `srpm.yml` + `seed-dnf-cache.yml` + `drift-check.yml`
composite is ~1,500 lines engineered for ~140 parallel packages. pluto's first
slice is two. The first workflow:

```yaml
name: build-packages
on:
  workflow_dispatch:
    inputs:
      package:
        type: string
        required: true
permissions:
  contents: read
  packages: write
  id-token: write
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - run: sudo apt-get update -qq && sudo apt-get install -y -qq podman
      - run: podman pull quay.io/fedora/fedora:44
      - name: Stage + verify source
        uses: ./.github/actions/stage-sources
        with: { package: "${{ inputs.package }}" }
      - name: rpmbuild in Fedora 44 + Hummingbird overlay
        run: |
          mkdir -p work/out
          podman run --rm -v "$PWD:/repo:Z" -v "$PWD/work/src:/src:Z" \
            -v "$PWD/work/out:/out:Z" -e PACKAGE="${{ inputs.package }}" \
            quay.io/fedora/fedora:44 bash -exc '
              dnf install -qy dnf-plugins-core rpm-build
              printf "[hummingbird]\nname=Hummingbird\nbaseurl=https://koji-s3-cache.hummingbird-project.io/packages.redhat.com/api/pulp-content/public-hummingbird/x86_64/\nenabled=1\ngpgcheck=0\nexclude=ruby* rubygem*\n" > /etc/yum.repos.d/hummingbird.repo
              export RPMBUILD=/root/rpmbuild
              mkdir -p "$RPMBUILD"/{BUILD,RPMS,SOURCES,SPECS,SRPMS}
              SPEC=$(ls /repo/packages/packages/$PACKAGE/*.spec | head -1)
              cp /src/* "$RPMBUILD/SOURCES/"; cp "$SPEC" "$RPMBUILD/SPECS/"
              dnf builddep -y --skip-unavailable "$RPMBUILD/SPECS/$(basename "$SPEC")" || true
              rpmbuild -ba "$RPMBUILD/SPECS/$(basename "$SPEC")" --define "_topdir $RPMBUILD" --define "dist .hum1.pluto"
              cp "$RPMBUILD"/RPMS/*/*.rpm /out/
            '
      - name: Publish repo image
        run: |
          sudo apt-get install -y -qq createrepo-c || sudo apt-get install -y -qq createrepo
          mkdir -p repository && cp work/out/*.rpm repository/ && createrepo_c repository
          printf 'FROM scratch\nCOPY repository /repository\n' > Dockerfile.publish
          podman build -f Dockerfile.publish -t ghcr.io/siddhj2206/pluto-packages:latest .
          echo "${{ secrets.GITHUB_TOKEN }}" | podman login ghcr.io -u ${{ github.actor }} --password-stdin
          podman push ghcr.io/siddhj2206/pluto-packages:latest
```

(`createrepo_c` runs on the runner; alternatively run it inside the Fedora
container and copy `/repository` out. The bind-mount/`COPY --from` seam is
unchanged either way.)

Add a PR-time `validate-packages.yml` that runs the ported
`packages/tools/validate.py` + `audit_sources.py --spec-sources` +
`pytest packages/tests`, so a broken spec or a missing `Source0` fails the PR
before any image build.

Then add `renovate.json` custom managers exactly like Kestrel's, but for pluto:

```json
{
  "customType": "regex",
  "description": "Containerfile pluto-packages digest pin",
  "managerFilePatterns": ["/Containerfile$/"],
  "matchStrings": ["ARG PLUTO_PACKAGES_SHA=(?<currentDigest>sha256:[a-f0-9]+)"],
  "datasourceTemplate": "docker",
  "depNameTemplate": "ghcr.io/siddhj2206/pluto-packages"
}
```

(Use a separate `ARG PLUTO_PACKAGES_SHA` rather than Kestrel's
`${PIGEON_IMAGE}@${PIGEON_IMAGE_SHA}` concatenation, so the digest is a single
unambiguous token for Renovate and for `COPY --from`.)

### 8.7 First slice: the DMS stack

DMS (`dms`, `dms-cli`) is the right first slice: leaf, mostly noarch/Qt, no ABI
consumers, and currently third-party (`avengemedia/dms` COPR). It is also small
enough to prove the pipeline in one PR.

1. Create `packages/packages/dms/` and `packages/packages/dms-cli/` (or one
   recipe with two subpackages if the spec does both), seeding from
   `AvengeMedia/DankMaterialShell` upstream. **Check the license and the right to
   redistribute before importing.** Prefer the upstream tarball over the
   avengemedia spec if the spec is not clearly licensed.
2. Add both to `packages/config/upstream-sources.json` with `version`,
   `url_template`, `sha512`, `filename`, and a `renovate` block
   (`github-tags`, `AvengeMedia/DankMaterialShell`).
3. Copy the spec(s) in; `rpmbuild -bs` locally (`just srpm dms` if you port the
   recipe) to prove sources are complete.
4. Add `.github/workflows/build-packages.yml` (§8.6); run it; confirm
   `ghcr.io/siddhj2206/pluto-packages` exists and
   `skopeo list-tags docker://ghcr.io/siddhj2206/pluto-packages` shows `latest`.
5. Pin the digest into the new `ARG PLUTO_PACKAGES_SHA` in the Containerfile.
6. Add `local_packages_install /var/pluto-packages dms dms-cli` to
   `build/20-packages-and-services.sh`; remove `dms`/`dms-cli` from the
   `avengemedia/dms` COPR install.
7. `just lint && just check && just test-unit`; build; confirm `90-cleanup.sh`
   passes and `bootc container lint --fatal-warnings` is clean.

**Slice 2:** `danklinux` (`dms-greeter`, `quickshell`, `matugen`, `danksearch`,
`dgop`, `material-symbols-fonts`) — same shape, more recipes, and the first place
the Hummingbird-vs-Fedora ABI question bites (quickshell links Qt).

**Slice 3:** `niri` and `uupd`/`oversteer`. niri is already in Fedora; own it
only for currency. Both are Rust/Go, so expect `%generate_buildrequires`
retries (Kestrel's bounded loop in `build-stage.yml` is the reference).

**Defer:** the five-stage dependency-wave engine, shared dnf cache, drift-check,
publish gate, SLSA. Add them when the package count makes them pay for
themselves.

### 8.8 Humans with account access — required

The great appeal of Kestrel is how short this list is:

| Step | Who / what |
|---|---|
| Enable Actions and grant `packages: write` | Repo admin (you). No GitHub App. |
| First GHCR push creates the `pluto-packages` package | Repo admin verifies visibility = public |
| Nothing else | **No FAS, no Copr project, no Packit App, no `COPR_API_TOKEN`, no signing key** (cosign keyless uses OIDC) |

If pluto ends up needing a COPR for anything not yet owned, the first report's
FAS/Copr/App gates return — but this pattern deliberately removes them.

### 8.9 Uncertainties / flags

- **Kestrel's image half is unproven.** The factory image exists; the images do
  not. Do not treat `build-warbler.yml` as a tested reference.
- **`COPY --from=${A}@${B}` expansion is unverified** (Kestrel's SHAs are TODO).
  pluto should avoid the concatenation and use one combined
  `ARG PLUTO_PACKAGES_REF=ghcr.io/.../pluto-packages@sha256:...` per §8.6.
- **No GPG verification of RPMs.** The digest of the OCI image is the trust
  anchor. If pluto wants `gpgcheck=1`, it must sign RPMs and ship the key, which
  is more machinery than the first slice needs.
- **`/etc/pigeon` is not cleaned up in Kestrel.** pluto's bind-mount design
  avoids the problem; do not copy Kestrel's `COPY --from` + repo-file-removal.
- **`pigeon/tools/*.py` are Apache-2.0** (repo LICENSE). Porting them is
  license-compatible; keep the attribution in a header.
- **The Hummingbird Pulp endpoint is used with `gpgcheck=0`** and an
  undocumented `exclude=ruby*`. Verify the URL and the priority story at write
  time (the first report already flags repo/GPG handling on Hummingbird).
- **Stale docs in Kestrel** to ignore: README's `packit-srpm-pilot.yml` (does not
  exist), "56 packages" (actually 145 recipes / 374 allow-list entries),
  `docs/*.md` → `PLAN.md` (absent).

---

## 9. Sources

### Repository metadata and commands used

```bash
gh auth status                                   # account Siddhj2206, scopes gist, read:org, repo, workflow
gh api repos/HuntedRaven7/Kestrel --jq '{full_name,description,default_branch,created_at,pushed_at,license:.license.spdx_id}'
gh api 'repos/HuntedRaven7/Kestrel/git/trees/main?recursive=1' --paginate > /tmp/opencode/kestrel-tree.json
gh api 'repos/HuntedRaven7/Kestrel/commits?per_page=15'
gh api 'repos/HuntedRaven7/Kestrel/contributors'
gh api 'users/HuntedRaven7/repos?per_page=100&sort=pushed'
git clone --depth 1 https://github.com/HuntedRaven7/Kestrel /tmp/opencode/kestrel

# image/tag checks
skopeo inspect docker://ghcr.io/huntedraven7/pigeon:latest
skopeo list-tags docker://ghcr.io/huntedraven7/pigeon
skopeo list-tags docker://ghcr.io/huntedraven7/warbler        # 403 — not published
skopeo list-tags docker://ghcr.io/huntedraven7/woodpecker     # 403 — not published
```

Clone HEAD: `c05a3363b2d6fef41ba3aa3811d719f83de1d985`, 2026-09-22 13:55:58 -0400,
"fix: finished the half finished nvme-cli".

### Kestrel files cited

- `README.md`, `AGENTS.md`, `Justfile`, `renovate.json`, `config/flavors.json`, `LICENSE`
- `docs/SKILL.md`, `docs/architecture.md`, `docs/building.md`, `docs/targeting-hummingbird.md`
- `.agents/skills/{pigeon-packaging,pigeon-source-verify,warbler-image,woodpecker-server,ci-release,mango-quickshell,sddm-autologin,review}/SKILL.md`
- `.github/workflows/{validate,rebuild-pigeon,build-stage,srpm,build-warbler,build-woodpecker,build-iso,import-package,recalculate-gaps,seed-dnf-cache,drift-check}.yml`
- `.github/actions/stage-sources/action.yml`
- `pigeon/.packit.yaml`, `pigeon/.packit.yaml` header + first package entry
- `pigeon/config/factory-contract.json`, `pigeon/config/upstream-sources.json`
- `pigeon/packages/` (145 recipe dirs)
- `pigeon/tools/{source_pipeline,audit_sources,render_packit_config,packit_source0,packit_workflow,sync_versions,fetch_vendored,validate,publish_gate,package_cache_key,package_inventory,drift_check,spec_source_alias,factory_contract}.py`
- `pigeon/tests/` (15 test modules)
- `warbler/Containerfile` (base ARG :6, PIGEON_IMAGE :10-11, `COPY --from` :25)
- `warbler/Containerfile.kernel`, `warbler/packages/warbler.toml`,
  `warbler/packages/{bluefin.toml,hummingbird.repo,fedora-44.repo}`
- `warbler/scripts/{install-packages.py,verify-rpm-contract.py,clean-stage.sh,configure-services.sh,configure-branding.sh,install-nvidia.sh,install-ogc-kernel.sh}`
- `warbler/system_files/shared/...`
- `woodpecker/Containerfile`, `woodpecker/packages/woodpecker.toml`,
  `woodpecker/scripts/...`, `woodpecker/contracts/server.toml`

### pluto files cited

- `Containerfile` (base `FROM` :53, context stages :38-49, package RUN :101-106)
- `build/20-packages-and-services.sh`, `build/copr-helpers.sh`, `build/90-cleanup.sh`
- `Justfile` (`build` target :142-157)
- `.github/workflows/` (12 files), `.github/renovate.json`
- `docs/research/{utah-hummingbird-niri-dms,packit-monorepo-packages}.md`
- `AGENTS.md`

### External references

- `HuntedRaven7/Kestrel`: https://github.com/HuntedRaven7/Kestrel
- Related owner repos consulted for lineage: `HuntedRaven7/Perch`,
  `HuntedRaven7/Velociraptor`, `HuntedRaven7/Archaeopteryx`,
  `HuntedRaven7/utah-packages-1`, `HuntedRaven7/FeatherPilot`
- `projectbluefin/utah-packages`: https://github.com/projectbluefin/utah-packages
- `ublue-os/packages`: https://github.com/ublue-os/packages
