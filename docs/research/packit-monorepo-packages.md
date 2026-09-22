# Packit-based monorepo package builds for pluto

Research report, 2026-09-22. Scope: how to build pluto-owned RPMs with
[Packit](https://packit.dev) in a monorepo and consume them in the bootc image
through Copr, following the patterns the `projectbluefin/*` and `ublue-os/*`
folks actually use.

Everything below is grounded in real files in real repositories; every config
example cites repo + path. Where the upstream projects do something different
from what a "Packit monorepo" tutorial implies, that is called out, because it
changes the recommendation.

---

## 0. TL;DR

- **Packit** is a Red Hat tool that turns a git repo into RPMs. It runs either
  as a **hosted GitHub App** ("Packit-as-a-Service") or as a **CLI**
  (`packit`) that you drive yourself from GitHub Actions. The CLI lives in the
  digest-pinned `quay.io/packit/packit` image.
- **projectbluefin does not use Packit-as-a-Service, and does not use Copr for
  its self-built packages.** The one real Packit monorepo in the org,
  [`projectbluefin/utah-packages`](https://github.com/projectbluefin/utah-packages),
  explicitly describes itself as "GitHub Actions' replacement for Copr" and
  drives the **Packit CLI** inside Actions. It has **no `jobs:` section**, so
  the Packit service does no work on its PRs.
- **ublue-os does use Copr** — but not Packit. [`ublue-os/packages`](https://github.com/ublue-os/packages)
  builds specs with a custom `ublue-builder` container running `mock` in GitHub
  Actions, then humans **manually add** the resulting package to a
  `ublue-os/*` Copr project ([README](https://github.com/ublue-os/packages/blob/main/README.md)).
  Bluefin's image consumes those Coprs with `copr_install_isolated`
  ([`bluefin/build_files/shared/copr-helpers.sh`](https://github.com/projectbluefin/bluefin/blob/main/build_files/shared/copr-helpers.sh)).
- pluto's finpilot template already consumes Copr this way: `build/20-packages-and-services.sh`
  installs `uupd` from `ublue-os/packages` via `copr_install_isolated`, and
  `build/90-cleanup.sh` disables every COPR and **fails the build if one is
  still enabled**.
- **Recommendation:** use **Packit-as-a-Service with `copr_build` jobs** into a
  pluto-owned Copr project (`Siddhj2206/pluto-packages`). It is the lowest-code
  path to "package change → RPM in Copr → image consumes it" and needs no
  GitHub Actions workflow of its own. Keep the CLI-in-Actions factory
  (utah-packages style) as a later option if you ever need Hummingbird-native
  disttags. First slice: own the **DMS** stack (`dms`, `dms-cli`) — today from
  the third-party `avengemedia/dms` Copr — then `danklinux`, then `niri`.
- **Human gate:** a Fedora Account System (FAS) account is required to onboard
  Packit Service, and someone must create the Copr project and grant Packit
  permissions. That is the only step an agent cannot complete.

---

## 1. Packit fundamentals

### 1.1 What it is

Packit has two faces:

| | Packit-as-a-Service | Packit CLI |
|---|---|---|
| Runs as | Hosted GitHub App (`packit-as-a-service`) | `packit` binary you invoke |
| Reacts to | PRs, pushes, releases via webhooks | whatever your CI calls |
| Build backend | Fedora Copr (for `copr_build`), Koji, Testing Farm | Copr, Koji, or local Mock |
| Config file | same `.packit.yaml` | same `.packit.yaml` |
| Onboarding | install GitHub App + FAS identity verification | your own tokens |

Both read the same `.packit.yaml` in the upstream repo root. Accepted names:
`.packit.yaml`, `.packit.yml`, `packit.yaml`, `packit.yml`
([Configuration](https://packit.dev/docs/configuration)).

### 1.2 `packit.yaml` anatomy

Top-level keys (all optional unless noted) — full list in
[packit.dev/docs/configuration](https://packit.dev/docs/configuration):

- **`packages`** (dict) — the monorepo key. Maps `{name: package-config}`.
- **`jobs`** (list of dicts) — what Packit should do and when.
- **`specfile_path`**, **`upstream_package_name`**, **`downstream_package_name`**,
  **`paths`**, **`files_to_sync`**, **`actions`**, **`sources`**,
  **`srpm_build_deps`** — package-specific keys (see §3/§6).
- **`upstream_project_url`**, **`merge_pr_in_ci`**, **`update_release`**,
  **`release_suffix`**, **`version_suffix`**, **`packit_instances`**,
  **`notifications`**, **`issue_repository`**, **`allowed_gpg_keys`**.

A minimal single-package config:

```yaml
specfile_path: package.spec
upstream_package_name: package
downstream_package_name: package
files_to_sync:
  - package.spec
  - .packit.yaml
jobs:
  - job: copr_build
    trigger: pull_request
    targets: [fedora-stable]
```

### 1.3 The main jobs

Source: [Packit Service jobs configuration](https://packit.dev/docs/configuration/jobs)
and the per-job pages.

**Upstream jobs** (defined in your upstream repo):

| job | trigger(s) | what it does |
|---|---|---|
| `copr_build` | `pull_request`, `commit`, `release` | Builds an SRPM and submits it to Fedora Copr. This is the one pluto needs. |
| `tests` | `pull_request`, `commit`, `release` | Runs TMT/FMF tests in Testing Farm. |
| `upstream_koji_build` | `pull_request`, `commit`, `release` | Scratch Koji build of the upstream state. |
| `vm_image_build` | `pull_request` | Builds a VM image via Copr + Testing Farm image builder (`image_distribution`, `image_type`, `image_architecture`, `image_account_id`, `packages_to_install`). |
| `propose_downstream` | `release` | Pushes an update to Fedora dist-git (lookaside upload + PR). |

**Downstream jobs** (defined in dist-git, run by Packit Service): `pull_from_upstream`,
`koji_build`, `bodhi_update`.

**`sync_from_downstream` is not a service job.** It is a CLI command,
`packit sync-from-downstream` ([CLI docs](https://packit.dev/docs/cli)),
used to pull dist-git changes back into the upstream repo. Do not look for a
`sync_from_downstream:` job key; it does not exist in the current schema.

Aliases you can use for `targets` / dist-git branches: `fedora-all`,
`fedora-stable`, `fedora-development`, `fedora-latest`, `fedora-latest-stable`,
`fedora-branched`, `epel-all`. Architecture can be suffixed (`fedora-stable-aarch64`);
default is `x86_64`.

### 1.4 Which model projectbluefin/ublue actually use

- **`projectbluefin/utah-packages`** — Packit **CLI only**, run inside GitHub
  Actions in a pinned `quay.io/packit/packit` container. It has a root
  `.packit.yaml` with a `packages:` map for all 352 recipes and **no `jobs:`**.
  `packit srpm` is used as a spec-validity gate; binaries are built by a
  hand-rolled `rpmbuild` lane, and the repo's own docs say Copr/Packit Service
  are "not dependencies"
  ([`docs/architecture.md`](https://github.com/projectbluefin/utah-packages/blob/main/docs/architecture.md),
  [`docs/contributing.md`](https://github.com/projectbluefin/utah-packages/blob/main/docs/contributing.md),
  [`docs/superpowers/specs/2026-09-05-full-packit-rpm-factory-design.md`](https://github.com/projectbluefin/utah-packages/blob/main/docs/superpowers/specs/2026-09-05-full-packit-rpm-factory-design.md)).
- **`ublue-os/packages`** — **no Packit**. A custom `ublue-builder` container
  (`mock-wrapper`, `mock`) is invoked by `just build` from
  [`.github/workflows/build-package.yml`](https://github.com/ublue-os/packages/blob/main/.github/workflows/build-package.yml),
  and humans then add packages to Copr by hand.
- **`projectbluefin/bluefin`** — consumes `ublue-os/packages` Copr; it does not
  build RPMs.
- **`projectbluefin/common`** — no Packit, no Copr; it ships files/specs only.

So "follow how the projectbluefin folks do it" is ambiguous, and the honest
answer is: **for a Packit monorepo the only in-org precedent is
utah-packages' CLI pattern; for Copr consumption the precedent is
ublue-os/packages + bluefin's `copr_install_isolated`.** pluto's finpilot
template already has the second half. This report recommends wiring the two
together with Packit-as-a-Service `copr_build`, which is neither project's exact
choice but is the standard, least-moving-parts Packit path and matches what the
Packit project itself does.

---

## 2. Real templates and examples

### 2.1 utah-packages root monorepo config (the in-org precedent)

`projectbluefin/utah-packages` → `.packit.yaml` (58 KB, generated by
`tools/render_packit_config.py`). Head of file:

```yaml
# Root-level Packit monorepo config.
# Generated by tools/render_packit_config.py -- do not edit manually.
actions:
  create-archive:
    - bash -c 'python3 "$(git rev-parse --show-toplevel)/tools/packit_source0.py"'
packages:
  ModemManager:
    specfile_path: ModemManager.spec
    upstream_package_name: ModemManager
    downstream_package_name: ModemManager
    paths:
      - packages/ModemManager
  # ... 352 entries, each identical in shape ...
  zvbi:
    specfile_path: zvbi.spec
    upstream_package_name: zvbi
    downstream_package_name: zvbi
    paths:
      - packages/zvbi
```

What it does and what it deliberately omits:

- One `packages:` entry per recipe directory under `packages/<name>/`.
- A single top-level `create-archive` action that returns the already-verified
  `Source0` (via `tools/packit_source0.py`) instead of letting Packit build a
  git archive.
- **No `jobs:` section**, so Packit Service does nothing; `packit srpm` is run
  manually in CI. The file's purpose is to satisfy `packit`'s monorepo schema,
  not to trigger service jobs.
- The per-package `paths:` entry is load-bearing: the container's git-root
  resolution requires the package's `paths` entry to match, and the config must
  live at the repo root (documented in
  [`docs/superpowers/specs/2026-09-05-packit-srpm-pilot-design.md`](https://github.com/projectbluefin/utah-packages/blob/main/docs/superpowers/specs/2026-09-05-packit-srpm-pilot-design.md)).

### 2.2 utah-packages per-package leftover config

`packages/libgexiv2/.packit.yaml` — an unmodified Fedora dist-git service config,
**not** read by the factory. It shows the downstream-style jobs (useful to know
what not to copy into a build config):

```yaml
jobs:
  - job: pull_from_upstream
    trigger: release
    dist_git_branches:
      - fedora-stable
    version_update_mask: '^\d+\.\d+\.'
  - job: koji_build
    trigger: commit
    dist_git_branches:
      - fedora-stable
  - job: bodhi_update
    trigger: commit
    dist_git_branches:
      - fedora-stable
```

### 2.3 `fedora-copr/copr` — the canonical Packit monorepo with `copr_build`

This is the best real multi-package example: 11 packages in one repo, each with
its own `paths`, `specfile_path`, `files_to_sync`, `upstream_tag_template`, and
shared `actions` via a YAML anchor. Abridged (full file at
`.packit.yaml` on `main`):

```yaml
---
upstream_project_url: https://github.com/fedora-copr/copr.git

actions: &common_actions
  create-archive:
    - bash -c "tito build --tgz --test -o ."
    - bash -c "ls -1t ./*.tar.gz | head -n 1"
  get-current-version:
    - bash -c "grep -Po 'Version. +\K.*' *.spec"

packages:
  python-copr:
    downstream_package_name: python-copr
    upstream_package_name: copr
    paths: [./python]
    specfile_path: python-copr.spec
    files_to_sync: [python-copr.spec]
    upstream_tag_template: python-copr-{version}
    upstream_tag_include: "^python-copr-.*"
    actions:
      <<: *common_actions
  copr-cli:
    downstream_package_name: copr-cli
    upstream_package_name: copr-cli
    paths: [./cli]
    specfile_path: copr-cli.spec
    files_to_sync: [copr-cli.spec]
    upstream_tag_template: copr-cli-{version}
    upstream_tag_include: "^copr-cli-.*"
    actions:
      <<: *common_actions
  # ... 9 more packages ...

srpm_build_deps:
  - wait-for-copr
  - tito
  - git

merge_pr_in_ci: False

jobs:
  - job: copr_build
    packages: [copr-backend, copr-keygen, copr-messaging, copr-dist-git, copr-frontend]
    trigger: pull_request
    targets: [fedora-all-x86_64]
    manual_trigger: true
  - job: copr_build
    packages: [copr-selinux, python-copr, python-copr-common, copr-cli]
    trigger: pull_request
    targets: [fedora-all-x86_64, fedora-all-aarch64, fedora-all-ppc64le, epel-all-x86_64]
    manual_trigger: true
  - job: copr_build
    packages: [copr-rpmbuild]
    trigger: pull_request
    targets: [fedora-all-x86_64, fedora-all-aarch64, fedora-all-ppc64le, epel-9-x86_64, epel-8-x86_64]
    manual_trigger: true
  - job: tests
    trigger: pull_request
    identifier: gating
    targets: [fedora-44-x86_64]
    packages: [copr-frontend]
    skip_build: true
    manual_trigger: true
    tmt_plan: /testing-farm/plans/sanity
    status_name_template: "packit/testing-farm/gating/{package}"
```

Key takeaways:
- **One job can target several packages** with the `packages:` key inside the
  job; a job with no `packages:` key applies to all packages.
- **`identifier` is required when you have multiple `copr_build` jobs**, or
  check-run reporting collides (docs call this out).
- **`manual_trigger: true`** gates expensive builds behind a `/packit build`
  comment — useful while a monorepo is young.
- Copr's own repo shows the intended `trigger: commit` → named `project:` →
  `preserve_project: true` pattern (commented out in their file because monorepo
  support was immature at the time).

### 2.4 `packit/packit` — single-package reference

`.packit.yaml` on `main` shows the full modern shape: `packit_instances`,
`packages:` with one entry, `actions`, `srpm_build_deps`, and `copr_build` jobs
with explicit `owner`/`project`/`preserve_project`/`list_on_homepage` for
`commit` and `release` triggers. The commented `vm_image_build` block shows that
job's keys.

### 2.5 `ublue-os/packages` — the Copr-source repo shape

`packages/<name>/<name>.spec` + `staging/<name>/<name>.spec`, no Packit. Builds
via `ublue-builder` (`ublue-builder/Containerfile` = Fedora + `mock rpmdevtools
rpkg copr-cli rpmlint`; `ublue-builder/mock-wrapper` runs `rpkg spec`, `rpmlint`,
`spectool`, `rpkg srpm`, then `mock`). `just build <spec>` runs it locally.
This is the model for a *spec repo that feeds Copr*, and pluto can adopt the
same `packages/<name>/` directory shape even while using Packit to drive Copr.

---

## 3. Monorepo support

Packit's monorepo support is the `packages:` top-level dict
([Configuration § packages](https://packit.dev/docs/configuration#packages)).
Rules that matter:

- Each value is a package config using the package-specific keys; `paths` may
  appear **only** inside a `packages:` value.
- `downstream_package_name` defaults to the **key** in the `packages` dict;
  `specfile_path` defaults to `<downstream_package_name>.spec`.
- `upstream_project_url` may only be set at top level, not per package.
- `paths` scopes the package to a subdirectory — this is what makes one repo
  hold several packages.
- Jobs select packages with a `packages:` list inside the job; no list = all
  packages. `status_name_template` can include `{package}` to disambiguate check
  names in a monorepo.

Package-specific keys to know:

| key | meaning |
|---|---|
| `specfile_path` | path to the `.spec` within the repo |
| `upstream_package_name` | name of the upstream project (archive/dir naming) |
| `downstream_package_name` | RPM package name in Fedora/Copr |
| `paths` | subdirectory(ies) the package lives in |
| `files_to_sync` | files copied to dist-git on an update (spec, config, patches) |
| `actions` | override Packit steps (`create-archive`, `get-current-version`, `post-upstream-clone`, `pre-sync`, `fix-spec-file`, …) |
| `srpm_build_deps` | RPMs installed in the Copr build env before actions run |
| `sources` | override `SourceX` URLs (`path:` + `url:`) |
| `patch_generation_ignore_paths` | paths excluded from generated patches in a source-git repo |
| `spec_source_id` | which `SourceN` Packit rewrites (default `Source0`/`Source`) |
| `upstream_tag_template` / `upstream_tag_include` | tag → version mapping |

Concrete multi-package examples in the wild: `fedora-copr/copr/.packit.yaml`
(§2.3) and `packit/packit/.packit.yaml` (§2.4). There is **no** in-org
multi-package `copr_build` example; utah-packages is the only in-org monorepo
and it is CLI-only.

---

## 4. Copr integration

### 4.1 How Packit builds land in Copr

`copr_build` submits an SRPM to Fedora Copr. Relevant job keys
([copr_build docs](https://packit.dev/docs/configuration/upstream/copr_build)):

- `targets` — chroots; omit when using a custom project (targets come from the
  Copr project settings).
- `owner` — Copr namespace; defaults to `packit`. Prefix `@` for a group.
- `project` — Copr project name; defaults to `"{github_namespace}-{repository_name}-{pr_id}"`.
- `preserve_project` — keep the project past 60 days.
- `list_on_homepage`, `follow_fedora_branching`, `enable_net`,
  `module_hotfixes`, `additional_repos`, `identifier`, `manual_trigger`,
  `bootstrap`.

With **no `owner`/`project`**, Packit builds into its own namespace:
PR builds go to `packit/<repo>-<pr_id>`; commit builds to
`packit/<repo>-<branch>` (or a named `project:`). To land in a pluto-owned
project, set `owner: Siddhj2206` (or a group) and `project: pluto-packages`.

### 4.2 Copr project naming and chroots

For a pluto-owned project the natural names are:

- Personal namespace: `Siddhj2206/pluto-packages` (URL
  `https://copr.fedorainfracloud.org/coprs/Siddhj2206/pluto-packages/`).
- Or a group namespace once one exists: `@projectbluefin/pluto` — note there is
  **no** `projectbluefin/packages` Copr today (verified: API returns not found).

Chroots for a Fedora/Hummingbird bootc image:

- Fedora leaf packages: `fedora-43-x86_64`, `fedora-44-x86_64`,
  `fedora-45-x86_64`, `fedora-rawhide-x86_64`, or the `fedora-all` /
  `fedora-stable` aliases.
- The base image is `quay.io/hummingbird-community/bootc-os` and pluto is
  Fedora-44-flavoured. Pin `fedora-44-x86_64` for reproducibility; add
  `fedora-45-x86_64` when you move.
- **Hummingbird is not a Copr chroot.** Copr builds against Fedora, producing
  Fedora disttags (e.g. `fc44`), not the `hum1.bfin`-style disttags Hummingbird
  uses. For leaf packages (DMS is noarch scripts + Qt; niri is a compositor)
  that is usually fine. For ABI-sensitive shared libraries it is the exact
  problem utah-packages documented and refused to solve with Copr
  (`docs/architecture.md`: "a Fedora-chroot SRPM does not answer it"). Flag this
  before packaging anything that other packages link against.

### 4.3 Creating and binding the Copr project

1. Create it (needs a Fedora account / `copr-cli` login):
   ```bash
   copr-cli create pluto-packages \
     --chroot fedora-44-x86_64 \
     --chroot fedora-45-x86_64 \
     --description "pluto-owned packages"
   ```
   Or via the web UI at `https://copr.fedorainfracloud.org/coprs/Siddhj2206/`.
2. Grant Packit build permission (Packit Service asks for `builder` on first
   use; approve in project settings, or pre-grant):
   ```bash
   copr-cli edit-permissions --builder packit pluto-packages
   # add --admin packit only if you want Packit to change project settings
   ```
3. Add the forge project to the project's **"Packit allowed forge projects"**
   field in Copr settings:
   ```
   github.com/Siddhj2206/pluto
   ```
   Without this, `copr_build` refuses to run even with permissions
   ([copr_build docs § Allow builds from forges](https://packit.dev/docs/configuration/upstream/copr_build#allow-builds-from-forges)).

### 4.4 Required secrets / tokens

| Model | What you need |
|---|---|
| **Packit Service** (recommended) | **No repository secret.** Packit authenticates to Copr itself. You need: the GitHub App installed on the repo, a FAS account with `GitHub Username` set to `Siddhj2206` (self-approval), and the Copr project permissions + forge allowlist above. |
| **Packit CLI in Actions** (utah-packages style) | A Copr API token file (`~/.config/copr`) or `COPR_API_TOKEN` secret, plus a GitHub token if Packit touches the forge. Packit's own container image is `quay.io/packit/packit` (pin by digest). |

The `COPR_API_TOKEN` secret name is a convention, not a Packit requirement —
Packit Service needs none. `copr-cli` reads `~/.config/copr` (keys `username`,
`login`, `token`, `copr_url`).

### 4.5 Enabling the Copr repo inside the Containerfile

pluto already has the safe pattern: `build/copr-helpers.sh` →
`copr_install_isolated "owner/project" pkg...`, which enables, **immediately
disables**, then installs only from the named repo id. The repo id it derives is:

```
copr:copr.fedorainfracloud.org:Siddhj2206:pluto-packages
```

Usage in `build/20-packages-and-services.sh`:

```bash
source /ctx/build/copr-helpers.sh
copr_install_isolated "Siddhj2206/pluto-packages" dms dms-cli
```

Alternatives, in descending order of safety:

- `dnf5 -y copr enable Siddhj2206/pluto-packages` — leaves it enabled; only
  acceptable if `90-cleanup.sh` disables it (it does) and the install is in the
  same layer.
- A hand-written `/etc/yum.repos.d/pluto-packages.repo` with `enabled=0` and
  `--enablerepo=` at install time. Avoids `dnf5 copr` entirely.

**Hummingbird gotcha (important).** The old pluto tree had to force the Copr
chroot because the Hummingbird base's `/etc/os-release` reports
`ID=hummingbird` / `VERSION_ID=20251124`, which `dnf5 copr`'s auto-detection
turns into a bogus `hummingbird-20251124-x86_64` chroot:

```bash
# .delta/worktrees/a5fpy4aejxqn/pluto/build/scripts/package-lib.sh
copr_chroot() {
	printf 'fedora-%s-%s' "$(cat /etc/dnf/vars/releasever)" "$(uname -m)"
}
dnf5 -y copr enable "${copr_id}" "$(copr_chroot)"
```

The finpilot `build/copr-helpers.sh` does **not** pass a chroot argument. If
pluto ships on the Hummingbird base and enables a Copr there, `copr enable` will
likely fail or pick the wrong chroot. Add the chroot argument (derived from
`/etc/dnf/vars/releasever`) before relying on `copr_install_isolated` on
Hummingbird. This is a concrete gap to fix, not a hypothetical.

---

## 5. CI wiring

### 5.1 Packit Service: no workflow required

Installing the GitHub App is the whole wiring. Packit then reacts to PRs,
pushes, and releases per your `jobs:`. No `.github/workflows/` file is involved
and no repo secret is needed.

### 5.2 Packit CLI in GitHub Actions (utah-packages pattern)

If you later want the CLI factory, model it on
`projectbluefin/utah-packages/.github/workflows/packit-srpm-pilot.yml` +
`packit-srpm-chunk.yml`:

- `workflow_dispatch` (manual) and/or `pull_request`.
- Matrix one package per job, `fail-fast: false`; **chunk at ≤256 jobs** because
  GitHub silently expands a larger matrix to zero jobs.
- Run in `quay.io/packit/packit` pinned by digest:
  ```yaml
  docker run --rm -e PACKAGE -v "$PWD:/repo:Z" -v "$PWD/work/srpm:/out:Z" -w /repo \
    quay.io/packit/packit:latest@sha256:<digest> bash -exc '
      git config --global --add safe.directory "*"
      packit srpm --preserve-spec --output "/out/$PACKAGE.src.rpm" -p "$PACKAGE"
      rpm -qp --qf "%{NAME}-%{VERSION}-%{RELEASE}\n" "/out/$PACKAGE.src.rpm"'
  ```
- `actions/checkout` with `fetch-depth: 0` and `fetch-tags: true` — `packit srpm`
  needs real history/tags.
- Assert the SRPM is non-empty (`test -s`) and `rpm -qp`-parseable; upload with
  `if-no-files-found: error`.
- The CLI factory in utah-packages deliberately does **not** use the mutable
  `packit/actions/*@main` GitHub Actions, and the `quay.io/packit/packit`
  digest churns (two pins died upstream). Renovate does not track an image named
  inside a `run:` script.

### 5.3 Keeping it consistent with finpilot's workflows

pluto's existing workflows (`pr-validation.yml`, `build-image.yml`,
`unit-tests.yml`, `validate-*.yml`, `renovate.yml`) should be left alone. Add:

- `.packit.yaml` at the repo root (Packit Service reads it; no workflow needed).
- Optionally a `validate-packit` job appended to `pr-validation.yml`, or a new
  `validate-packit.yml`, running `packit config validate` in the pinned image so
  a malformed config fails the PR before the service ever sees it:
  ```yaml
  - name: Validate Packit config
    run: |
      docker run --rm -v "$PWD:/repo:Z" -w /repo \
        quay.io/packit/packit@sha256:<digest> \
        packit config validate
  ```
- Add a `packages/**/*.spec` path so spec changes trigger validation. Do **not**
  wire Packit into `build-image.yml`; the image build consumes the Copr repo,
  it does not build RPMs.

There is also a Packit pre-commit hook for config validation
([pre-commit hooks](https://packit.dev/posts/pre-commit-hooks#validate-config)),
which fits pluto's existing `.pre-commit-config.yaml`.

---

## 6. Spec files and sources

- **`specfile_path`** — relative path within the repo, e.g.
  `packages/dms/dms.spec`. If omitted it defaults to
  `<downstream_package_name>.spec` searched under `paths`.
- **`sources`** — override `SourceX` URLs without editing the spec:
  ```yaml
  sources:
    - path: DankMaterialShell-<version>.tar.gz
      url: https://github.com/AvengeMedia/DankMaterialShell/archive/refs/tags/v<version>.tar.gz
  ```
  This is the escape hatch when the spec's `Source0` points at a Fedora lookaside
  URL Packit cannot fetch.
- **`files_to_sync`** — files copied to dist-git on an update (spec, `.packit.yaml`,
  patches). For a self-owned package that never goes to Fedora dist-git, this is
  mostly inert, but keep the spec listed so `propose_downstream` works if you
  ever add it.
- **`patch_generation_ignore_paths`** — only relevant to source-git repos;
  excludes paths (spec, config) from generated patches.
- **`create-archive` action** — if your upstream is the repo itself (a pluto
  asset package), define an action that produces the tarball and prints its
  filename, as utah-packages and fedora-copr do. If the upstream is external
  (DMS, niri), let Packit fetch the release tarball or use `sources:`.
- **`srpm_build_deps`** — RPMs installed in the Copr build env for your actions
  (e.g. `tito`, `git`, `hatch`). Needed only when actions require tooling.
- **Source tarballs**: for a self-owned package with external upstream, the spec
  declares a real upstream `Source0` URL and Packit fetches it. There is no
  lookaside cache in the Copr model. If a spec has `SourceN`/`PatchN` files that
  are not downloadable URLs, stage them in the repo and reference them via
  `sources:` (this is the failure mode that excluded 5 packages from
  utah-packages' SRPM pilot).
- **Conventions the projectbluefin/ublue folks use** (`ublue-os/packages/README.md`):
  follow Fedora Packaging Guidelines; `rpmlint` must pass; buildable offline once
  sources are fetched; Renovate-updatable. Bump `Version:` for upstream bumps and
  `Release:` for packaging-only changes; use `rpmdev-bumpspec`; reset `Release` to
  `1` on a version bump; support `%autorelease` / `%global baserelease`.

---

## 7. How it connects to the image build

Sequence for a pluto-owned package:

```
edit packages/<name>/<name>.spec  (push/PR to Siddhj2206/pluto)
        │
        ▼
Packit Service `copr_build` job
   → SRPM built, submitted to Copr Siddhj2206/pluto-packages
   → RPMs appear in https://copr.fedorainfracloud.org/coprs/Siddhj2206/pluto-packages/
        │
        ▼
build/20-packages-and-services.sh
   copr_install_isolated "Siddhj2206/pluto-packages" <name>
   (enables → disables → installs only from that repo id)
        │
        ▼
build/90-cleanup.sh
   disables every _copr:*.repo / _copr_*.repo / rpmfusion-*.repo
   and FAILS the build if any is still enabled=1
        │
        ▼
bootc container lint --fatal-warnings
```

**The finpilot rule: the shipped image must have no enabled COPRs.** Two
mechanisms enforce it: `copr_install_isolated` disables the COPR immediately
after enabling it, and `90-cleanup.sh` is the backstop that `sed`s every COPR
repo file to `enabled=0` and then hard-fails if `grep -qE '^enabled=1'` still
matches. Any new pluto package path must go through `copr_install_isolated`
(or otherwise leave the repo disabled) or the image build fails. This is
deliberate and matches bluefin's `disable-repos.sh` / `validate-repos.sh`.

Also note the **cache-layer rule** (Containerfile comment): packages are
installed in `20-packages-and-services.sh`, never in `10-overlay.sh`, so a
filesystem-overlay change cannot invalidate the expensive package layer. Put
pluto-owned package installs in the package phase.

---

## 8. Recommended plan for pluto

### 8.1 Model choice

Use **Packit-as-a-Service `copr_build`** into `Siddhj2206/pluto-packages`.

Why: it is the smallest change that delivers "package change → RPM in Copr →
image consumes it". It needs no Actions workflow, no Copr token secret, and no
custom build container. The utah-packages CLI factory is more powerful but it is
a 1,445-line, hand-rolled Copr replacement that still has open design questions
(its own `#43`); do not copy that for a first slice.

Trade-off to accept: Packit builds Fedora disttags, not Hummingbird disttags.
That is fine for the DMS/Qt leaf stack. Do not use it to own ABI-sensitive
libraries until the Hummingbird build-root question is solved.

### 8.2 Monorepo layout

```text
pluto/
├── .packit.yaml                      # root monorepo config (Packit Service)
├── packages/
│   ├── dms/
│   │   ├── dms.spec
│   │   ├── sources                   # optional, for lookaside-style files
│   │   └── patches/                  # optional
│   └── danklinux/
│       ├── danklinux.spec
│       └── patches/
├── build/
│   ├── 20-packages-and-services.sh   # copr_install_isolated ...
│   └── copr-helpers.sh               # add Hummingbird chroot arg (see §4.5)
└── docs/research/packit-monorepo-packages.md
```

A single root `.packit.yaml` with a `packages:` map (like utah-packages and
fedora-copr) — not per-package `.packit.yaml` files. Per-package configs are the
dist-git/service shape and will error with `KeyError('downstream_package_name')`
when pointed at directly (documented in utah-packages' pilot spec).

### 8.3 Config skeleton (exact)

`.packit.yaml`:

```yaml
---
# pluto self-owned packages. Packit Service builds these into Copr
# Siddhj2206/pluto-packages; build/20-packages-and-services.sh installs them.
upstream_project_url: https://github.com/Siddhj2206/pluto

merge_pr_in_ci: false

packages:
  dms:
    downstream_package_name: dms
    upstream_package_name: DankMaterialShell
    paths:
      - packages/dms
    specfile_path: packages/dms/dms.spec
    files_to_sync:
      - packages/dms/dms.spec

  dms-cli:
    downstream_package_name: dms-cli
    upstream_package_name: DankMaterialShell
    paths:
      - packages/dms
    specfile_path: packages/dms/dms.spec
    files_to_sync:
      - packages/dms/dms.spec

jobs:
  # PR verification: build against the Fedora chroots pluto ships on.
  - job: copr_build
    trigger: pull_request
    identifier: pr-dms
    packages: [dms, dms-cli]
    owner: Siddhj2206
    project: pluto-packages
    targets:
      - fedora-44-x86_64

  # Merge to main: keep the consumer project fresh.
  - job: copr_build
    trigger: commit
    branch: main
    identifier: main-dms
    packages: [dms, dms-cli]
    owner: Siddhj2206
    project: pluto-packages
    targets:
      - fedora-44-x86_64
      - fedora-45-x86_64
    preserve_project: true
    list_on_homepage: true
```

Notes:
- `identifier` is mandatory because there is more than one `copr_build` job.
- `dms` and `dms-cli` may be two subpackages of one spec; if so, one `packages:`
  entry suffices and the Copr build produces both. Split them only if they are
  separate source packages.
- Add `manual_trigger: true` to the PR job if Copr queue cost matters while you
  iterate.
- `targets` may be omitted if you would rather let Copr project settings drive
  them; keeping them explicit is clearer.

### 8.4 Copr project setup (commands)

```bash
# once, as a human with a Fedora account
copr-cli create pluto-packages \
  --chroot fedora-44-x86_64 \
  --chroot fedora-45-x86_64 \
  --description "pluto-owned packages (DMS, Dank Linux, later niri)"

copr-cli edit-permissions --builder packit pluto-packages

# then, in the Copr web UI for the project:
#   Settings → "Packit allowed forge projects" → add:
#     github.com/Siddhj2206/pluto
```

Verify from the API:

```bash
curl -fsSL 'https://copr.fedorainfracloud.org/api_3/project?ownername=Siddhj2206&projectname=pluto-packages' | jq '.name, .chroot_repos'
```

### 8.5 Required GitHub secrets/settings

- **Packit Service:** no repo secret. Install the
  [Packit-as-a-Service GitHub App](https://github.com/marketplace/packit-as-a-service)
  on `Siddhj2206/pluto`; set `GitHub Username` in the FAS account profile to
  `Siddhj2206` for self-approval; resolve the allowlist issue if one is opened
  (`/packit verify-fas <fas-user>`).
- **Workflow permissions:** none change. `pr-validation.yml` already uses
  `permissions: {}` with `contents: read` on the job.
- **If you later add the CLI factory:** create a `COPR_API_TOKEN` secret
  (contents of `~/.config/copr`) and reference it as a build secret; that is the
  only new secret.

### 8.6 CI files to add

| file | purpose |
|---|---|
| `.packit.yaml` | the monorepo config above |
| `docs/research/packit-monorepo-packages.md` | this report |
| (optional) `validate-packit` job in `pr-validation.yml` | run `packit config validate` in the pinned image on PRs |
| (optional) `.pre-commit-config.yaml` entry | Packit's `validate-config` hook |

Do not touch `build-image.yml`, `execute-release.yml`, `promote-main-to-stable.yml`,
or the validate-* workflows beyond the optional job above.

### 8.7 First slice (prioritized)

**Slice 1 — prove the pipeline with the DMS stack.**
`dms` and `dms-cli` currently come from the third-party `avengemedia/dms` Copr
(verified live; it also carries `fedora-44-x86_64` and `fedora-45-x86_64`).
They are leaf, mostly noarch/Qt packages — the right size to prove
"Packit → Copr → image" without ABI risk.

1. Import the `dms` spec (from `avengemedia/dms` or upstream
   `AvengeMedia/DankMaterialShell`) into `packages/dms/`.
2. Add `.packit.yaml` (§8.3), install the GitHub App, create the Copr project
   and permissions (§8.4).
3. Verify a PR build lands in `Siddhj2206/pluto-packages`.
4. Change `build/20-packages-and-services.sh` to
   `copr_install_isolated "Siddhj2206/pluto-packages" dms dms-cli`, replacing the
   `avengemedia/dms` dependency.
5. Confirm `90-cleanup.sh` still passes (no enabled COPR) and the image builds.

**Slice 2 — `danklinux`** (`dms-greeter`, `quickshell-git`, `matugen`,
`danksearch`, `dgop`, `material-symbols-fonts`) from the `avengemedia/danklinux`
Copr. Same shape, more packages.

**Slice 3 — `niri`.** niri is already in Fedora (`niri`,
`xwayland-satellite`); own it only when you need a newer version than Fedora
ships. Note the old pluto manifest preferred the `yalter/niri-git` Copr for
currency. Building a compositor from Packit is heavier (Rust, `%cargo_prep`,
`%generate_buildrequires`) — the utah-packages pilot had to special-case Rust
`%generate_buildrequires` retries, so expect that.

**Defer:** the full CLI factory (utah-packages style), `propose_downstream` /
`koji_build` / `bodhi_update` (only needed if you push to Fedora dist-git),
`tests` (Testing Farm), `vm_image_build`, and any ABI-sensitive library.

### 8.8 Humans with account access — required

- Install the **Packit-as-a-Service GitHub App** on `Siddhj2206/pluto`
  (repo admin).
- Create/verify a **Fedora Account System (FAS)** account and set its
  `GitHub Username` to `Siddhj2206`; respond to the allowlist issue if Packit
  opens one.
- Create the **Copr project** `Siddhj2206/pluto-packages`, grant `packit`
  `builder`, and add `github.com/Siddhj2206/pluto` to the forge allowlist.
- If the CLI factory is chosen later, generate a **Copr API token** and store it
  as `COPR_API_TOKEN`.

Everything else (specs, `.packit.yaml`, build-script edits, validation job) an
agent can do.

### 8.9 Uncertainties / flags

- **Hummingbird vs Fedora chroot.** Packit/Copr produce Fedora disttags, not
  Hummingbird ones. Untested for pluto's Hummingbird base; verify the DMS RPMs
  install cleanly on `quay.io/hummingbird-community/bootc-os`.
- **`dnf5 copr` chroot detection on Hummingbird.** `build/copr-helpers.sh`
  lacks the chroot argument the old pluto tree needed. Add it before depending on
  `copr_install_isolated` on Hummingbird.
- **Packit monorepo support maturity.** fedora-copr commented out its `commit`
  builds citing incomplete monorepo support
  (packit/packit#1903). Validate PR and commit jobs behave as expected before
  relying on them.
- **No in-org `copr_build` precedent.** The recommendation composes two in-org
  patterns (utah-packages' Packit monorepo shape + bluefin's Copr consumption);
  it is not a copy of an existing projectbluefin Packit+Copr repo, because none
  exists.
- **Packit Service Copr namespace.** With explicit `owner`/`project`, builds go
  to your project; without them they go to `packit/<repo>-<pr>`. Do not confuse
  the two when debugging.
- **Spec provenance/licensing.** DMS is from `avengemedia`; confirm the license
  and the right to redistribute before importing the spec into pluto.

---

## Sources

### Primary configs read

- `projectbluefin/utah-packages` → `.packit.yaml` (root monorepo; 352 packages,
  no `jobs:`), `packages/libgexiv2/.packit.yaml`,
  `.github/workflows/packit-srpm-pilot.yml`,
  `.github/workflows/packit-srpm-chunk.yml`, `docs/architecture.md`,
  `docs/contributing.md`,
  `docs/superpowers/specs/2026-09-05-full-packit-rpm-factory-design.md`,
  `docs/superpowers/specs/2026-09-05-packit-srpm-pilot-design.md`.
- `fedora-copr/copr` → `.packit.yaml` (canonical monorepo with `copr_build`).
- `packit/packit` → `.packit.yaml` (single-package reference; `vm_image_build`
  keys).
- `ublue-os/packages` → `README.md`, `.github/workflows/build-package.yml`,
  `Justfile`, `ublue-builder/Containerfile`, `ublue-builder/mock-wrapper`,
  `packages/README.md`.
- `projectbluefin/bluefin` → `build_files/shared/copr-helpers.sh`,
  `build_files/shared/package-lib.sh`, `build_files/shared/disable-repos.sh`,
  `build_files/shared/validate-repos.sh`, `build_files/base/03-packages.sh`,
  `build_files/base/17-cleanup.sh`, `.github/workflows/copr-health-monitor.yml`.
- `projectbluefin/common` → `Containerfile` (no Copr/Packit).
- pluto (this repo) → `Containerfile`, `Justfile`, `README.md`,
  `build/10-overlay.sh`, `build/20-packages-and-services.sh`,
  `build/90-cleanup.sh`, `build/copr-helpers.sh`, `build/README.md`,
  `.github/workflows/pr-validation.yml`, `.github/workflows/build-image.yml`,
  `.agents/skills/ci/SKILL.md`, `.agents/skills/build/SKILL.md`.
- pluto prior tree (pre-reset worktree) →
  `.delta/worktrees/a5fpy4aejxqn/pluto/build/scripts/package-lib.sh`,
  `build/packages/niri.toml`, `build/packages/base.toml` (source of the exact
  niri/DMS Copr names and the Hummingbird `copr_chroot` workaround).

### Documentation

- Packit configuration: https://packit.dev/docs/configuration
- Packit jobs: https://packit.dev/docs/configuration/jobs
- `copr_build`: https://packit.dev/docs/configuration/upstream/copr_build
- Packit CLI: https://packit.dev/docs/cli
- Packit onboarding guide (GitHub App, FAS approval): https://packit.dev/docs/guide
- Packit pre-commit config validation: https://packit.dev/posts/pre-commit-hooks#validate-config
- Fedora packaging guidelines: https://docs.fedoraproject.org/en-US/packaging-guidelines/
- Copr: https://copr.fedorainfracloud.org/

### Copr API checks (2026-09-22)

```
GET https://copr.fedorainfracloud.org/api_3/project?ownername=avengemedia&projectname=dms
GET https://copr.fedorainfracloud.org/api_3/project?ownername=avengemedia&projectname=danklinux
GET https://copr.fedorainfracloud.org/api_3/project?ownername=scottames&projectname=ghostty
GET https://copr.fedorainfracloud.org/api_3/project?ownername=ublue-os&projectname=packages
GET https://copr.fedorainfracloud.org/api_3/project?ownername=projectbluefin&projectname=packages   # not found
GET https://copr.fedorainfracloud.org/api_3/project?ownername=packit&projectname=copr
```

### `gh` / shell commands used

```bash
gh auth status
gh api user -q .login

# repo existence / metadata
gh api repos/projectbluefin/utah-packages -q '.full_name + " " + .default_branch'
gh api repos/ublue-os/packages        -q '.full_name + " " + .default_branch'
gh api repos/projectbluefin/common    -q '.full_name + " " + .default_branch'
gh api repos/projectbluefin/bluefin   -q '.full_name + " " + .default_branch'
gh api repos/projectbluefin/finpilot  -q '.full_name + " " + .default_branch'

# find Packit configs in the orgs
gh api -X GET search/code -f q='org:projectbluefin filename:packit.yaml OR filename:.packit.yaml' --paginate -q '.items[] | .repository.full_name + " :: " + .path'
gh api -X GET search/code -f q='org:ublue-os filename:packit.yaml OR filename:.packit.yaml' --paginate -q '.items[] | .repository.full_name + " :: " + .path'
gh search code --owner projectbluefin "packit"

# find monorepos with copr_build
gh api -X GET search/code -f q='"copr_build" "downstream_package_name" filename:packit.yaml' --paginate -q '.items[] | .repository.full_name + " :: " + .path'
gh api -X GET search/code -f q='"packages:" "copr_build" "paths:" filename:.packit.yaml' --paginate -q '.items[] | .repository.full_name + " :: " + .path'

# local clones used for reading configs
git clone --depth 1 https://github.com/projectbluefin/utah-packages /tmp/opencode/research/utah-packages
git clone --depth 1 https://github.com/projectbluefin/common       /tmp/opencode/research/common
git clone --depth 1 https://github.com/projectbluefin/bluefin      /tmp/opencode/research/bluefin
git clone --depth 1 https://github.com/ublue-os/packages           /tmp/opencode/research/packages
git clone --depth 1 https://github.com/projectbluefin/finpilot     /tmp/opencode/research/finpilot
git clone --depth 1 https://github.com/hanthor/hummingbird-github  /tmp/opencode/research/hummingbird-github
```
