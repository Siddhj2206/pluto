# pluto — vision

**pluto gives every git worktree a durable work machine: a microVM box that wakes for work, sleeps when idle, and can be entered with SSH — on hardware you own.**

## The job

Developers run long work — agents, builds, tests — tied to a laptop that sleeps. Cloud sandboxes solve that by owning your machine and your account. pluto solves it on hardware you own, with no accounts, no control plane, and no telemetry.

Three uses, one mechanism:

- **Agent runs that outlive the laptop.** A coding agent runs in the branch's box; close the laptop; come back and attach to the machine, not a chat log.
- **Scheduled work with a place to land.** Nightly builds, tests, migrations, agent sweeps wake on a schedule. When something fails you enter the machine that failed instead of reading logs.
- **Warm branch environments.** Every branch keeps its installed dependencies, running services, and databases between sessions.

## The claim

The novelty is the lifecycle, not the storage: a machine that belongs to a branch, wakes for work, sleeps when done, and can be entered. It is agent-agnostic by construction — an agent is a job or a service, not an integration.

Not claimed: memory snapshots; cross-host handoff in v1; isolation or security guarantees; a hosted service; agent-specific APIs; content-addressed deduplication.

The demo that proves it: a schedule fires, an agent works, the machine sleeps; you attach to the machine that did the work. Numbers reported at M0: time to first shell, pause/wake latency, provision duration.

## Public shape

- Verbs: `up`, `run`, `attach`, `pause`, `status`, `ls`, `logs`, `destroy`. Schedules land with M1.
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
- **M1 — wake for work.** Schedules from the contract, job history, a scheduled agent demo, remote access recipes (BYO network + SSH).
- **M2 — own the fleet.** Export/import, a second host, then bucket-backed portability if it earns its keep.

## Where things live

- Decisions: `docs/adr/` · Language: `GLOSSARY.md` · Deferred: `docs/DEFERRED.md` · Research (reference only): `docs/research/` and the `research/*` branches.
