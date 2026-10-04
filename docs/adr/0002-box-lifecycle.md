# Pause is the only dormant state in v1; durability is git plus the host disk

A box's disk lives on its host. `pause` stops the machine cleanly and nothing leaves the host; `up` wakes it again. The durability contract is deliberately modest: disk state is as of the last graceful stop, a crash may lose recent writes, and **git is the floor** — anything not pushed does not exist. Nothing uploads in v1: `hibernate` (seal to a bucket and release for another host), `fork`, and `snapshot` are deferred with revive triggers in `docs/DEFERRED.md`. Box identity is an immutable uuid per incarnation; there is no fleet ownership to track.

Auto-pause uses one rule: no client attached and no job running for an idle window (default 60 minutes, overridable per box) → pause. The busy-signal composition from research (inhibitors, agent APIs, cgroups, tmux) is deferred until this rule misbehaves.

## Considered options

- **Hibernate at M0** (the original plan): pulls the bucket, leases, and chunking onto the critical path before a second host exists.
- **Checkpoint at every pause**: every pause reads and hashes the whole disk and needs a store reachable; makes offline pause impossible.
- **Keeping boxes running between jobs**: wastes RAM/CPU on a personal host and erodes the wake/sleep contract.

## Consequences

- Host loss loses paused boxes; git is the recovery path until export lands.
- `resume` is an internal transition, not a verb; `up` and `attach` imply it.
- Public verbs are `up`, `run`, `attach`, `pause`, `status`, `ls`, `logs`, `destroy`.
