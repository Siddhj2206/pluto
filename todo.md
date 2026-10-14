# todo — moonshot core follow-ups and the next spec

Written 2026-10-14 at the close of spec **#130** (Moonshot core — YAML, durable work,
and ephemeral CI). Reference doc for wayfinding the next spec in a fresh chat.

## Where things stand

- **Spec:** GitHub issue **#130** (`Siddhj2206/pluto`), part of map **#117** (Wayfinder:
  durable work, ephemeral boxes) under moonshot **#116**.
- **Tickets:** **#138–#151**, all implemented and **closed**.
- **PR #152** (`moonshot/core → next`) is **merged** (merge commit `7fdc225`); `next`
  carries the moonshot core and this file. `master` is untouched.
- **Tickets were closed manually**, not by the PR: GitHub's `Closes #…` keywords only fire
  on the *default* branch, and the PR targeted `next`. Each was closed with a comment
  pointing at the merge.
- **Map #117 stays open** — areas 3–6 are still staged.
- **Verification:** `gofmt`, `go vet`, `go test ./...`, `go generate`/`go mod tidy`
  clean-tree checks, and GitHub Actions all green.
- **ADRs/glossary:** ADRs 0014–0019 added (containers are the runtime; core is vcs/forge
  agnostic; boxes own their terminal; layers rebase beneath durable volumes; runs share a
  box by lock policy; in-box engine is rootless). Older ADRs 0001/0002/0006/0007/0010 marked
  superseded. `GLOSSARY.md` rewritten around repo/workspace/layer/volume/box/task/run.
- **Exploration notes** from the build (may still exist on this host):
  `/tmp/opencode/moonshot-notes/` — 01 contract, 02 runner/lifecycle, 03 daemon/runs,
  04 CLI harness, 05 PTY/attach/masking, 06 images/engine. Useful prior art for wayfinding.

## Conventions decided after the merge

- **Referenced contract files live under `.pluto/`, not `pluto/`.** `docs/pluto-yml.md`
  and the README quickstart now use `./.pluto/provision.yml`, `./.pluto/wake.yml`,
  `./.pluto/tasks/<name>.yml`, `./.pluto/Dockerfile`, and state the convention explicitly.
  Update the spec #130 example and any new examples to match. (`pluto init`'s starter is
  inline, so it needs no change — but if init ever scaffolds file refs, they go under
  `.pluto/`.)
- **`pluto init` still exists.** It scaffolds a minimal `pluto.yml` and never overwrites
  one; the README quickstart now leads with `pluto init` rather than hand-writing the file.
  What #150 removed was its post-commit hook (which called the deleted `pluto event`), not
  the command.
- Open: should area 3 extend `pluto init` (e.g. to set up runner registration / forge
  auth), and should the closed spec #130 example be edited to `.pluto/`?

## Highest-priority follow-up: the daemon background service

**Decision: the daemon's own background service should exist.** Ticket #150 ("Retire the
microVM substrate") removed *all* host systemd unit management, including the daemon's own
unit and `pluto install`/`uninstall`. That over-reached: the spec's Remove list says "host
systemd units", which in context means the **per-box** `pluto-box@.service` machinery
(microVM-specific). The daemon's unit is how the *salvaged* daemon gets supervised, and
dropping it leaves the daemon foreground-only (`pluto daemon`).

**Do:** restore a daemon systemd **user** unit plus `pluto install` / `pluto uninstall`,
keeping the container path only. Do **not** reintroduce per-box host units, cgroup
drop-ins, or the in-guest systemd agent.

Since #152 is merged, this wants its own ticket (or a small follow-up branch on `next`).

## Next spec — workload-driven, not just "Actions compatibility"

Spec #130 stages area 3 as "GitHub runner integration and forge auth". That framing is
**too narrow**: the real question is making Pluto genuinely useful for the workloads it
exists to serve, and the GitHub runner worker is one deliverable within that, not the
whole spec.

The reference workload is **finpilot** (`/var/home/sid/Documents/Projects/finpilot`): a
bootc OS-image template that assembles a Fedora Silverblue-derived image from shared OCI
layers, publishes `:stable-testing`/`:stable` to ghcr, and builds qcow2/raw/ISO artifacts
locally for VM boot testing. It is explicitly "built to be driven by hand or by an agent".
A read-only study is at `docs/research/finpilot-workload.md` (the raw notes are also at
`/tmp/opencode/finpilot-notes.md` on this host); the older Actions-parity study is
`docs/research/finpilot-pilot.md`.

### What finpilot implies (prioritized)

1. **Durable dev box for OS-image iteration, with a nested/privileged engine (P1).** The
   loop is edit phase → `just build` (podman, minutes) → `just build-qcow2`
   (bootc-image-builder: rootful, `--privileged`, btrfs, host container storage) →
   `just run-vm-qcow2` (`/dev/kvm` + OVMF) → boot → test. Expensive, stateful, cache-heavy:
   it wants a warm box that persists `/var/lib/containers`, the build caches, and `output/`.
   ADR 0019 already names "the finpilot/Bluefin bootc path" as the
   `docker: {privileged: true}` case.
2. **BuildKit/Containerfile cache-mount + registry-cache awareness (P2).** finpilot's cache
   is not a language lockfile: BuildKit `--mount=type=cache` for `/var/cache/libdnf5` and
   `/var/cache/rpm-ostree`, plus a ghcr registry layer cache (`--cache-from`/`--cache-to`).
   Pluto's `provision.watch` lockfile keying describes neither.
3. **Box-local services (P3).** A container-engine daemon and/or a local registry as
   `service: true` tasks, restarted on wake, with durable volumes so pulled images survive
   a stop.
4. **Retain-and-attach on failed builds (P4).** Failures are slow and stateful (a dnf
   transaction at minute 40; a black-screen boot). Keep the box, attach, inspect.
   (Retention and attach already exist via #148/#144.)
5. **A heavyweight, privileged ephemeral CI box shape (P5).** The image build wants root,
   loop devices, btrfs, native overlay, and ~40 GB; lint/test jobs are light. At least two
   box shapes, with host prerequisites documented like `/dev/kvm`.
6. **Schedules that rebuild and record drift (P6).** Renovate every 6h, daily promotion,
   weekly cleanup, in-image `uupd` timers. Wake the durable box, rebuild, record a run, and
   ideally diff what changed (base digest moved? package set changed?).
7. **Artifact publication as a first-class output (P7).** Push OCI images, retain built
   ISOs/qcow2 as artifacts. Caveat: keyless OIDC signing cannot be reproduced off GitHub.
8. **Secrets + masking for registry/signing/dependency automation (P8).** `GITHUB_TOKEN`
   for ghcr push, `RENOVATE_TOKEN` for Renovate — declared and masked (already modelled).
9. **Agent-as-durable-task (P9).** finpilot is agent-driven; durable work is an agent
   editing the image, running the long build, and opening a PR over hours. This does not
   depend on Actions compatibility at all.
10. **Multi-arch (P10, weak evidence) and host-capability declaration (P11).** The latter
    lets a contract declare "needs KVM / privileged / btrfs / N GB" and fail early with a
    legible message.

### The Actions/runner piece (still in scope, as one part)

- The **GitHub runner worker**: register Pluto as a self-hosted Actions runner and speak
  the runner protocol, so a GitHub job lands on an ephemeral box.
- **Forge auth without a GitHub App**: a PAT-backed **registration-token** flow (story 48).
- Each job gets a fresh ephemeral box seeded from the package layer (built: `pluto ci`,
  `box.CreateEphemeral`, #140 layers); success discarded, failure retained + attachable.
- Workflows and reusable actions stay in GitHub's normal locations; GitHub keeps
  orchestrating workflows, status, and step logs.
- **Acceptance tests:** GitHub runner-protocol tests through the same CLI harness with a
  controllable protocol fixture.

Still out of scope (per #130 "Out of Scope"): full Actions parity (the larger `uses`
surface, reusable workflows, artifacts, service/job containers, advanced matrices, OIDC
emulation), GitLab/other forges, GitHub Apps, untrusted external PRs/forks.

### Open questions to resolve while wayfinding

From the finpilot study — answer these to shape the spec:

1. Durable target: the container build (`just build`, rootless) or the booted OS
   (`build-qcow2` + `run-vm`, privileged + KVM)?
2. Iterate a real booted VM inside the box (needs `/dev/kvm` passthrough + a durable VM
   disk), or is building artifacts enough?
3. Signing off-GitHub: a key-based path in the durable box, or keep signing GitHub-only?
4. Multi-arch: x86_64-only acceptable, or aarch64 on the roadmap?
5. Does Pluto own artifact publication (ghcr push, ISO publish) or only build?
6. Registry cache ownership: ghcr as the cache, or a Pluto-owned local registry/cache?
7. Release half: keep promotion/signing on GitHub, or bring it onto Pluto?
8. Should agent tasks and interactive runs share one durable box (lock policy), or split?
9. Homebrew in the warm base (heavy) — acceptable, or keep it out?
10. Is finpilot meant to drive a generic "OS-image build" box shape, or a finpilot-specific
    one?

### Current state that area 3 builds on

- `pluto ci <task>` → `POST /v1/ci` → `box.Manager.CreateEphemeral` (fresh box, no durable
  volumes, seeded from base+package layers) → run record + step logs; success discards,
  failure retains with an attach path; retained boxes evicted at the earlier of 48h/5
  unless pinned; orphaned durable boxes collected. CLI: `pluto ci`, `pluto box pin|unpin`.
- Daemon HTTP-over-unix-socket surface, CLI + in-process acceptance harness, client.
- vcs adapter (`internal/vcs`) and the forge side still to come.

## Deferred items carried out of #130

These were flagged during implementation or the two-axis code review. None block #152.

### Behaviour / correctness

1. **Multi-step tasks.** Spec user story 20 ("one or more steps"); the contract still
   declares a single `run`, so every task is one implicit step. The run record
   (`state.Run.Steps`, per-step `Step.Log`) already supports many steps. No ticket AC
   required it — pick up in a later contract revision.
2. **Secrets for service tasks and the wake phase.** Declared `secrets:` are injected into
   the exec env for `pluto run` / `pluto ci` / scheduled runs (and masked). Service tasks
   and the `wake` phase still run without injected secrets.
3. **`mask:` prefix token set.** Prefixes redact the following run of ASCII letters, digits,
   `_`, `-`. Confirm that's the intended token alphabet for real credential formats.
4. **Retained failed-box eviction count is host-global, not per-workspace.** Spec says "the
   earlier of 48 hours or 5 boxes" with no per-workspace qualifier; confirm intent for a
   multi-repo host.
5. **Container-engine smoke tests are opt-in** (`-tags container`), excluded from default
   CI. The "a task can run `docker build` / `buildah` / `bootc` inside the box" criterion
   is only exercised there. Rootless default assumes nested user namespaces + `/dev/fuse`;
   `docker: {privileged: true}` is the escape hatch.
6. **Orphan-GC grace** is measured from `box.UpdatedAt`; a stopped box with other record
   mutations can have its grace clock nudged.
7. **`api.Error.Contract` is never set by the daemon** (pre-existing), so the CLI's
   "contract" hint rarely fires.
8. **`state.Schedule.Job`** is retained only so pre-cutover records still read; the new
   scheduler never sets it.
9. **Terminal surface:** the attach shell is hard-coded `/bin/sh -i` (no shell/command
   selection); `api.TerminalError` is dead code; `--watch` holds the shared lock like
   `attach` (a long-lived watcher delays an exclusive run).
10. **`runOutputSink`** persists the running record twice per manual/CI run (needed so
    `logs --follow` can read the step's log ref mid-run); `box.start` now makes one extra
    read-only `engine.Status` probe.
11. **Provider public ingress** (`provider route add|remove`) and `pluto connect` were
    removed along with the legacy SSH/vsock runner. Decide whether the container era wants
    an ingress surface (area 3/6 territory).

### Docs / naming

12. **`devices.toml` → `devices.yml`** (needed to drop the TOML dependency) has no
    auto-migration: an existing `~/.config/pluto/devices.toml` silently yields an empty
    registry. Add a rename-with-notice or document the manual step.
13. **Dangling historical docs links.** `docs/DEFERRED.md`, `docs/pitch.md`,
    `docs/review-1-*.md`, `docs/m5-*.md`, `docs/task-api.md`, `docs/webhooks.md`,
    `docs/service-connections.md` were removed; ADRs and `docs/research/*` still cite some
    of them. ADRs keep their historical `.pluto.toml` references by design (superseded).
14. **`image` vs `layer` naming** (`DefaultImage`, `BaseImage`, `--image`) sits on the
    GLOSSARY's "layer → avoid: image" list, but names a real OCI artifact. Leave or rename.

### Explicitly decided against (do not re-litigate without reason)

- The daemon systemd **unit** removal is treated as a mistake (see above) — but the
  per-box units, cgroup drop-ins, in-guest systemd agent, vsock, Firecracker, image
  builder, and environment cache stay removed.
- MicroVM as a second backend stays out (ADR 0014); the runner seam remains so a substrate
  could return if measurements justify it.
