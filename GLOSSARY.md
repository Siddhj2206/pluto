# pluto

pluto runs a workspace's durable work and a GitHub job's ephemeral CI as boxes on hardware you own. One engine, two lifetimes: a durable box for a workspace, a fresh ephemeral box for each CI run.

## Language

**repo**:
A project's identity, independent of host and forge. A repo owns one or more workspaces.
_Avoid_: project, repository, origin

**workspace**:
A repo checkout on a host, identified by its path. A box is keyed to a workspace.
_Avoid_: worktree, checkout, branch, sandbox

**layer**:
A content-addressed, read-only filesystem tier a box's root is composed from. Two tiers: the base layer (OS and heavy toolchains) and the package layer (project packages).
_Avoid_: image, snapshot, cache, rootfs

**base layer**:
The layer built from `provision.base`: the OS and heavy toolchains.
_Avoid_: base image, template, flavor

**package layer**:
The layer built from `provision.packages` and the lockfiles it watches, on top of the base layer.
_Avoid_: dependency cache, cache

**volume**:
Durable per-box storage mounted outside the layer stack — the workspace at `/workspace` and the user home at `/home/pluto`. It survives idle, stop, and layer rebase.
_Avoid_: disk, mount, upper

**box**:
An isolated container that runs a workspace's durable work or one ephemeral CI run. A durable box keeps its volumes and record across runs and idle; an ephemeral box is fresh for one run.
_Avoid_: sandbox, devbox, VM, machine, environment

**durable box**:
The box owned by a workspace; its tasks share it, and its volumes persist.
_Avoid_: devbox, pet

**ephemeral box**:
A box created for a single CI run and discarded on success.
_Avoid_: runner, worker, transient

**box contract**:
A project's `pluto.yml`: the entry point for box settings and durable work, whose nodes may be inline or reference typed YAML files.
_Avoid_: config, manifest, spec, `.pluto.toml`

**provision**:
The once-per-project setup that builds the base and package layers. Changing provision affects newly created boxes; it never replaces an existing box's volumes.
_Avoid_: setup, bootstrap, build

**wake**:
The contract phase that runs every time a box starts: restart services, repair what a stop discarded. Short and idempotent.
_Avoid_: resume hook, on-boot, restore

**service**:
A long-lived task that stays running for the life of a box and is restarted on every wake.
_Avoid_: daemon, background process

**task**:
A named, durable piece of work declared in the box contract, such as `dev`. Each invocation adds a run to its history; a task runs on its workspace's durable box.
_Avoid_: job, action, command

**step**:
One part of a run. A task given as a single command has one implicit step; a task may declare several, each with its own log stream.
_Avoid_: stage, phase

**lock policy**:
How a task's runs share their durable box: `exclusive` (runs alone and waits for shared runs to drain), `shared` (runs alongside other shared work and attach sessions), or `service` (stays running).
_Avoid_: concurrency mode, mutex

**run**:
One execution of a task, recorded in its history. Repeated invocations and schedule occurrences add runs.
_Avoid_: exec, invoke, attempt

**trigger**:
Anything that runs a task: a manual `run`, a schedule, and later repository events.
_Avoid_: alarm, webhook, hook

**schedule**:
A recurring trigger in the box contract that targets a named task. Occurrences coalesce into a single pending run.
_Avoid_: cron job, timer

**frozen**:
The box state after a short idle: its processes are cgroup-frozen and its memory preserved. It thaws transparently when work arrives.
_Avoid_: paused, suspended, asleep

**stopped**:
The box state after a long idle: its container is gone and its volumes and record remain. The next work wakes it.
_Avoid_: paused, shut down, powered off

**wake_failed**:
The box state after its wake phase fails: the box is retained for inspection and no run starts.
_Avoid_: crashed, broken

**attach**:
A connection to a box's terminal that both watches output and may type into it; several clients may attach at once.
_Avoid_: session, ssh, connect

**watch**:
A read-only connection to a box's output.
_Avoid_: follow, tail, logs

**scrollback**:
The bounded buffer of recent terminal output the daemon keeps for a box so an attaching client sees history.
_Avoid_: buffer, history

**secret**:
A value declared in the box contract from a host environment variable or file, redacted from every log surface.
_Avoid_: credential, token, env

**masking**:
Redacting a secret's value from output before it is written or streamed anywhere.
_Avoid_: redaction, scrubbing

**rebase**:
Recomputing a box's base and package layers on wake. It never touches the box's volumes or its own writable layer.
_Avoid_: upgrade, refresh, rebuild

**retained**:
A failed box kept after its run for inspection. Retention is bounded by age and count unless the box is pinned.
_Avoid_: kept, saved

**pin**:
Exempting a retained box from eviction.
_Avoid_: keep, hold

**host**:
A machine running the pluto daemon and owning the boxes that live on it.
_Avoid_: node, server, machine

**host daemon**:
The single process per host that owns pluto's state — boxes, tasks, runs, schedules, and lifecycle — and serves the CLI over a unix socket.
_Avoid_: controller, server, agent

**runner**:
pluto's component that creates, starts, freezes, stops, and executes work in a box's container.
_Avoid_: orchestrator, manager, runtime, VMM

**vcs adapter**:
The boundary between the core and a version-control system. Git lives behind it; the core knows workspaces, not branches.
_Avoid_: git layer, scm

**forge adapter**:
The boundary between the core and a forge. GitHub lives behind it; the core knows repos and runs, not workflows.
_Avoid_: provider, integration

**container engine**:
The engine a box may run for its own build tooling when the contract sets `docker`. It is isolated from the host's engine.
_Avoid_: docker, podman, dind

Deferred vocabulary — `hibernate`, `lease`, `epoch`, `fork`, `relay`, `lighthouse`, `shape`, `work item`, `trust class` — describes the fleet story and is parked in [docs/DEFERRED.md](docs/DEFERRED.md).
