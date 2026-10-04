# pluto

pluto is a personal, self-hosted fleet of durable machines: microVM boxes keyed by git worktree, paused disk-only, with state in a bucket the owner controls.

## Language

**box**:
A durable machine owned by one worktree: the unit that is created, built, leased, paused, hibernated, forked, and destroyed. A worktree has one primary box (key `project/branch`) plus any fork instances (`project/branch#2`, …).
_Avoid_: sandbox, devbox, VM, environment

**worktree**:
The (project, branch) pair a box is keyed to; fork instances share the pair under a `#n` suffix.
_Avoid_: workspace, checkout

**project**:
A git repository whose branches each have boxes.
_Avoid_: repo, repository

**runner**:
pluto's component that boots and manages a box on a host by driving a VMM: it prepares the disk, launches the process, wires vsock and networking, and performs lifecycle operations.
_Avoid_: orchestrator, manager, runtime

**VMM**:
The external hypervisor process that runs a box, such as Firecracker or Cloud Hypervisor. It sits behind the runner's interface and is not part of pluto.
_Avoid_: hypervisor, emulator

**up**:
The lifecycle verb that ensures a box is running: it creates the box if absent, resumes it if paused, or wakes it if hibernated. Idempotent.
_Avoid_: start, wake, open

**pause**:
The transition from running to paused: the box's machine stops cleanly and its disk stays local to the host. Cheap and offline-safe; the box keeps its lease and resumes only there.
_Avoid_: stop, shut down, suspend

**hibernate**:
The transition that seals a box's disk into the bucket and releases its lease, making the box claimable from any host. Disk-only — no memory is preserved — and the only durability boundary: nothing between hibernations leaves the host.
_Avoid_: snapshot, archive, save

**resume**:
The transition from paused to running, performed by `up`; not a public verb.
_Avoid_: start, boot

**fork**:
The lifecycle verb that creates a new box instance from a paused or hibernated box: a faithful clone — same branch, same working tree — sharing disk lineage copy-on-write.
_Avoid_: clone, copy, branch

**destroy**:
The lifecycle verb that removes a box: its record, its local disk, and its state pointer. Never automatic, and requires explicit confirmation.
_Avoid_: delete, remove, rm

**paused**:
A box that has been paused: powered off, disk local to its host, lease held, resumable only there.
_Avoid_: stopped, asleep, suspended

**hibernated**:
A box whose disk is committed to the bucket and whose lease is free: claimable and resumable from any host.
_Avoid_: archived, sealed, stored

**trigger**:
A durable activation request on a box: ensure running, optionally run a command, record the outcome. Manual, schedule, and connection are its kinds; git events are planned.
_Avoid_: alarm, webhook, hook

**schedule**:
A recurring trigger with cron-style timing and an optional command; it exists whether or not the box is awake. Without a command it is a warm-up.
_Avoid_: cron job, timer

**warm-up**:
A schedule with no command: it wakes a box so it is running before it is needed.
_Avoid_: pre-warm, boot

**host**:
A machine running the pluto daemon. Hosts renew a lease while alive and own the boxes they run; a box moves between hosts only through hibernate and claim.
_Avoid_: node, server, machine

**lease**:
Ownership of a box by one host, recorded in the bucket and renewed while the host lives. When a host's lease expires, another host may claim its hibernated boxes.
_Avoid_: lock, mutex, session

**epoch**:
A per-incarnation counter that advances with every activation. All mutable box state is written under the epoch that produced it, so a superseded writer's output is ignored.
_Avoid_: version, revision
