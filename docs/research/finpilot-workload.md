# finpilot as a Pluto workload — durable dev + ephemeral CI

Read-only study of `/var/home/sid/Documents/Projects/finpilot` (upstream
`projectbluefin/finpilot`; local fork `siddhj2206/finpilot`, public). All file
paths below are relative to that repo unless prefixed otherwise. This note
deliberately looks past "GitHub Actions compatibility" and asks what the
*workload* needs from Pluto, for durable development and for CI/ephemeral work.

Note: Pluto already has a prior research note,
`/var/home/sid/Documents/Projects/pluto/docs/research/finpilot-pilot.md`, which
is almost entirely about the Actions-parity question (runner mode vs. local
workflow execution). This note complements it: it treats finpilot as an
**OS-image build/dev workload** first and an Actions workload second.

---

## 1. What finpilot is

finpilot is a **template for building your own bootc operating-system image**:
it assembles a Fedora Silverblue-derived OCI image from shared OCI layers rather
than by mutating an existing image, and it is explicitly "built to be driven by
hand or by an agent" (`README.md:1-11`). The desktop configuration comes from
`ghcr.io/projectbluefin/common`, Homebrew from `ghcr.io/ublue-os/brew`, and the
fork's own content from `build/` and `custom/` (`Containerfile:36-53`,
`.agents/skills/overview/SKILL.md:9-33`). The output is not an app: it is an
installable OS image (`:stable-testing` / `:stable`), consumed with
`sudo bootc switch --transport registry ghcr.io/<owner>/<repo>:stable`
(`README.md:95-133`), and it can also be turned into a bootable QCOW2/RAW disk
or an installer ISO (`Justfile:356-378`, `iso/disk.toml`, `iso/iso.toml`).

Concrete evidence:

- Template nature / shared-layer assembly: `README.md:3-8`,
  `Containerfile:16-53`, `.agents/skills/overview/SKILL.md:9-33`.
- Agent-driven intent: `README.md:9`, `AGENTS.md` (whole file), `.agents/skills/`
  (7 skills: overview, onboarding, customize, build, ci, troubleshooting, plus
  README).
- Two-branch release model (main → `:stable-testing`; stable → `:stable` via
  digest promotion, never a rebuild): `README.md:95-109`, `AGENTS.md:19-26`,
  `.agents/skills/ci/SKILL.md:29-53`.
- Scale: 74 tracked files, 3.9 MB working tree, 831 commits (single owner,
  public, no external contributors, no untrusted-fork CI).
- Language/tooling: bash (all build scripts), YAML, TOML, bats, just; no
  compiled language in the repo. (Matches the pilot note's §2.)
- The image is a *desktop OS*: systemd services, GNOME/Silverblue base,
  Flatpak, Homebrew, `ujust`, `uupd` (`build/10-overlay.sh:115-133`,
  `build/20-packages-and-services.sh`, `README.md:71-76`).

---

## 2. Command / recipe inventory

The `Justfile` (661 lines) is the single developer entry point. Grouped by what
they need from the host.

### 2.1 Image build (container image) — rootless-capable, cacheable, minutes

- **`just build [image] [tag]`** — `podman build` of the `Containerfile`
  (`Justfile:141-240`). Reads the base image name/tag from the `FROM` line
  (`Justfile:150-157`), computes a Bluefin-style version string, checks GHCR for
  tag collisions via `skopeo` (`Justfile:178-192`), passes `GITHUB_TOKEN` as a
  build secret when present (`Justfile:218-221`), and adds registry layer cache
  args when `REGISTRY_CACHE_WRITE=1` (`Justfile:223-233`).
  - Tools: `podman`, `skopeo`, `jq`, `git`.
  - Cache: BuildKit `--mount=type=cache` for `/var/cache/libdnf5` and
    `/var/cache/rpm-ostree` (`Containerfile:91-92,101-104`), plus
    `--cache-from`/`--cache-to ghcr.io/<vendor>/<name>` (`Justfile:226-233`).
  - Cost: package layer is the expensive one; the Containerfile is explicitly
    ordered so an overlay edit does not invalidate it (`Containerfile:64-116`,
    `build/README.md:7-14`).
  - Disk: CI's `setup-runner` action frees "20GB -> 50GB" and the pilot note
    estimates ~44 GB needed; a full OS-image build with a dnf cache and a
    multi-GB output.
  - A bare `podman build .` is unsupported — build through `just`
    (`.agents/skills/build/SKILL.md:58-61`).

- **`just tag-images <name> <tag> <tags>`** — `podman inspect`/`untag`/`tag`
  (`Justfile:242-264`).

### 2.2 OS artifact build (QCOW2 / RAW / ISO) — **privileged, rootful, long**

- **`just build-qcow2` / `build-raw` / `build-iso`** (and `rebuild-*`) — convert
  the container image to a bootable disk/ISO with Bootc Image Builder
  (`Justfile:303-378`).
  - `_rootful_load_image` (`Justfile:266-301`): because BIB runs rootful, the
    user-built image is copied into root's podman store with
    `podman image scp` (or pulled from the registry).
  - `_build-bib` (`Justfile:306-351`): runs
    `sudo podman run --rm --privileged --net=host
    --security-opt label=type:unconfined_t`, mounts
    `/var/lib/containers/storage:/var/lib/containers/storage`, uses
    `--use-librepo=True --rootfs=btrfs`, and the pinned BIB image
    (`Justfile:5`).
  - ISO path reads the image's `image-info.json` to get the published reference
    (`Justfile:314-326`).
  - Disk layout: root grows to `20 GiB` or twice the base (`iso/disk.toml`).
  - Tools: `sudo`/root, `podman` (rootful), `btrfs`, the BIB container image.

### 2.3 VM run / boot test — **KVM + firmware, or a privileged container**

- **`just run-vm-qcow2` / `run-vm-raw` / `run-vm-iso`** (`Justfile:399-568`).
  Native QEMU first (`-accel kvm`, `-machine q35`, OVMF firmware, ssh forwarded
  to `127.0.0.1:2222`), falling back to `ghcr.io/qemus/qemu` with
  `--rm --privileged --device=/dev/kvm` and a browser console (`Justfile:506-556`).
  The ISO path creates a 64G sparse scratch target disk (`Justfile:478-487`).
- **`just spawn-vm`** — `systemd-vmspawn`, requires `qemu-system-x86_64` and a
  writable `/dev/kvm` (`Justfile:570-612`).
- Needs: `/dev/kvm`, OVMF (`edk2-ovmf`/`ovmf`), `qemu-system-x86_64` or the
  container fallback.

### 2.4 Validate / lint / test — minutes, light

- **`just lint`** — shellcheck every git-tracked `*.sh` (`Justfile:614-641`).
- **`just format`** — shfmt (`Justfile:643-661`).
- **`just check`** — `just --unstable --fmt --check` over `Justfile` and every
  `*.just` (`Justfile:31-34,18-29`).
- **`just test-unit` / `test-contract` / `test-template`** — bats over
  `tests/` (`Justfile:36-73`); needs `bats`.
- **`just validate-brewfiles`** — greps Brewfiles and calls `brew info`; needs
  **Homebrew installed** (`Justfile:76-80`, `build/validate-brewfiles.sh:26-29`).
- **`just validate-flatpaks`** — needs `flatpak` + network to Flathub
  (`Justfile:82-87`, `build/validate-flatpaks.sh:28-33`).
- **`just clean` / `sudo-clean`** (`Justfile:94-124`).

### 2.5 Build-phase scripts (what runs inside the image build)

- `build/00-image-info.sh` — writes `os-release` + `image-info.json` identity.
- `build/10-overlay.sh` — rsyncs `projectbluefin/common` shared layer and
  `ublue-os/brew` over `/`; copies custom Brewfiles/ujust/flatpaks; curls the
  Flathub remote descriptor; `systemctl enable`s brew/flatpak/ublue/podman
  units (`build/10-overlay.sh:36-136`).
- `build/20-packages-and-services.sh` — `dnf5 install -y just gum fzf jq`;
  `copr_install_isolated "ublue-os/packages" uupd`; enables `uupd.timer`
  (`build/20-packages-and-services.sh`).
- `build/90-cleanup.sh` — reverts dnf settings, disables third-party repos,
  prunes `/var`, `/tmp`, `/boot`, `/run` (`build/90-cleanup.sh`).
- Inactive `.example` phases: Tailscale (third-party RPM repo pattern),
  GNOME extensions, **NVIDIA** (pulls prebuilt akmods by exact kernel version,
  writes kargs, installs CDI toolkit — `build/50-nvidia.sh.example`), desktop
  swap (`build/60-desktop-swap.sh.example`).

### 2.6 Tool matrix and privilege

| Need | Commands | Privilege | Cacheable / expensive |
|---|---|---|---|
| podman, skopeo, jq, git, just | `build`, `tag-images` | rootless user | **yes** — dnf/buildah cache mounts + ghcr registry cache |
| rootful podman + `--privileged` + btrfs + host container storage | `build-qcow2/raw/iso`, `rebuild-*` | **root/privileged** | partly — BIB output; base container image layer |
| `/dev/kvm` + OVMF + qemu | `run-vm-*`, `spawn-vm` | KVM group / device | no (runtime) |
| bats, shellcheck, shfmt | `test-unit`, `lint`, `format`, `check` | none | no |
| Homebrew | `validate-brewfiles` | none (heavy install) | maybe |
| flatpak + network | `validate-flatpaks` | none | no |
| bootc-image-builder image, qemus/qemu image | `build-*`, `run-vm-*` fallback | rootful/privileged | pulled from ghcr |

### 2.7 What a developer actually does (the durable loop)

Edit a phase (e.g. `build/20-packages-and-services.sh` or activate
`build/50-nvidia.sh.example`) → `just build` (minutes) → `just build-qcow2`
(BIB, privileged, longer) → `just run-vm-qcow2` (KVM) → boot, check
`systemctl status brew-setup.service` / `flatpak-preinstall.service`, run
`ujust` → repeat. This is the loop `README.md:148-168` and
`.agents/skills/build/SKILL.md` describe, and it is exactly the kind of
iteration that benefits from a **warm, durable box**: the expensive package
layer, the dnf/buildah caches, the BIB tooling, and the built artifacts all want
to persist between iterations.

---

## 3. CI inventory

12 workflows under `.github/workflows/`. Full detail is in
`pluto/docs/research/finpilot-pilot.md` §3; the load-bearing facts here:

| Workflow | Trigger | Runner | Timeout | Does |
|---|---|---|---|---|
| `build-image.yml` | push `main`; dispatch | `ubuntu-24.04` | 360 min | builds, tags, pushes, **keyless OIDC signs**, attests |
| `pr-validation.yml` | PR | `ubuntu-latest` | 10 min | `validate-pr` composite: shellcheck, hadolint, `just check`, pre-commit |
| `unit-tests.yml` | PR/push | `ubuntu-latest` | 10 min | install just 1.58.0 + bats, `just test-unit` |
| `validate-brewfiles.yml` | PR paths | `ubuntu-latest` | 15 min | Homebrew setup + `just validate-brewfiles` |
| `validate-flatpaks.yml` | PR paths | `ubuntu-24.04` | — | `apt-get install flatpak` + script |
| `validate-justfiles.yml` | PR paths | `ubuntu-latest` | — | `just check` |
| `validate-renovate.yml` | PR/push | reusable | — | validates Renovate config |
| `renovate.yml` | schedule `0 */6 * * *`; dispatch | `ubuntu-latest` | — | runs Renovate if `RENOVATE_TOKEN` set |
| `promote-main-to-stable.yml` | schedule `0 4 * * *`; dispatch | `ubuntu-latest` ×4 | 5–10 min | opens/repairs promotion PR, approves checks, release gate |
| `execute-release.yml` | push `stable`; dispatch | reusable | — | promotes candidate digest, creates GitHub release + inline SBOM |
| `sync-stable-to-main.yml` | push `stable` | reusable | — | merges stable hotfixes back to main |
| `clean.yml` | schedule `0 0 * * 0` | `ubuntu-latest` | — | deletes GHCR versions >90 days |

Key facts:

- **Runners:** every `runs-on` is `ubuntu-24.04` (2×) or `ubuntu-latest` (10×);
  **zero self-hosted runners** are registered (pilot note §3.5). No matrix, no
  service containers, no job containers, no `upload-artifact` (pilot note §3.2).
- **Secrets:** exactly `GITHUB_TOKEN` (build/clean) and `RENOVATE_TOKEN`
  (renovate). **Cosign is keyless — no signing key exists anywhere**
  (`README.md:111-124`).
- **Signing / provenance:** `build-image.yml:233-249` calls
  `projectbluefin/actions/bootc-build/sign-and-publish` with `signing-mode:
  keyless`, `generate-sbom: false`, `push-attestation: true`, and a
  `certificate-identity-regexp` scoped to the repo's workflows. The release gate
  re-verifies the cosign signature before promoting
  (`execute-release.yml:75-93`); an unsigned candidate reports
  `release/blocked` (`README.md:123-124`).
- **Published artifacts:** OCI images to `ghcr.io/<owner>/<name>` with many
  alias tags (`build-image.yml:144-231`), a GitHub Release with an inline SBOM
  (`execute-release.yml:95-117`), SLSA provenance attestation
  (`build-image.yml:239-249`), and Artifact Hub metadata baked into image labels
  (`Containerfile:142-155`, `artifacthub-repo.yml`). **No ISOs/QCOW2 are built
  in CI** — the VM/ISO recipes are local-only (`Justfile:303-378`).
- **Build runner shape:** `setup-runner` (`build-image.yml:101-107`) sets
  `storage-backend: btrfs`, `native-overlay: "true"`, updates podman, installs
  `just`; `dnf-cache` restore/save wraps the build
  (`build-image.yml:109-115,191-198`); the build step is retried 3× with a
  300 s wait (`build-image.yml:117-130`).
- **What would break off GitHub-hosted runners** (pilot note §4, §6.2):
  reusable workflows (6 of 12), keyless OIDC signing and `actions/attest*`,
  `GITHUB_TOKEN` scoping for ghcr push, the Actions cache v1 service used by
  `dnf-cache`, the Docker container action (`hadolint`), and the forge-shaped
  jobs (approve check runs, `gh pr list`, force-push, tree checks). The release
  half is GitHub-shaped by construction.

---

## 4. Prioritized Pluto capabilities this workload implies

These go beyond "run GitHub Actions jobs locally." Each is motivated by
finpilot evidence. Ordered by how central it is to the user's framing ("a very
real example of what all I see myself using pluto for").

### P1 — Durable dev box for OS-image iteration, with a nested/privileged engine

**Why:** The core finpilot loop is multi-step, slow, and stateful: edit phase →
`just build` → `just build-qcow2` (BIB) → `just run-vm-qcow2` → boot → test.
The package layer and caches are expensive; the BIB step needs rootful podman
and `--privileged` (`Justfile:306-351`); the VM needs `/dev/kvm`
(`Justfile:463-504`). A fresh box every time throws away the warm package layer,
the dnf/buildah caches, and the built artifacts.

**Implies:** a durable box that persists `/var/lib/containers`, the build caches,
and `output/` across runs; a **privileged nested engine** (`docker: {privileged:
true}` per Pluto ADR 0019, which already names "the finpilot/Bluefin bootc path"
as the motivating case); and KVM device access for boot testing. This is the
strongest signal in the repo for the durable side.

### P2 — BuildKit/Containerfile-aware warm layers (cache mounts, not just lockfiles)

**Why:** finpilot's cache is not a language lockfile. It is BuildKit
`--mount=type=cache` for `/var/cache/libdnf5` and `/var/cache/rpm-ostree`
(`Containerfile:91-92,101-104`) plus a **registry layer cache** in ghcr
(`Justfile:223-233`, `build-image.yml:39`). Pluto's package layer keyed on
`provision.watch` lockfiles (`pluto.yml` docs:19-23) does not describe either.

**Implies:** `provision.base: { dockerfile: ... }` must preserve BuildKit cache
mounts across builds (or expose a host-side cache), and provision should
understand `--cache-from`/`--cache-to` registry reuse. The registry cache already
crosses machines, so a second Pluto host gets warm builds by pulling ghcr — the
pilot note calls this "the important finding for density."

### P3 — Long-running services inside the box (engine daemon, local registry, VM)

**Why:** The build loop wants a container engine already running and image
iterations already local. `_rootful_load_image` exists only because the user's
rootless podman and BIB's rootful podman are separate stores (`Justfile:266-301`);
inside one box the engine could just be there. BIB and the QEMU fallback pull
their own images (`Justfile:5-6`). A local registry (or a single engine) would
remove the `podman image scp` handoff.

**Implies:** Pluto's `service: true` tasks used for a box-local container engine
daemon and/or a local registry, restarted on every wake; and durable volumes so
pulled images survive a stop.

### P4 — Attach-to-failed-build debugging (retain the box that failed)

**Why:** finpilot's failures are slow and stateful: a build that fails at minute
40 of a dnf transaction; a VM that boots to a black screen
(`build/60-desktop-swap.sh.example:32-33`); a first boot with no Flatpaks or no
`brew` (`README.md:158-168`); an NVIDIA image whose driver only proves itself on
real hardware (`build/50-nvidia.sh.example:20-22`). CI masks this with a 3×
retry (`build-image.yml:117-130`). Pluto's own promise — "when a run fails, you
can enter the box that ran it" (`docs/VISION.md:19-20`) — is exactly what this
workload needs.

**Implies:** retain-on-failure, `pluto attach`, and durable volumes that keep the
half-built image, BIB output dir, and VM disk for inspection.

### P5 — A heavyweight, privileged ephemeral CI box (runner-shaped, not lint-shaped)

**Why:** The `build-image` job is not a lint job. It wants root + loop devices +
btrfs + native overlay + ≥ ~40 GB disk (`build-image.yml:101-115`, pilot note
§7.1–7.2), because it is doing a full OS-image build. The lint/test jobs are
minutes and light.

**Implies:** at least two box shapes for this workload: a light ephemeral box for
lint/test and a heavy privileged box for the image build, with a host btrfs
and disk-space prerequisite documented the way Pluto documents `/dev/kvm`.
This is a workload that pushes against the rootless default.

### P6 — Scheduled rebuilds / drift detection, not just cron CI

**Why:** finpilot has a genuine scheduled surface: Renovate every 6 h
(`renovate.yml`), daily promotion (`promote-main-to-stable.yml`), weekly cleanup
(`clean.yml`), and in-image `uupd` timers. The base image moves via Renovate, and
there are explicit timing windows (NVIDIA akmods built daily vs. base weekly,
`build/50-nvidia.sh.example:36-37`). A rebuild also embeds a date-based version
(`Justfile:172-176`).

**Implies:** Pluto `schedules` that wake the durable box, rebuild, and record a
run; ideally a diff of what changed (base digest moved? package set changed?).
This is "scheduled work with a place to land" applied to OS images, and it is the
durable counterpart to Renovate.

### P7 — Artifact publication as a first-class output (OCI push, ISOs, qcow2, SBOM, signatures)

**Why:** finpilot's real outputs are OCI images pushed to ghcr
(`Justfile:235-264`, `build-image.yml:224-231`), installable ISOs and qcow2/raw
disks (`Justfile:356-378`), plus CI SBOM and provenance
(`execute-release.yml:104-117`). Registry auth is a credential
(`Justfile:218-221`; local push/cache needs a token). Pluto's layers are already
OCI-shaped (`ADR 0017`; `actions-subset.md` §2.5's `oci://` artifact channel).

**Implies:** boxes that can push OCI artifacts to a registry using Pluto
secrets, retain built ISOs/qcow2 as artifacts, and mask registry/signing tokens
in logs. Note the tension: **keyless OIDC signing cannot be reproduced off
GitHub** (pilot note §6.2), so signing off-GitHub needs a key-based path or
stays a CI-only step.

### P8 — Secrets + masking for registry, signing, and dependency automation

**Why:** finpilot needs `GITHUB_TOKEN` for ghcr push and as a build secret
(`Justfile:218-221`, `build-image.yml:121`), and `RENOVATE_TOKEN` for dependency
automation (`renovate.yml:27-47`). Pluto already models `secrets` + `mask`
(`pluto.yml` docs:84-90).

**Implies:** declared registry credentials injected into the build box and masked
from build logs. This is shared with the CI runner path in the pilot note.

### P9 — Forge-aware triggers for *durable* agent work (agents are tasks)

**Why:** finpilot is explicitly agent-driven (`README.md:9`, `AGENTS.md`,
`.agents/skills/`), with humans triaging and agents working assigned/queued issues
(`AGENTS.md:48-53`). Durable work here is an agent editing the image, running the
long build, and opening a PR — over hours, surviving a laptop sleep. Pluto's
"an agent is a task, not an integration" (`docs/VISION.md:44-46`) fits directly.

**Implies:** the durable box carries the full toolchain (just, podman, skopeo,
bats, shellcheck, brew) so an agent can iterate unattended; eventually
PR/release events trigger runs, but the durable loop does not depend on GitHub
Actions compatibility at all.

### P10 — Multi-arch / cross-build (weak evidence — flag as open question)

**Why (thin):** finpilot is x86_64-only today (BIB `--rootfs=btrfs`,
`qemu-system-x86_64`, no `--platform` anywhere). But its base options include
CentOS/Hummingbird and the NVIDIA example builds an akmods tag from `%{ARCH}`
(`build/50-nvidia.sh.example:32`), and upstream Bluefin publishes multi-arch.

**Implies (if wanted):** `--platform` in provision, qemu-user/binfmt in the box.
Evidence is not sufficient to prioritize this; listed to surface the question.

### P11 — Host capability declaration/detection

**Why:** finpilot silently requires `/dev/kvm`, OVMF, btrfs, loop devices, root,
and ~40–50 GB free disk (`Justfile:422-453,570-587`; `build-image.yml:101-107`).
Pluto already lists host prerequisites (`ADR 0014`, `ADR 0019`).

**Implies:** a way for a contract to declare "this box needs KVM / privileged /
btrfs / N GB" and for Pluto to fail early with a legible message, rather than
failing deep in a build.

### Capability summary (one line each)

1. Durable OS-image dev box + privileged nested engine (P1).
2. BuildKit/Containerfile cache-mount + registry-cache awareness (P2).
3. Box-local services: engine daemon / registry / VM (P3).
4. Retain-and-attach on failed builds (P4).
5. Heavy privileged ephemeral CI box shape (P5).
6. Schedules that rebuild and record drift (P6).
7. OCI/ISO/qcow2 artifact publication + secrets/masking (P7, P8).
8. Agent-as-durable-task workflow (P9).
9. Multi-arch (open) (P10).
10. Host-capability declaration (P11).

---

## 5. Open questions for the user

1. **What is the durable target — the container build, or the booted OS?**
   `just build` (podman, rootless) and `just build-qcow2` + `run-vm` (BIB,
   privileged, KVM) have very different privilege/disk/host needs. Which is the
   "durable development" loop you mean?
2. **Do you want to iterate a real booted VM inside the box?** That needs
   `/dev/kvm` passthrough and a durable VM disk; or is building artifacts
   enough?
3. **Signing off-GitHub.** Keyless OIDC cannot be reproduced locally (pilot note
   §6.2). Do you want a key-based signing path in the durable box, or keep
   signing as a GitHub-only release step?
4. **Multi-arch:** is x86_64-only acceptable, or is aarch64 on the roadmap?
5. **Should Pluto own artifact publication** (push to ghcr, publish ISOs) or only
   build and leave publishing to CI?
6. **Registry cache ownership:** is ghcr (`--cache-from`/`--cache-to`) the cache,
   or do you want Pluto to own a local registry/cache so a build does not depend
   on ghcr?
7. **Release half:** keep promotion/signing/release on GitHub (the pilot note's
   recommendation), or bring it onto Pluto? The release workflows are forge-shaped
   by construction.
8. **Box sharing:** should agent tasks and your interactive `just` runs share one
   durable box (lock policy `shared`/`exclusive`), or run separately?
9. **Homebrew in the warm base:** `just validate-brewfiles` needs Homebrew, which
   is heavy. Acceptable in a shared warm base, or keep it out?
10. **Representativeness:** is finpilot meant to drive a generic "OS-image build"
    box shape, or a finpilot-specific one? The capability list above is
    generalizable, but P1/P2/P5 are the load-bearing, harder ones.

---

### Cross-references (Pluto, read-only)

- `/var/home/sid/Documents/Projects/pluto/docs/research/finpilot-pilot.md` —
  the Actions-parity study (runner mode vs. local execution).
- `/var/home/sid/Documents/Projects/pluto/docs/research/actions-subset.md` —
  the Actions subset spec, `uses:` tiers, cache protocol, parity tail.
- `/var/home/sid/Documents/Projects/pluto/docs/adr/0019-in-box-engine-is-rootless.md`
  — names "the finpilot/Bluefin bootc path" as the `docker: {privileged: true}`
  case.
- `/var/home/sid/Documents/Projects/pluto/docs/adr/0017-layers-rebase-beneath-durable-volumes.md`,
  `docs/pluto-yml.md`, `docs/VISION.md`, `GLOSSARY.md`.
