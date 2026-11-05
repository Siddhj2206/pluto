# pluto

Pluto turns machines you own into a place to run work. This is the language of the rewrite. Add a term when a real decision needs it.

## Language

**host**:
A machine running Pluto. It owns its runs, boxes, caches, secrets, and its own list of people and devices.
_Avoid_: server, node, daemon

**device**:
One installation of the Pluto client, with its own key. A person may have several.
_Avoid_: client, machine

**person**:
A human allowed to use a host through one or more devices.
_Avoid_: user, account, member

**repo**:
A git repository known to a host, identified by its remote URL.
_Avoid_: project, workspace

**task**:
A named piece of work declared in a repo's optional `.pluto/` directory.
_Avoid_: job, action

**job**:
A CI job as the git host defines it. Pluto runs it but doesn't define it.
_Avoid_: task

**schedule**:
A task plus the times it should run.
_Avoid_: cron job

**run**:
One execution of a task, a command or a job, with its logs, state and timing.
_Avoid_: execution, attempt

**box**:
The Linux container a run executes in. Fresh by default, and kept only when asked.
_Avoid_: sandbox, VM, environment

**cache**:
Reusable files kept outside a box so a fresh box still starts warm.
_Avoid_: layer, snapshot

**secret**:
A named value stored on a host and given to boxes by name.
_Avoid_: credential, token

**route**:
One way for a device to reach a host. A host can have several, and the device picks one that works.
_Avoid_: connection, tunnel

**join token**:
A long, one-time value, pasted or scanned, that lets a device join a host.
_Avoid_: pairing code, invite code
