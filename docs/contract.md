# The box contract reference

A repository's `.pluto.toml` declares the box for its worktrees: the image, the
once-per-box provision, the per-wake repair, long-lived services, named jobs,
long-lived attachable sessions, and recurring schedules. The daemon parses it
on the host — where the worktree lives — and sends it to the box's agent; the
box never parses TOML.

[ADR 0007](adr/0007-box-contract.md) is the decision record; this page is the
field-by-field reference. Editors can use the generated schema with a single
line at the top of the file:

```toml
#:schema https://raw.githubusercontent.com/Siddhj2206/pluto/main/pluto.schema.json
```

The contract is read when a box boots (the handoff) and its hash is recorded,
so `pluto status` can flag a worktree whose contract changed since the box
applied it. After an edit, pause and wake the box to apply it:

```sh
pluto pause <box> && pluto up
```

Unknown keys are rejected at parse time, so a typo fails the handoff instead
of silently doing nothing. A missing `.pluto.toml` is not an error: the box
gets no phases, services, jobs, sessions, or schedules.

## A full example

```toml
[box]
resources = { cpus = 4, memory = "8GiB", disk = "40GiB" }
auto_pause = "1h"

[env]
NODE_ENV = "development"

[provision]
command = ".pluto/provision.sh"
timeout = "20m"

[wake]
command = ".pluto/wake.sh"
timeout = "30s"

[jobs.dev]
description = "start the dev server"
command = ["pnpm", "dev"]
dir = "web"

[jobs.test]
description = "run the test suite"
command = "pnpm test"
timeout = "30m"

[services.web]
description = "web UI"
command = "pnpm dev"
port = 3000

[sessions.agent]
description = "the coding agent"
command = "opencode"

[[schedule]]
name = "nightly"
cron = "0 2 * * *"
job = "test"
```

## Shared rules

These apply wherever a section takes a command:

- **`command`** — a string runs through `/bin/sh -c`; an array is exec'd
  directly, with no shell. A shell-string command can reference the merged env
  through the shell; an argv command receives literal values. Every section
  uses the key `command`.
- **`dir`** — optional everywhere; defaults to the in-box worktree root and a
  relative value resolves against it. Commands that reference repo files must
  point at committed files, because the box receives the committed tree
  ([ADR 0008](adr/0008-sync-is-git-once.md)).
- **`env`** — a flat `KEY = "value"` table. The top-level `[env]` applies to
  provision, wake, services, jobs, sessions, and ad-hoc `pluto run --`; every
  entity's own `env` merges over it per key. Values are literal — there is no
  interpolation — and `PLUTO_` is reserved. M1 sets only `PLUTO_WORKTREE`
  inside the box, to the worktree root.
- **`description`** — allowed on `[jobs.<name>]`, `[services.<name>]`, and
  `[sessions.<name>]` only. A job's description appears in the no-arg
  `pluto run` listing and in unknown-job errors; a service's appends to its
  `pluto status` row; a session's describes it in `pluto status` and in
  unknown-session errors.
- **names** — job, service, and session names match
  `^[A-Za-z0-9][A-Za-z0-9_-]*$`.

## `[box]`

| Key | Type | Meaning |
|---|---|---|
| `image` | string | The base image version to boot, exactly as printed by `pluto image ls` (content-derived, e.g. `fe2ff2088c425d24`). Empty (the default) pins the newest imported image. A box pins the version it first booted with. |
| `resources` | table | Declared machine size: `cpus` (integer), `memory` (string like `"8GiB"`), `disk` (string like `"40GiB"`). Parsed and schema-checked; the M1 runner does not size boxes from it yet. |
| `auto_pause` | string | The idle window — a Go duration such as `"30m"` or `"1h"` — before the daemon pauses a box with no client attached and no job running. `"off"` disables auto-pause. Empty means the default, `1h`. |

## `[env]`

```toml
[env]
NODE_ENV = "development"

[jobs.dev]
command = "pnpm dev"
env = { NODE_ENV = "staging" }   # this job sees staging
```

See the shared `env` rules above.

## `[provision]`

Runs **once per box**, produces its durable disk, and may install anything —
tools fetched with `curl`, a package manager's user-level install, a repo
script. Its leftover processes are discarded, so anything long-lived must be a
service. `command` is required.

| Key | Type | Meaning |
|---|---|---|
| `command` | string or array | Required. The setup command. |
| `dir` | string | Working directory; defaults to the worktree root. |
| `env` | table | Merged over the top-level `[env]`. |
| `timeout` | string | Timebox; default `"20m"`. |

A failed provision still boots the box, marked failed: the machine itself is
the debugging surface (`pluto status`, `pluto logs <box> --phase provision`).

## `[wake]`

Runs **every time the box starts**: restart services, refresh tunnels and
credentials, repair what a pause discarded. Keep it short and idempotent.
`command` is required.

| Key | Type | Meaning |
|---|---|---|
| `command` | string or array | Required. The per-start repair command. |
| `dir` | string | Working directory; defaults to the worktree root. |
| `env` | table | Merged over the top-level `[env]`. |
| `timeout` | string | Timebox; default `"30s"`. |

A failing or slow wake does not block the box; its outcome shows in
`pluto status` and `pluto logs <box> --phase wake`.

## `[jobs.<name>]`

A named, bounded command: what `pluto run <name>` runs and what a schedule
references. No-arg `pluto run` lists the declared jobs with their
descriptions. `pluto jobs` is the run-history command; `[jobs.<name>]`
declares what can run.

| Key | Type | Meaning |
|---|---|---|
| `description` | string | Optional; shown in the `pluto run` listing and unknown-job errors. |
| `command` | string or array | Required. |
| `dir` | string | Working directory; defaults to the worktree root. |
| `env` | table | Merged over the top-level `[env]`. |
| `timeout` | string | Optional timebox, e.g. `"30m"`. Unlimited when omitted; a trip records a failed job. |

```sh
pluto run test          # run the 'test' job in the current worktree's box
pluto run mybox test    # run it in another worktree's box
pluto run -- pnpm test  # a one-off command, under the top-level env
```

## `[services.<name>]`

A long-lived process: supervised in the box and restarted on every wake.

| Key | Type | Meaning |
|---|---|---|
| `description` | string | Optional; appended to the service's `pluto status` row. |
| `command` | string or array | Required. |
| `dir` | string | Working directory; defaults to the worktree root. |
| `env` | table | Merged over the top-level `[env]`. |
| `port` | integer | Optional; the port the service listens on, `0`–`65535`. Surfaced in `pluto status`; reaching it from outside uses the `ssh -L` recipe in [remote-access.md](remote-access.md). |

## `[sessions.<name>]`

A long-lived interactive command run under tmux, so a client can detach and
reattach without ending it ([ADR 0010](adr/0010-sessions-are-tmux.md)). An
agent is whatever command a session runs; pluto integrates with no agent. A
session is neither a job (no bounded outcome, no history) nor a service
(services are non-interactive and may take a `port`).

| Key | Type | Meaning |
|---|---|---|
| `description` | string | Optional; describes the session in `pluto status` and unknown-session errors. |
| `command` | string or array | Required. |
| `dir` | string | Working directory; defaults to the worktree root. |
| `env` | table | Merged over the top-level `[env]`. |

Sessions are restarted on every wake and killed by pause: durability is
disk-only ([ADR 0002](adr/0002-box-lifecycle.md)), so a session's program
resumes from its own on-disk state. Attach with
`pluto attach <box> --session <name>`; bare `pluto attach` still opens a plain
shell. Duplicate `[sessions.<name>]` tables, names outside the shared rule, and
a missing or malformed `command` fail at parse time with `file:line`.

## `[[schedule]]`

A recurring time that wakes a box and optionally runs a declared job. Entries
are an array of tables:

```toml
[[schedule]]
name = "nightly"
cron = "0 2 * * *"   # every day at 02:00 UTC
job = "test"

[[schedule]]
name = "warm-up"
cron = "30 8 * * 1-5"  # weekdays at 08:30 UTC, no job
```

| Key | Type | Meaning |
|---|---|---|
| `name` | string | Required; unique among the schedules. |
| `cron` | string | Required. Five fields — minute, hour, day of month, month, day of week — minute resolution, UTC. |
| `job` | string | Optional; must name a declared `[jobs.<name>]`. A schedule without one is a **warm-up**: it only ensures the box is running. |

Schedule semantics follow the durable-alarm decision
([ADR 0003](adr/0003-triggers-are-durable-alarms.md)): schedules are stored
with the box record, so they survive daemon restarts and host reboots; a
trigger wakes a paused box, runs its job, and records the outcome in job
history; missed firings coalesce into one late run; and a box that is already
running a job skips the occurrence. Schedules never carry inline commands,
and M1 has no timezone field.

## Errors and the schema

A contract failure names the file and the line, because the fix is an edit
there:

```
pluto: /home/me/src/app/.pluto.toml:12: unexpected EOF; expected value
next: fix the contract and run 'pluto run' again
```

`pluto.schema.json` at the repo root is generated from the `internal/contract`
Go types — the single source of truth — with `go generate ./...`, and CI fails
when regeneration leaves the tree dirty. Add the `#:schema` comment shown
above to get editor completion and validation for the fields on this page.
