# pluto — vision

**pluto gives every git worktree a durable work machine: a microVM box that wakes for work, sleeps when idle, and can be entered with SSH — on hardware you own.**

## The job

Developers run long work — agents, builds, tests — tied to a laptop that sleeps. Cloud sandboxes solve that by owning your machine and your account. pluto solves it on hardware you own, with no accounts, no control plane, and no telemetry.

Three uses, one mechanism:

- **Agent runs that outlive the laptop.** A coding agent runs in the branch's box; close the laptop; come back and attach to the machine, not a chat log.
- **Scheduled work with a place to land.** Nightly builds, tests, migrations, agent sweeps wake on a schedule. When something fails you enter the machine that failed instead of reading logs.
- **Warm branch environments.** Every branch keeps its installed dependencies, running services, and databases between sessions.

## Where pluto sits

pluto is the self-hosted intersection of three tools:

- **mise** — per-project dev experience: one config for tools, env, and tasks. pluto's box contract is mise's config for a machine instead of a shell.
- **Amp Orbs** — agents that work on a machine of their own and keep going after the laptop closes. pluto's sessions are the self-hosted, per-worktree version.
- **GitHub Actions** — work that wakes on a schedule or an event and lands somewhere you can inspect. pluto's schedules and, later, triggers are the self-hosted version.

The position is the intersection: mise's dev experience, Orbs' durable remote agent, and Actions' triggered work — on hardware you own, with no accounts, no control plane, and no telemetry.

## The claim

The novelty is the lifecycle, not the storage: a machine that belongs to a branch, wakes for work, sleeps when done, and can be entered. It is agent-agnostic by construction — an agent is a job or a service, not an integration.

Not claimed: memory snapshots; cross-host handoff in v1; isolation or security guarantees; a hosted service; agent-specific APIs; content-addressed deduplication.

The demo that proves it: a schedule fires, an agent works, the machine sleeps; you attach to the machine that did the work. It runs end to end on a dev host — `scripts/e2e-m1.sh` asserts the M1 loop and `scripts/e2e-m2.sh` the agent-session loop, each cleaning up ([docs/testing.md](testing.md)). Numbers reported at M0: time to first shell, pause/wake latency, provision duration.

## Public shape

- Verbs: `up`, `run`, `attach`, `pause`, `status`, `ls`, `logs`, `jobs`, `device`, `destroy`. Schedules land with M1.
- The box contract (`.pluto.toml`): image, provision, wake, services, schedules. See ADR 0007.
- Access: SSH. The CLI is the control surface; the box is a computer.

## Principles

1. **Boring by default.** SSH, systemd, cron, local files, port-forward. Novelty is spent only on the lifecycle.
2. **Git is the floor.** Untracked disk state is a convenience that survives pauses, not a backup.
3. **No accounts, no telemetry, no control plane.** One host in v1; the network is the user's choice.
4. **Disk-only.** Processes die on pause; declared services and a short wake hook bring the box back.
5. **Deferred, with revive triggers.** Fleet, bucket, relay, MCP, and the rest live in `docs/DEFERRED.md`.

## Milestones

- **M0 — walking skeleton** (#17). One host, local disk: up, provision, run, attach, pause, wake, auto-pause, destroy, daemon re-adoption, demo script.
- **M1 — wake for work** (#38). Schedules from the contract, job history, saved devices and `pluto --device` remote control, a scheduled agent demo, remote access recipes (BYO network + SSH).
- **M2 — agent sessions** (the Orbs leg). Durable, attachable sessions in a box: `[sessions.<name>]`, tmux-owned, restarted over a pause from their own on-disk state, with an auto-pause that does not sleep work in flight and an `origin` the agent can push to. Re-cut from the earlier "fleet is devices": `pluto --device` already delivers the remote-consumer path, and export/import, a second host, and bucket portability move behind.
- **M3 — platform refresh** (#66). A reproducible, hash-locked base image built by one Go program over a single pins manifest (base digest, apt snapshot date, kernel URL+hash, Firecracker version), the guest kernel moved back inside Firecracker's support window, `[box].resources` made real (`cpus`/`memory` size the machine and its cgroup limits, `disk` sizes the rootfs), and every pin kept current by self-hosted Renovate. The honest foundation the Actions leg lands on.
- **M4 — events** (the Actions leg, #64). Triggers beyond cron: a push or a pull request on the box's origin, webhooks, and git hooks. Repo-URL provenance — a box for a GitHub repo with no local checkout — lands here.
- **M5 — coherent UX** (the human and agent interface overhaul). Make project setup,
  starting work, following it across clients, and reviewing/recovering its result feel like
  one product. The CLI grammar/output and review surface are core deliverables; structured
  agent interfaces and remote-client seams use the same vocabulary and durable identity.
  See [`docs/m5-ux-overhaul.md`](m5-ux-overhaul.md) for the brief and
  [`docs/m5-cli-overhaul.md`](m5-cli-overhaul.md) for the detailed CLI work.

## Where things live

- Decisions: `docs/adr/` · Language: `GLOSSARY.md` · Deferred: `docs/DEFERRED.md` · Research (reference only): `docs/research/` and the `research/*` branches.
