# pluto

pluto gives every git worktree a durable work machine: a microVM box that wakes for work, sleeps when idle, and can be entered with SSH — on hardware you own.

## Language

**box**:
A durable machine owned by one worktree: the unit that is created, provisioned, run in, paused, and destroyed. A worktree has one primary box.
_Avoid_: sandbox, devbox, VM, environment

**worktree**:
A git working tree: a checkout of a project on one branch, identified by its path. A box is keyed to a worktree.
_Avoid_: workspace, checkout

**project**:
A git repository whose worktrees each have a box.
_Avoid_: repo, repository

**box contract**:
A repository's `.pluto.toml`: its declaration of image, resources, auto-pause, provision, wake, services, jobs, and schedules.
_Avoid_: config, manifest, spec

**image**:
A versioned bootable base artifact — kernel plus rootfs — that boxes boot from.
_Avoid_: shape, flavor, template, distro

**provision**:
The contract phase that runs once per box and produces its durable disk: installs, checkouts, and setup that is too expensive to repeat. Its leftover processes are discarded.
_Avoid_: setup, bootstrap, build

**wake**:
The contract phase that runs every time a box starts: restart services, refresh tunnels and credentials, repair what a pause discarded. Short and idempotent.
_Avoid_: resume hook, on-boot, restore

**service**:
A long-lived process declared in the box contract: supervised in the box, restarted on every wake.
_Avoid_: daemon, background process

**trigger**:
Anything that wakes a box to do work: a manual `run`, a schedule, and later events such as git pushes or webhooks.
_Avoid_: alarm, webhook, hook

**schedule**:
A recurring time in the box contract that wakes a box and optionally runs a declared job. A schedule with no job is a warm-up.
_Avoid_: cron job, timer

**warm-up**:
A schedule with no job: it wakes a box so it is running before it is needed.
_Avoid_: pre-warm, boot

**job**:
A bounded command run in a box — a build, a test, an agent run — with a recorded outcome; in the box contract, the named declaration (`[jobs.<name>]`) of work that `pluto run <name>` and schedules can invoke.
_Avoid_: task, exec

**up**:
The lifecycle verb that ensures a box is running: creates it if absent, wakes it if paused. Idempotent.
_Avoid_: start, wake, open

**run**:
The lifecycle verb that runs a job in a box, ensuring the box is up first: `pluto run <name>` for a declared job, `pluto run -- <cmd>` for an ad-hoc one.
_Avoid_: exec, execute, invoke

**attach**:
The lifecycle verb that opens an interactive session in a box, ensuring it is up first.
_Avoid_: connect, enter, ssh in

**pause**:
The transition from running to paused: the machine stops cleanly and its disk stays on its host. Cheap and offline-safe.
_Avoid_: stop, shut down, suspend

**paused**:
A box that has been paused: powered off, disk on its host, woken by `up`.
_Avoid_: stopped, asleep

**auto-pause**:
The rule that pauses a box when no client is attached and no job is running for a configured idle window.
_Avoid_: idle timeout, sleep policy

**destroy**:
The lifecycle verb that removes a box: its record and its local disk. Never automatic; requires explicit confirmation.
_Avoid_: delete, remove, rm

**device**:
A saved ssh destination in the client's registry: a nickname such as `neptuno` for a machine that runs pluto, used by `pluto --device <nickname>`. The daemon knows nothing about devices.
_Avoid_: host, remote, node

**host**:
A machine running the pluto daemon and owning the boxes that live on it.
_Avoid_: node, server, machine

**host daemon**:
The single process per host that owns pluto's state: boxes, jobs, schedules, and the box lifecycle. It runs as a systemd user service with linger; the CLI talks to it over a unix socket.
_Avoid_: controller, server, agent

**runner**:
pluto's component that boots and manages a box on a host by driving a VMM: it prepares the disk, launches the process, wires vsock, and performs lifecycle operations.
_Avoid_: orchestrator, manager, runtime

**VMM**:
The external hypervisor process that runs a box, such as Firecracker or Cloud Hypervisor. It sits behind the runner's interface and is not part of pluto.
_Avoid_: hypervisor, emulator

**guest agent**:
pluto's process inside a box, talking to the host daemon over vsock: it applies the box contract, runs jobs, reports status, and stops the box cleanly.
_Avoid_: in-box daemon, sidecar

Deferred vocabulary — `hibernate`, `lease`, `epoch`, `fork`, `relay`, `lighthouse`, `shape` — describes the fleet story and is parked in [docs/DEFERRED.md](docs/DEFERRED.md).
