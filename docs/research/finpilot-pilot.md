# finpilot as the pilot workload — CI on pluto

Researched 2026-10-10 for wayfinder ticket #123 (part of #117, under the moonshot
#116). Primary source: the finpilot repository itself, read in full — `README.md`,
`AGENTS.md`, `Justfile`, `Containerfile`, `.github/`, `build/`, `custom/`, `iso/`,
`tests/`, `.agents/skills/`. Every claim about the workload cites a file path and
line range. Secondary sources — GitHub's own documentation, and the
`projectbluefin/actions` action metadata that finpilot's workflows call — are cited
by URL with an access date. Evidence is tagged:

- **[repo]** — the finpilot repo (or, where marked, the pluto repo). Path given.
- **[docs]** — documented behaviour on GitHub's own pages; access date 2026-10-10.
- **[api]** — GitHub's REST API, live and read-only (`gh api`).
- **[inference]** — this author's reading, not a source's statement.

**Companions, not repeats.** `docs/research/actions-subset.md` (#120) measured the
Actions grammar and drew the parity-tail boundary; `container-backend.md` (#119)
settled the substrate question; `shared-state-and-portable-resume.md` (#121) settled
cache ownership. This note takes one real repository and asks what it would take to
run *its* CI on pluto. It does not re-derive the subset; it applies it. Where a
statement below depends on a companion note, the note is cited rather than re-argued.

**Scope, per the moonshot.** Individuals and teams, trusted code, hardware they own,
a handful of machines. finpilot is a single-owner public repository with no external
contributors and no untrusted-fork CI — it is exactly the trusted-code profile #116
describes, and the constraint named in #123 (no GitHub Apps) is a property of how it
is actually operated, not a hypothetical.

---

## 1. The answer, up front

| # | Finding | Confidence |
|---|---|---|
| 1 | **finpilot's CI is 12 workflows, all on GitHub-hosted runners, zero self-hosted, zero service containers, zero matrix, zero artifacts.** The dependency that matters is not compute, it is *forge control plane*: 6 of the 12 workflows are thin callers of reusable workflows, and the release half (`execute-release`, `promote-main-to-stable`, `sync-stable-to-main`, `renovate`) is GitHub-shaped by construction. | [repo] verified |
| 2 | **The runner-mode path (GitHub orchestrates, pluto executes) is nearly free, and it is the path the ticket wants.** GitHub mints the job's `GITHUB_TOKEN`, delivers `RENOVATE_TOKEN`, enforces the concurrency groups and branch protection, accepts the logs, and issues the OIDC token cosign signs with. pluto's contribution is a warm base plus an ephemeral box that runs the official runner once. | [docs] + [inference] |
| 3 | **Forge auth without a GitHub App is a one-line answer: a repo-scoped registration token.** `POST /repos/{owner}/{repo}/actions/runners/registration-token`, authenticated by a PAT with `repo` (private) or `public_repo` (public) scope. finpilot is public, so `public_repo` is enough. | [docs] + [api] |
| 4 | **The local path (pluto reads the workflow files) works for the build and lint half and dead-ends at the release half.** Keyless OIDC signing cannot be reproduced outside GitHub — the certificate identity is minted by `token.actions.githubusercontent.com` and names `github.com/<repo>/.github/workflows/`. A local run can build `:stable-testing`; it cannot sign it in a way the promotion gate accepts. | [inference], evidence in §6 |
| 5 | **The compute profile is one heavy box, not many.** The build job wants ~44 GB free disk, podman 5.x on rootful native overlay, a btrfs loopback at `/var/lib/containers`, `sudo`, and Docker for one container action. The other five jobs want minutes and a handful of packages. pluto's density question (#119) is decided by this workload in favour of "one runner-shaped box", not by fan-out. | [repo] + [inference] |

---

## 2. What finpilot is

A **bootc operating-system image template**: it assembles a Fedora Silverblue-derived
OCI image from shared layers rather than by mutating an existing image
(`Containerfile:38-53`, `README.md:3-8`). The desktop configuration comes from
`ghcr.io/projectbluefin/common`, Homebrew from `ghcr.io/ublue-os/brew`, and the rest is
the fork's own (`build/`, `custom/`). The output is consumed with
`bootc switch --transport registry ghcr.io/<owner>/<repo>:stable` (`README.md:131`).

Languages and tooling, as they appear in CI: **shell** (every build script is bash
with `set -euo pipefail`), **YAML** (workflows, Renovate config, pre-commit config,
Artifact Hub, issue templates), **TOML** (`iso/disk.toml`, `iso/iso.toml` — bootc
image builder config), **Ruby by implication only** — Brewfiles are a Ruby DSL and the
repo deliberately refuses to *evaluate* them in CI (`build/validate-brewfiles.sh:1-12`),
**bats** for tests, and **just** as the single entry point for build, lint, validate and
test. There is no compiled language in the repo at all. [repo]

The build system is the `Justfile` (661 lines) driving `podman build` against the
`Containerfile`, whose four phases are ordered so the expensive package layer is not
invalidated by an overlay edit (`Containerfile:75-116`, `build/README.md`). The
Containerfile is deliberately verbose and read-first: each `RUN` block carries a
comment explaining what it does and why it is ordered where it is. [repo]

The release model is a two-branch promotion: `main` publishes `:stable-testing`,
`stable` never rebuilds, and `execute-release.yml` copies the already-signed digest to
`:stable` (`AGENTS.md:21-26`, `README.md:95-109`). Images are signed keyless with Cosign
via OIDC (`README.md:111-124`).

---

## 3. The CI inventory

### 3.1 Twelve workflows, read line by line

| File | Trigger (`on:`) | Jobs | Runner | Timeout | What it does |
|---|---|---|---|---|---|
| `build-image.yml` | `push` to `main` (`paths-ignore: "**.md"`, `.github/workflows/*validate*.yml`); `workflow_dispatch` (:15-23) | 1 (`build`, :49) | `ubuntu-24.04` (:57) | 360 min (:58) | Builds, tags, pushes, signs the image; writes size/layer telemetry to the step summary (:251-263) |
| `pr-validation.yml` | `pull_request` to `main`/`stable` (:5-8) | 1 (`validate`) | `ubuntu-latest` (:17) | 10 min (:18) | `projectbluefin/actions/bootc-build/validate-pr` — shellcheck, hadolint, `just check`, pre-commit |
| `unit-tests.yml` | `pull_request` + `push` to `main`/`stable`, `paths-ignore: "**.md"` | 1 (`bats`) | `ubuntu-latest` (:26) | 10 min (:27) | Downloads `just` 1.58.0 (SHA-verified), `apt-get install bats`, runs `just test-unit` |
| `validate-brewfiles.yml` | `pull_request` on `custom/brew/**`, `build/validate-brewfiles.sh`, its test, itself | 1 | `ubuntu-latest` (:14) | 15 min (:15) | `Homebrew/actions/setup-homebrew`, then `just validate-brewfiles` |
| `validate-flatpaks.yml` | `pull_request` on `custom/flatpaks/**`, its script, its test, itself | 1 | `ubuntu-24.04` (:14) | unset | `apt-get install flatpak`, then `build/validate-flatpaks.sh` |
| `validate-justfiles.yml` | `pull_request` on `**/*.just`, `Justfile`, itself | 1 | `ubuntu-latest` (:13) | unset | `extractions/setup-just`, then `just check` |
| `validate-renovate.yml` | `pull_request` + `push` on `.github/renovate.json`, the two renovate workflows | 1 (`validate`, a **reusable workflow call**) | (caller) | — | `projectbluefin/actions/.github/workflows/reusable-validate-renovate.yml` |
| `renovate.yml` | `schedule: 0 */6 * * *`; `workflow_dispatch`; `push` on the renovate config + its workflow | 2 (`token` gate, then `renovate` reusable call) | `ubuntu-latest` (:22) | — | Runs Renovate only if `RENOVATE_TOKEN` is set |
| `promote-main-to-stable.yml` | `schedule: 0 4 * * *`; `workflow_dispatch` | 5 (`promote` reusable call, `repair-promotion-branch`, `locate-promotion-pr`, `unblock-promotion-checks`, `gate` reusable call) | `ubuntu-latest` ×4 | 10 and 5 min | Opens the daily squash promotion PR, repairs its tree, finds the PR, approves `action_required` check runs, runs the release gate |
| `execute-release.yml` | `push` to `stable`; `workflow_dispatch` | 3 (`check-trigger`, `execute` reusable call, `release-notes` reusable call) | `ubuntu-latest` (:23) | — | Refuses non-promotion pushes; promotes the candidate digest; creates the GitHub release |
| `sync-stable-to-main.yml` | `push` to `stable`; `workflow_dispatch` | 1 (`sync`, reusable call) | (caller) | — | Merges `stable` back into `main` |
| `clean.yml` | `schedule: 0 0 * * 0`; `workflow_dispatch` | 1 | `ubuntu-latest` (:15) | — | Deletes GHCR package versions older than 90 days |

[repo] All paths are under `.github/workflows/`.

### 3.2 What is *not* there — the things that usually dominate a gap analysis

Read against the same files: **no `strategy.matrix` in any workflow** (grep `matrix` →
no match). **No `services:` block** (grep `services:` → no match). **No `jobs.*.container:`
or `image:` job-level container**. **No `actions/upload-artifact` or
`actions/download-artifact`** — nothing crosses a job boundary through the artifact
service. **No self-hosted label anywhere**: every `runs-on` is `ubuntu-24.04` (2×) or
`ubuntu-latest` (10×), and the repo has **zero self-hosted runners registered**
(`gh api repos/siddhj2206/finpilot/actions/runners` → `total_count: 0`) [api]. **No
GHES**, **no Windows or macOS runner**, **no untrusted-fork CI** — the workflows only
run for the owner and for Renovate's bot branches. [repo]

This matters for scoping: the gap list below is shorter than a generic "port a repo to
pluto" analysis would be, because five of the notorious Actions features (matrix,
service containers, job containers, artifacts, cross-org runners) simply do not appear.

### 3.3 Third-party `uses:` — what the jobs actually pull in

Pinned by SHA everywhere, Renovate-maintained [repo]:

| Action | Where | Kind |
|---|---|---|
| `actions/checkout@3d3c42e…` (v7) | 6 workflows | JavaScript |
| `projectbluefin/actions/bootc-build/{preflight,setup-runner,dnf-cache,generate-tags,chunka,push-image,sign-and-publish,validate-pr,ghcr-cleanup}@837850b…` | build, pr-validation, clean | **composite** [repo, metadata read at that ref] |
| `projectbluefin/actions/.github/workflows/reusable-{promote-squash,release-gate,execute-release,release,sync-branches,renovate,validate-renovate}.yml@837850b…` | promote, execute-release, sync, renovate, validate-renovate | **reusable workflows** |
| `nick-fields/retry@ad984534…` (v4) | build (2×) | JavaScript |
| `extractions/setup-just@53165ef7…` (v4) | validate-justfiles | JavaScript |
| `Homebrew/actions/setup-homebrew@848c272c…` (pinned to `main`) | validate-brewfiles | JavaScript |
| `hadolint/hadolint-action@06be81ba…` (v3.5.0) | inside `validate-pr` | **Docker** (`using: docker`, `image: Dockerfile`) [repo] |
| `ublue-os/remove-unwanted-software@695eb75b…`, `ublue-os/container-storage-action@dc1f4c8f…` | inside `setup-runner` | composite |
| `taiki-e/install-action`, `sigstore/cosign-installer`, `oras-project/setup-oras`, `anchore/sbom-action/download-syft`, `actions/attest@1e69f48a…`, `actions/attest-build-provenance@4d101475…`, `actions/cache@55cc8345…` | inside the composites | JavaScript |

Read of the composite metadata [repo, `raw.githubusercontent.com/projectbluefin/actions/837850b…`]:

- `setup-runner` **deletes software from the runner host to free disk** (its own
  description: "free 20GB -> 50GB"), **mounts a btrfs loopback filesystem at
  `/var/lib/containers`**, installs podman 5.x from Ubuntu 26.04 ("resolute") apt
  repos onto a 24.04 runner, and rewrites `/etc/containers/storage.conf` for **rootful
  native overlay**.
- `dnf-cache` is `actions/cache/restore` + `actions/cache/save` (v6.1.0) over
  `/var/tmp/buildah-cache-*`, keyed `<os>-<arch>-buildah-<name>-<cache-bust>` with
  `restore-keys` for a warm start.
- `validate-pr` runs `just check`, `shellcheck` (`apt-get install shellcheck=0.9.0-1`),
  `pip install pre-commit==4.6.0`, caches `~/.cache/pre-commit`, hadolint, and
  `pre-commit run --all-files`.
- `sign-and-publish` **requires `ACTIONS_ID_TOKEN_REQUEST_URL`** and is documented
  "(GitHub-hosted runners only)".

### 3.4 Secrets, permissions, concurrency

- **Secrets used**: `secrets.GITHUB_TOKEN` (4 sites in `build-image.yml`, 1 in
  `clean.yml`) and `secrets.RENOVATE_TOKEN` (2 sites in `renovate.yml`, one as a named
  input to a reusable). The repository's secret list is exactly
  `["RENOVATE_TOKEN"]` [api]. `github.token` / `GH_TOKEN` is used in the promotion,
  unblock and validate jobs. Cosign is keyless — **no key is stored anywhere**
  (`README.md:111-114`).
- **Permissions**: workflow-level grants are narrow and per-job; `build-image.yml:24-28`
  takes `contents: read`, `packages: write`, `id-token: write`, `attestations: write`.
  `pr-validation.yml` and `unit-tests.yml` open with `permissions: {}` and re-grant
  per job.
- **Concurrency**: `build-image.yml:43-45` groups on `${{ github.workflow }}-${{ github.ref }}`
  with `cancel-in-progress: true`; `pr-validation`/`unit-tests` group per PR number and
  cancel; `promote`, `execute-release` and `sync` group **without** cancellation
  (deliberately — a promotion must not be interrupted mid-way).
- **Reporting**: no `upload-artifact`. The only structured output is the step summary
  (`build-image.yml:251-263`, `$GITHUB_STEP_SUMMARY`), and `AGENTS.md:28-33` defines the
  PR-comment policy for this repo's own agents (one comment per PR event, fold new
  findings in, nothing when the only finding is "tests pass").

### 3.5 Where it runs today

**GitHub-hosted, exclusively.** `ubuntu-24.04` twice (pinned deliberately —
`projectbluefin/actions` asserts 24.04 when adding its resolute apt source, and
`.github/renovate.json` disables the Ubuntu major upgrade for exactly that reason),
`ubuntu-latest` ten times, zero self-hosted runners registered [api]. The repo's own
`AGENTS.md:13-15` frames the validation gate as "the `validate` status check runs
shellcheck and hadolint on a pull request" — a GitHub-native check-run contract.

---

## 4. What its CI depends on that pluto does not have yet

| Dependency | finpilot's use | pluto today | Severity for path A (runner) | Path B (local) |
|---|---|---|---|---|
| Forge auth without a GitHub App | nothing today; would be needed to register a runner | no forge adapter, no credential store (#120 §3.4) | **must build** — a PAT with `public_repo` (repo is public) and the registration-token call | not needed for reading, needed for `checkout` |
| Runner registration | n/a | none | **must build** — `config.sh --ephemeral` with the token, then `run.sh` once [docs] | not applicable |
| `GITHUB_TOKEN` with `packages`/`attestations`/`id-token: write` | `preflight`, `push-image`, `sign-and-publish`, ren | none, and #120's tail says pluto will not scope a token it does not have | **free** — GitHub mints it and hands it to the runner | **blocked** — needs a PAT with `packages: write`, and OIDC cannot be faked |
| Secrets delivery | `RENOVATE_TOKEN` | no secret store | **free** — GitHub delivers it in the job message | **must build** — pluto's own secret store, then map `secrets.RENOVATE_TOKEN` onto it |
| Reusable workflows (`jobs.<id>.uses:` + `secrets: inherit`) | 6 of 12 workflows | explicitly in the parity tail (#120 §8) | **free** — GitHub does it | **the blocker for the release half** |
| OIDC / keyless cosign / `actions/attest*` | signing, SBOM + provenance attestations | none; #120 tail excludes OIDC | **free** — the runner receives `ACTIONS_ID_TOKEN_REQUEST_URL`/`…TOKEN` | **not reproducible** — the identity is minted by GitHub's issuer and names `github.com/<repo>/.github/workflows/` |
| Actions cache v1 service | `dnf-cache` (buildah cache), pre-commit cache, apt cache | `internal/envcache` exists for *environment layers*, not the cache protocol | **free** — GitHub's own cache is used, no pluto code | **must build** — the `_apis/artifactcache` v1 service (#120 §9.2), and only works when `GITHUB_SERVER_URL` is honestly `https://github.com` |
| Registry push to ghcr | `push-image` | none | **free** — the job's `GITHUB_TOKEN` carries `packages: write` for this repo | **must build** — a declared PAT secret |
| Docker container actions | `hadolint/hadolint-action` (1 step) | none | **must build** — the runner needs Docker or a container hook [docs] | **must build** — tier 2 container actions need a runtime in the box (#119) |
| Forge API surface (check runs, PR approvals, refs, labels) | `unblock-promotion-checks`, `locate-promotion-pr`, `repair-promotion-branch`, `pr-validation`'s tree check, `clean.yml`'s ghcr API | M4 event *intake* exists (webhook → task); no *outbound* forge client | **free** — GitHub is the orchestrator | **must build** — and it is forge-shaped work with no local payoff |
| Cancellable `concurrency` groups | `build-image`, `pr-validation`, `unit-tests` | not modelled | **free** | excluded by #120 §8 (best-effort only) |
| Service containers | none | excluded by #120 §8 | n/a | n/a |
| Artifacts | none | excluded | n/a | n/a |
| Matrix | none | supported in the subset spec | n/a | n/a |
| `hashFiles()` | `cache-bust: ${{ hashFiles('**/Containerfile') }}` (`build-image.yml:115`) | in the subset spec; implemented in act's interpreter | free | **must build** — it is the cache key |
| Log/step-summary reporting | `$GITHUB_STEP_SUMMARY` | pluto has job logs (`internal/runner/job.go`) and a step-summary file protocol in the subset spec (#120 §2.3) | free — GitHub is the sink | **must build** |

---

## 5. Path A — runner mode: workflows stay on GitHub, pluto executes

This is the Blacksmith shape and the one the ticket asks for: **GitHub keeps the
orchestration, pluto supplies the box.** Nothing in finpilot's workflow files changes,
including `runs-on: ubuntu-24.04`.

### 5.1 What GitHub supplies, for free, in this mode

The job payload the runner receives already carries: a one-time `GITHUB_TOKEN` scoped
to the job's declared `permissions`; the repository's secrets (`RENOVATE_TOKEN`); the
OIDC request endpoints when `id-token: write` is granted (which is what
`sign-and-publish` asserts with `ACTIONS_ID_TOKEN_REQUEST_URL` [repo]); the check-run
and commit-status surface branch protection reads; the log and step-summary sink; the
cache service; and the concurrency-group cancellation. [docs] Every one of the "must
build" cells in §4's path-A column disappears, because the orchestrator is GitHub.

### 5.2 Auth without a GitHub App

The registration-token endpoint is the whole answer:

```
POST /repos/siddhj2206/finpilot/actions/runners/registration-token
```

It "returns a token that you can pass to the config script… The token expires after
one hour", and "authenticated users must have admin access to the repository".
Authentication is "an access token or a GitHub app", and the scopes are documented:
`repo` for private repositories, **`public_repo` for public** ones, `admin:org` for
organizations [docs]. finpilot is public (`visibility: public` [api]), so a
fine-grained-ish classic PAT with `public_repo` is sufficient — no App installation, no
private key, no webhook, no installation-id table. That is the "no GitHub Apps"
constraint solved by the smallest credential that can possibly work. [docs] + [api]

Registration throughput is not a concern at this scale: "1500 runners / 5 minutes /
repository/org/enterprise" [docs], and one ephemeral runner per run registers and
deregisters once.

### 5.3 The ephemeral runner loop

GitHub's own recommendation is the design: "GitHub recommends implementing autoscaling
with ephemeral self-hosted runners", registered with `--ephemeral`, after which "The
GitHub Actions service will then automatically de-register the runner after it has
processed one job" [docs]. That is a one-job-per-box contract, which is precisely the
moonshot's "ephemeral box per run" (#116, #118's run boxes: discard on success, retain
on failure).

Three documented operational facts that shape the loop [docs]:

- **A runner that does not pick up an assigned job within 60 seconds is re-queued**,
  and a job that stays queued for **24 hours fails**. So the box must come up and
  register faster than the queue tolerates, or the job is lost. This is a hard
  warm-base requirement: the runner must be present in the base image (or on a host
  cache), not installed at run time.
- **The runner application auto-updates unless registered with `--disableupdate`**,
  and a runner more than 30 days behind is not given jobs. pluto must pin the runner
  version in the warm base and rebuild it on a schedule.
- **The runner's own application logs are not preserved by GitHub**, and GitHub's
  warning is explicit that ephemeral deployments must forward them to external
  storage. pluto's run record is the natural place: capture `_diag` into the job log
  before destroying the box.

### 5.4 What the box must look like

Derived from what the jobs do [repo]:

| Requirement | Where it comes from | Note |
|---|---|---|
| A Linux distro on GitHub's supported list (Ubuntu 20.04+, Fedora 29+, RHEL 8+…) [docs] | every `runs-on` | `ubuntu-24.04` and `ubuntu-labels` both resolve to it |
| Root/`sudo` | `setup-runner` mounts filesystems and rewrites `/etc/containers/storage.conf`; `validate-pr` runs `sudo apt-get install`; `build` runs `sudo -E just build` | The box is runner-shaped, not rootless |
| podman 5.x, **rootful native overlay**, btrfs loopback at `/var/lib/containers` | `setup-runner` (metadata [repo]) | The action does the work; the box must permit it (loop devices, `mount`, btrfs tools) |
| ≥ ~40 GB free disk | `remove-unwanted-software`'s own claim ("free 20GB -> 50GB") [repo] | an OS-image build with a dnf cache and a multi-GB output image |
| **Docker** (or a container hook) | `hadolint/hadolint-action` is `using: docker`, and GitHub documents that Docker must be installed for container actions [docs] | one step; the runner can be pointed at podman via the documented container-hook mechanism [docs] |
| `just`, `bats`, `shellcheck` 0.9.0, `hadolint`, `pre-commit` + python/pip, `flatpak`, (optionally) Homebrew | `unit-tests.yml`, `validate-pr`, `validate-flatpaks.yml`, `validate-brewfiles.yml` | the lint/test half |
| The `actions/runner` tarball, ~228 MB, version-pinned | `actions/runner` v2.338.0 `actions-runner-linux-x64-…tar.gz` = 227,660,598 bytes [api] | register with `--disableupdate`, rebuild the base on release |
| Outbound 443 to the documented domain list [docs] | logs, cache, actions download, ghcr, OIDC | github.com, api.github.com, *.actions.githubusercontent.com, codeload.github.com, results-receiver…, *.blob.core.windows.net, objects…, ghcr.io, *.pkg.github.com, pkg-containers… |
| Outbound to the workload's own registries | `Containerfile` FROM lines, `Justfile` pins | quay.io, ghcr.io, copr.fedorainfracloud.org, dl.flathub.org, github.com releases, the Ubuntu resolute apt mirror, PyPI, sigstore (Fulcio/Rekor) |

No `/dev/kvm` is needed: the VM recipes (`just build-qcow2`, `run-vm*`) are local dev
paths and never run in CI [repo]. No inbound ports are needed: logs, cache and OIDC are
all fetched *by* the runner over outbound HTTPS [docs], which is the same shape as
pluto's own egress posture.

### 5.5 Labels

`runs-on` is a label match [docs], so the box registers with the label set the
workflows name: `ubuntu-24.04` and `ubuntu-latest` both resolve to this base — today
GitHub maps `ubuntu-latest` to 24.04 for this repo (the renovate config holds the major
for exactly that reason [repo]). Adding `self-hosted`, `Linux`, `X64` is free and makes
any future `runs-on: [self-hosted, linux]` resolve too, which #120's subset spec already
calls for as the shape a self-hosted repo writes.

### 5.6 The cost, honestly

pluto's contribution is: one warm base, one credential, one registration call, an
ephemeral box, and a teardown. Everything else in this path is GitHub's. The work is
real but small — it is area 3 on the map's priority list ("more YAML, incl. runner mode
+ forge auth") and depends on area 1's run loop (#118).

---

## 6. Path B — pluto reads the same workflow files and runs them locally

Same 12 files, no GitHub orchestration. The verdict is per workflow, and it splits
cleanly along the fork in the repo's own design.

### 6.1 Runs locally with modest work

| Workflow | Why it works | What it needs |
|---|---|---|
| `unit-tests.yml` | pure `run:` steps; the bats suite stubs podman/skopeo/git/date on `PATH` (`tests/template/justfile-build_test.bats:15-89`) | `just` 1.58.0 and `bats` in the base; `hashFiles` not needed |
| `validate-flatpaks.yml` | one `apt-get install flatpak` and one script | root; egress to flathub |
| `validate-justfiles.yml` | one setup action and `just check` | a native `just`, or tier-3 JS for `extractions/setup-just` |
| `validate-brewfiles.yml` | Homebrew on Linux, or the script's own logic without it | `brew` in the base (heavy) or an allowlisted JS action |
| `build-image.yml` — the build itself | `sudo -E just build` is pure shell + podman | podman 5.x, btrfs, ≥40 GB, root; the dnf/buildah cache; the registry layer cache through ghcr |
| `pr-validation.yml` | `validate-pr` is a **composite**, so it inlines to shell steps | shellcheck, hadolint (as a *native* binary, sidestepping the Docker action), pre-commit + python, `actions/cache` for the pre-commit cache |

### 6.2 Dead-ends, and why

1. **Reusable workflows.** Six workflows call
   `projectbluefin/actions/.github/workflows/reusable-*.yml@<sha>` with `secrets: inherit`.
   #120's parity tail puts cross-repo reusable workflows out of scope and names the
   cost precisely: "a second fetcher, a second trust decision, a second secrets model,
   and a nesting limit to police". Building it would buy pluto exactly one thing — the
   ability to run release automation for repos that borrowed someone else's.
2. **Signing.** `sign-and-publish` is documented "GitHub-hosted runners only" and asserts
   `ACTIONS_ID_TOKEN_REQUEST_URL`. The certificate identity the promotion gate verifies
   is `https://github.com/<repo>/.github/workflows/`, minted by
   `token.actions.githubusercontent.com` [repo]. A pluto-run build can produce
   `:stable-testing`; it cannot produce a signature the gate accepts, so
   `execute-release`'s reusable reports `release/blocked` — the repo's own README says
   exactly this for an unsigned candidate (`README.md:123-124`).
3. **`GITHUB_TOKEN` scoping.** With no GitHub token to scope, `packages: write` push
   becomes "supply a PAT", which is a *different* trust shape than the job's declaration
   and is exactly the credential pluto has no store for yet.
4. **Forge-shaped jobs.** `unblock-promotion-checks` approves `action_required` runs;
   `locate-promotion-pr` shells out to `gh pr list`; `repair-promotion-branch` force-pushes
   a branch; `pr-validation`'s first step reads refs through `gh api`. All four are
   GitHub logic that is *orchestration*, not compute. Running them locally means
   reimplementing the orchestrator's job badly.

**[inference]** The honest reading of path B: pluto can own the **build** and the
**lint/test** half of finpilot's CI — the parts that are actually compute — and the
release half stays on GitHub. That is the subset working as specified, and it is the
same boundary #120 drew from act's, Gitea's and Woodpecker's documented gap lists.

### 6.3 What path B would otherwise have to build, in order

1. Basic YAML + the run model + logging (area 1 — the first stint already in flight).
2. Steps and the `GITHUB_ENV` / `GITHUB_PATH` / `GITHUB_OUTPUT` / `GITHUB_STATE` /
   `GITHUB_STEP_SUMMARY` file protocols in the guest agent (#120 §2.3).
3. `uses:` tiers — native `checkout` (needs forge auth to clone), composite inlining,
   container actions (needs a runtime in the box), and the opt-in JS tier with Node in
   the warm base.
4. The Actions cache v1 service (`GET /_apis/artifactcache/cache`, `POST /caches`,
   `PATCH /caches/:id`, …) so the stock `actions/cache@v6.1.0` inside `dnf-cache` works
   unmodified — with the documented caveat that the stock action refuses any
   `GITHUB_SERVER_URL` that is not `github.com` / `*.ghe.com` / `*.localhost`
   (#120 §4.2).
5. `hashFiles()`, for the `cache-bust` key.
6. `secrets.*` resolution against pluto's store, with masking in pluto's own logs.
7. Then stop — the release half is the tail.

---

## 7. What the compute profile implies for the warm base and the run loop

### 7.1 Two box shapes, one of them heavy

| | lint/test shape | build shape |
|---|---|---|
| Workflows | unit-tests, validate-×4, pr-validation | build-image |
| Size of base | ~1–2 GB of packages [inference] | ~4–5 GB [inference] |
| Disk free needed | a few GB | **~40 GB** [repo, vendor claim from the action] |
| Privilege | root (`apt-get install`, `pip install`) | root + loop devices + btrfs + `mount` |
| Duration | 1–10 min (workflow timeouts are 5/10/15 min) [repo] | tens of minutes; `timeout-minutes: 360`, retry `max_attempts: 3`, `retry_wait_seconds: 300` [repo] |
| Network | flathub, PyPI, Homebrew, GH releases | quay.io, ghcr.io, COPR, Ubuntu resolute apt, sigstore |

**[inference]** The retry configuration is the most informative number in the repo: the
author expects a build to take minutes, not hours, and to *sometimes fail flakily*. On
pluto, that retry is not `nick-fields/retry` but the native one — a new run under the
same task (#118's vocabulary), which is strictly better than re-running a step inside a
box that may be in a bad state.

### 7.2 Warm base: one runner image, content-addressed, version-pinned

The warm base is a **runner base**, not a language base: the pinned `actions/runner`
tarball, podman 5.x with rootful overlay, btrfs tooling, Docker (or a container hook),
`just`/`bats`/`shellcheck`/`hadolint`/`cosign`/`skopeo`/`jq`/python-pip/flatpak, and the
apt/pip package lists already updated. It should be treated as an environment layer in
the existing sense — content-addressed, keyed by a setup fingerprint
(`internal/envcache/cache.go` [repo, pluto]) — with the runner version in the
fingerprint, because GitHub stops queueing jobs to a runner more than 30 days behind
[docs]. On the container substrate this is the warm base plus a read-only root and a
per-run upper dir (#119 D5); on a microVM it is the durable disk plus a per-run layer.

**[inference]** Two substrate consequences fall straight out of this workload. First,
the box must be **root-capable**, which pushes the build shape toward a microVM or a
privileged container, not a rootless one — #119's rootless-podman default is right for
the lint shape and wrong for the build shape. Second, the build job wants
`/var/lib/containers` on **btrfs with native (non-FUSE) overlay**, which is a host
filesystem decision, not a box decision.

### 7.3 Environment layers and caches: two different objects

- **Environment layer** — the runner base above. Shared, immutable,
  fingerprint-keyed. pluto has this shape already.
- **Caches** — the buildah/DNF cache at `/var/tmp/buildah-cache-*`, the pre-commit hook
  cache, and the weekly-rotated apt cache [repo, action metadata]. These are mutable,
  key-value, host-local. #120's decision to speak the Actions cache v1 protocol and own
  the store applies here; #121 owns the multi-host question.
- **The registry layer cache already exists and is cross-machine**: `just build` passes
  `--cache-from`/`--cache-to ghcr.io/<vendor>/<image>` when `REGISTRY_CACHE_WRITE=1`
  (`Justfile:223-233`), and CI sets that (`build-image.yml:39`). **[inference]** This is
  the important finding for density: the expensive layer reuse for finpilot's build is
  *already* solved through ghcr, so a second pluto host gets a warm build by pulling a
  registry, not by sharing a pluto-specific cache. pluto's own cache only needs to cover
  what the registry cache does not — the dnf/buildah cache directories and the apt/pip
  caches.

### 7.4 The ephemeral run loop

One job per box, one runner per job [docs]:

1. Trigger (push to `main`, PR, schedule, dispatch) → task → run → job.
2. Boot the runner base as an ephemeral box from the environment layer.
3. Mint a registration token, then
   `config.sh --url https://github.com/<owner>/<repo> --token … --ephemeral --disableupdate --labels ubuntu-24.04,self-hosted,Linux,X64`.
4. Run the runner once; stream its stdout into pluto's job log and tee to the run record.
5. GitHub deregisters the runner after the job [docs]; capture `_diag` first.
6. Success → destroy the box. Failure → **retain it**, because "when a run fails, you
   can enter the box that ran it" (#116) is the moonshot's own promise, and this
   workload's failure mode (a build that failed at minute 40 of a dnf transaction) is
   exactly the case where entering the box beats reading the log.
7. Cancellation: GitHub cancels the concurrency group; pluto must translate that into
   destroying the box mid-job. This is the one place pluto needs its own cancellation
   path into the box.

Concurrency: at most **one build at a time per repository** here (`cancel-in-progress:
true` on a per-ref group [repo]), and the lint jobs cancel per PR number. So the
scheduler needs one build slot and N lint slots, not a fan-out planner. A matrix would
have changed that; there is none. [repo]

---

## 8. Migration sketch — zero changes to finpilot's workflow files

**In finpilot: nothing.** No workflow edit, no new repo secret, no branch-protection
change, no label change. The repo already has exactly the right settings for someone
else's runner to pick up its work: Actions enabled, read/write workflow permissions,
auto-merge on, `validate` as the required check on both branches
(`.agents/skills/onboarding/SKILL.md` steps 2–9).

**What is needed, in order:**

1. **One credential.** A PAT with `public_repo` scope (the repo is public [api]), stored
   in pluto — the existing `RENOVATE_TOKEN` untouched, still GitHub-side. **[pluto must
   build: a credential store.** `actions-subset.md` §3.4 already names this as absent.]
2. **The registration call.** `POST /repos/{owner}/{repo}/actions/runners/registration-token`
   → `config.sh --ephemeral --disableupdate` → `run.sh` (one job) → GitHub deregisters
   [docs]. **[pluto must build: a GitHub forge adapter** — narrow in the build, per
   #116's "general at the seams, narrow in the build".]
3. **The runner base image**, built by pluto: pinned runner, podman 5.x + btrfs,
   Docker-or-container-hook, and the lint toolchain. **[pluto must build: the environment
   layer for a runner shape**, with the runner version in the fingerprint.]
4. **The ephemeral loop** from §7.4 — `_diag` capture, retain-on-failure, cancellation.
   **[pluto must build: this is #118's first deliverable**, the run loop, with a
   forge-shaped trigger instead of a CLI one.]
5. **Labels** matching the two `runs-on` values. Trivial, but it is what makes the same
   workflow file work unchanged on both GitHub's runners and pluto's.

**[inference]** Net new pluto code, honestly counted: a credential store, a GitHub forge
adapter with registration + labels, a runner base image, and a trigger→run wiring. That
is smaller than any individual research ticket, and it is the first slice that makes
"durable work, ephemeral boxes, wherever you want" demonstrably true on a real
repository. Path B is a *second* product decision that can wait until the runner path
runs and someone asks for offline runs.

### 8.1 The gap list in build order

**Path A (runner mode) — the recommended first slice:**

| Order | Gap | Why first | Effort |
|---|---|---|---|
| A1 | Credential store (one PAT, `public_repo`, injected into the box at run time; masked in logs) | nothing else works without it; §4 of #120 already names it absent | small |
| A2 | Runner base as an environment layer (pinned runner, podman 5.x + btrfs, Docker or container hook, lint toolchain), version in the fingerprint | the 60-second pickup window and the 24-hour queue deadline make an installed-at-run-time runner a non-starter [docs] | medium |
| A3 | Forge adapter: mint registration token, `config.sh --ephemeral --disableupdate --labels …`, `run.sh` once, deregister | the core of the path | small |
| A4 | Ephemeral run loop wired to a forge trigger: box per job, retain-on-failure, `_diag` capture, cancellation | this is #118's first deliverable | medium |
| A5 | Reporting on top: run record, per-step log, follow; the GitHub-native logs stay canonical | the DX differentiator | medium |
| A6 | Egress policy for the documented domain list plus the workload's registries | one config table | small |
| A7 | Cache: dnf/buildah + apt/pip caches on the host; the registry layer cache needs nothing | #120's protocol decision only matters for path B | medium |

**Path B (local) — only after A, and only for the compute half:**

| Order | Gap | Note |
|---|---|---|
| B1 | Basic YAML, run model, logging | area 1, in flight |
| B2 | Steps + the `GITHUB_*` file protocols in the guest agent | #120 §2.3 |
| B3 | `uses:` tiers: native checkout, composite inlining, container actions, opt-in JS | composites dominate this repo; JS is the expensive tier |
| B4 | Actions cache v1 service, when `GITHUB_SERVER_URL` is honestly github.com | #120 §9.2 |
| B5 | `hashFiles()` | needed for the `cache-bust` key |
| B6 | Secret store + `secrets.*` resolution and masking | shared with A1 |
| B7 | Stop. Reusable workflows, OIDC, forge API jobs — the tail | #120 §8 |

---

## 9. Open questions this note does not decide

1. **Runner binary: official tarball, or pluto's own runner client?** The official
   `actions/runner` v2.338.0 is 227,660,598 bytes for linux-x64 [api], bundles .NET and
   Node, and self-updates. The alternative is implementing the job protocol, a much
   larger and forever-moving target. **[inference]** Vendor the official tarball, pin the
   version in the warm base, `--disableupdate`, rebuild the base on a schedule. The open
   sub-question is size/licence comfort, not engineering.
2. **Docker for one action.** `hadolint/hadolint-action` is the only Docker action in the
   repo. Does the runner base carry dockerd (heavy, a daemon inside the box), a socket
   mount from the host, or the documented container-hook shim to podman [docs]?
3. **Substrate split.** #119 built one backend and measured. This workload says the
   *build* shape wants a root-capable, btrfs-capable, loop-device-capable box while the
   *lint* shape wants nothing. One substrate for both, or two?
4. **Host filesystem.** Does a pluto host running finpilot's build need btrfs *anywhere*
   — a loopback image file, or a real btrfs subvolume for `/var/lib/containers`? A host
   prerequisite to document alongside #119's `/dev/kvm` list.
5. **Where the buildah/dnf cache lives**, and how two pluto hosts share it — #121's
   question, now with a concrete first customer.
6. **Is `runs-on: ubuntu-latest` ever 26.04?** Today the renovate config pins 24.04
   because `setup-runner` asserts it [repo]. If upstream ever supports 26.04, the label
   `ubuntu-latest` starts meaning a *different* base, and pluto's label→base mapping has
   to handle that without editing the workflow.
7. **Untrusted CI stays out**, per #116 — but this repo already has
   `github-actions[bot]` pushing promotion branches, and `promote-main-to-stable.yml` has
   to *approve* the check runs GitHub holds for them. In runner mode that is GitHub's
   problem; in path B it would be pluto's.

---

## Sources

**Primary — the finpilot repository**, read 2026-10-10 at
`/var/home/sid/Documents/Projects/finpilot` (upstream `projectbluefin/finpilot`, fork
`siddhj2206/finpilot`, public [api]):

- `README.md`, `AGENTS.md`, `CONTRIBUTING.md`, `Justfile`, `Containerfile`,
  `.pre-commit-config.yaml`, `.github/renovate.json`, `.github/hadolint.yaml`,
  `.github/workflows/*.yml` (12 files, quoted by line), `.agents/skills/*/SKILL.md`,
  `build/*.sh`, `build/README.md`, `custom/**`, `iso/*.toml`, `tests/**/*.bats`.

**Action metadata** (`raw.githubusercontent.com`, ref `837850b6100b22c7920476035e7e1c28fdbba67f`
of `projectbluefin/actions`, plus the pinned refs of `hadolint/hadolint-action` and
`ublue-os/remove-unwanted-software`), read 2026-10-10 — establishes that `setup-runner`,
`dnf-cache`, `validate-pr`, `preflight` and `sign-and-publish` are composite, what they
install, and which of them need the runner host's filesystem.

**GitHub documentation**, access date 2026-10-10:

- Self-hosted runners reference — <https://docs.github.com/en/actions/reference/runners/self-hosted-runners>
  (ephemeral runners, `--ephemeral`, 60-second pickup, 24-hour queue, required domains,
  Docker for container actions, authentication scopes, runner updates).
- REST API endpoints for self-hosted runners —
  <https://docs.github.com/en/rest/actions/self-hosted-runners> (registration-token
  endpoint, one-hour expiry, admin access required).
- Usage limits — <https://docs.github.com/en/actions/reference/limits> (1500 runner
  registrations / 5 minutes / repository, 5-day self-hosted job execution, 24-hour job
  queue, cache rate limits).
- Customizing the containers used by jobs —
  <https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/customize-containers>
  (container hooks, podman as an alternative to docker).

**In-repo (pluto):**

- `docs/research/actions-subset.md` (#120) — the subset spec, the `uses:` tier ladder,
  the cache-protocol decision, the parity tail, §3.4's note that pluto has no secrets
  store.
- `docs/research/container-backend.md` (#119) — D1–D7, the warm base plus ephemeral
  layer mechanics, host prerequisites.
- `docs/research/shared-state-and-portable-resume.md` (#121) — cache ownership.
- `docs/research/container-and-microvm-hardening.md` — supply chain, SHA pinning.
- `internal/envcache/cache.go`, `internal/runner/job.go` — the environment-layer and
  job-log shapes this note's warm base and reporting build on.
- `GLOSSARY.md` — trigger / task / run / job / box / environment layer.
- `docs/VISION.md`, `docs/pitch.md`.
