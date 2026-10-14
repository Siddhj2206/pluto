# pluto — vision

**pluto gives every workspace a durable work machine: a container box that
wakes for work, sleeps when idle, and can be entered with a shared terminal —
on hardware you own.**

## The job

Developers run long work — agents, builds, tests — tied to a laptop that
sleeps. Cloud sandboxes solve that by owning your machine and your account.
pluto solves it on hardware you own, with no accounts, no control plane, and no
telemetry.

Three uses, one engine:

- **Work that outlives the laptop.** A task runs in the workspace's box; close
  the laptop; come back and attach to the machine, not a chat log.
- **Scheduled work with a place to land.** A `pluto.yml` schedule wakes the box,
  runs a declared task, and records the run. When something fails you enter the
  box that failed instead of reading logs.
- **Warm environments, dev and CI.** A content-addressed base layer and a
  package layer keyed on the project's lockfiles are reused by the durable box
  and by every ephemeral CI box, so setup is a cache hit in both modes.

## Where pluto sits

pluto is the self-hosted intersection of three tools:

- **mise** — per-project dev experience: one config for tools, env, and tasks.
  pluto's `pluto.yml` is mise's config for a machine instead of a shell.
- **Cloud agents** — agents that work on a machine of their own and keep going
  after the laptop closes. pluto's tasks and `attach` are the self-hosted
  version.
- **GitHub Actions** — work that wakes on a schedule or an event and lands
  somewhere you can inspect. pluto's schedules, and later its runner worker, are
  the self-hosted execution path.

The position is the intersection: mise's dev experience, a durable remote
agent, and triggered work — on hardware you own, with no accounts, no control
plane, and no telemetry.

## The claim

The novelty is the lifecycle, not the storage: a machine that belongs to a
workspace, wakes for work, sleeps when done, and can be entered. It is
agent-agnostic by construction — an agent is a task, not an integration.

Not claimed: memory snapshots; cross-host handoff; isolation or security
guarantees; a hosted service; agent-specific APIs; content-addressed
deduplication.

## Public shape

- **One contract.** A project declares everything in a single `pluto.yml`:
  provision, wake, the box's container-engine capability, secrets, named tasks,
  and schedules. Any node may be inline or a path to a typed YAML file. See
  [docs/pluto-yml.md](pluto-yml.md).
- **Verbs.** `box` (`ls`, `status`, `create`, `rm`, `pin`, `unpin`), `run`,
  `ci`, `attach`, `ls runs`, `show`, `logs`, `device`, `provider`, `init`,
  `daemon`.
- **Access.** The CLI talks to the host daemon over a unix socket; the daemon
  opens no ports. From another machine, `pluto --device` re-runs the CLI over
  SSH. The box's terminal is daemon-owned and shared.

## Principles

1. **Boring by default.** Containers, git, cron, local files. Novelty is spent
   only on the lifecycle.
2. **Git is the floor.** Durable volumes are a convenience that survives runs
   and idle, not a backup.
3. **No accounts, no telemetry, no control plane.** One host; the network is the
   user's choice.
4. **Layers and volumes.** A box's root is an overlay of content-addressed
   layers; durable state lives in volumes outside that stack, so a wake can
   rebase onto new layers without losing work.
5. **One engine for durable work and ephemeral CI.** A workspace's durable box
   and a CI job's fresh box are the same engine, seeded from the same package
   layer.

## Where things live

- Contract: `docs/pluto-yml.md` · Runs: `docs/runs.md` · Remote: `docs/remote-access.md`
- Decisions: `docs/adr/` · Vocabulary: `GLOSSARY.md`
- Research (reference only): `docs/research/` and the `research/*` branches.
