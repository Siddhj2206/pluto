# The box contract reference

A repository's `.pluto.toml` declares the box for its worktrees: the image, the
declarative tools, the once-per-box provision, the per-wake repair, long-lived
services, named jobs, long-lived attachable sessions, and recurring schedules.
The daemon parses it on the host — where the worktree lives — and sends it to
the box's agent; the box never parses TOML.

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

[tools]
packages = ["build-essential", "curl", "git"]

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
| `resources` | table | Declared machine size: `cpus` (integer vCPU count), `memory` (binary size string like `"8GiB"`), `disk` (string like `"40GiB"`). All three are real at creation ([sizing a box](#sizing-a-box)): `cpus` and `memory` size the Firecracker machine and its cgroups, `disk` sizes the rootfs. |
| `auto_pause` | string | The idle window — a Go duration such as `"30m"` or `"1h"` — before the daemon pauses a box with no client attached and no job running. `"off"` disables auto-pause. Empty means the default, `1h`. |

## Sizing a box

`[box].resources.cpus`, `[box].resources.memory`, and
`[box].resources.disk` are real at box **creation**: `cpus` and `memory` set
the Firecracker machine config and are enforced as cgroup CPU and memory
limits; `disk` sizes the box's rootfs.

```toml
[box]
resources = { cpus = 4, memory = "8GiB", disk = "40GiB" }
```

- **`cpus`** is the vCPU count. **`memory`** is a binary size: a number and a
  power-of-two unit, e.g. `"512MiB"`, `"8GiB"`, `"2G"`. Units are binary
  throughout — `1G` and `1GiB` are both 1024 MiB — and the size must be at
  least 1 MiB. A missing or empty field, or a contract with no
  `[box].resources` at all, keeps pluto's defaults: **2 vCPU / 1024 MiB**.
- **`disk`** is the rootfs size, in the same binary grammar (`"40GiB"`,
  `"2G"`). On first boot the runner clones the base image and grows the clone
  to this size (`truncate` then `resize2fs`); the grown disk is persisted on
  the host, so it is the box's disk from then on. A missing or empty `disk`,
  or a contract with no `[box].resources`, keeps the base image's size — the
  default. A declared size below the base image is refused rather than
  silently ignored: shrinking a rootfs is not supported.
- The machine config and the disk size are both written when the box first
  boots. The runner also writes a per-instance systemd drop-in
  (`pluto-box@<id>.service.d/resources.conf`) with `MemoryMax` and `CPUQuota`,
  so the box's own service cgroup cannot use more CPU or memory than declared.
  `CPUQuota` is the declared vCPU count as a percentage of one CPU
  (`cpus=4` → `400%`). `MemoryMax` is **not** the declared RAM verbatim: the
  cgroup also holds the Firecracker VMM and the page tables it maps for the
  guest, so a cap set to the guest size alone lets the VMM's own footprint
  push the cgroup over the limit and OOM-kill the box. The cap is the declared
  RAM plus headroom — the larger of 256 MiB and one-eighth of the declared RAM
  (`memory="8GiB"` → `MemoryMax=9216M`). This is the rootless path: systemd
  already owns the cgroup, so pluto never writes cgroupfs by hand.
- **Changing `[box].resources` on an existing box does nothing.** Sizing —
  including the disk — is frozen when the box first starts and is not resized
  in place: there is no live resize, and a disk that already exists is never
  grown. `pluto status` will flag the edited contract as changed, but applying
  it cannot resize the running machine or its rootfs. To resize, destroy and
  recreate the box:

  ```sh
  pluto destroy <box> --yes && pluto up
  ```

  This is deliberate, and consistent with the read-once stance of the box
  lifecycle ([ADR 0002](adr/0002-box-lifecycle.md)).

## `[env]`

```toml
[env]
NODE_ENV = "development"

[jobs.dev]
command = "pnpm dev"
env = { NODE_ENV = "staging" }   # this job sees staging
```

See the shared `env` rules above.

## Pushing from the box

After the first-boot clone the box mirrors every remote in the host worktree
— names, fetch URLs, distinct push URLs, and the default fetch refspec — so
`git fetch` and `git push` work from inside the box
([ADR 0008](adr/0008-sync-is-git-once.md)). The checked-out branch tracks
`origin`, or the sole remote when there is no `origin`; when several remotes
exist and none is `origin`, the branch is left untracked and `pluto` warns.
`push.default` is set to `current`, so a bare `git push` works with a single
remote, for published and unpublished branches alike. A worktree with no
remotes yields a local-only box: `pluto status` reports `none (local-only)`
and `pluto up` warns once that pushing is unavailable. Remotes are read on
first boot only.

pluto stores no credentials and adds no secret handling. For a private HTTPS
remote, supply a token through the top-level `[env]` and have `provision`
wire it into git with a credential helper or `url.insteadOf`:

```toml
[env]
GITHUB_TOKEN = "ghp_..."   # the user's token, not pluto's

[provision]
command = '''
git config --global url."https://x-access-token:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"
'''
```

`.pluto.toml` need not be committed, so the token can sit in a local,
uncommitted contract. `[env]` reaches provision, wake, services, and jobs.
An SSH (`ssh://` or `git@`) remote is mirrored but pushing over it is not
wired this milestone; `pluto status` and `pluto up` say so. pluto never runs
the push; a push that git rejects for want of a credential fails with git's
own message inside the box.

## `[tools]`

The declarative half of packaging: the apt packages a box installs **before**
its `[provision]` command, so a project does not hand-write `apt-get install`.
The box uses its own apt — no mise, Nix, or devbox is installed or required
([research](research/project-packaging.md)).

```toml
[tools]
packages = ["build-essential", "curl", "git", "pkg-config"]
```

| Key | Type | Meaning |
|---|---|---|
| `packages` | array | Required when `[tools]` is present. Apt package names (strings) or `{ name, version }` tables. |

A bare name installs whatever the box's apt index resolves; a `version` pins an
exact apt version. Exact pins render as apt's `name=version`:

```toml
[tools]
packages = [
  "curl",
  { name = "nodejs", version = "22.11.0" },
]
```

`[tools]` composes with `[provision]` ([ADR 0007](adr/0007-box-contract.md)).
The generated preamble
`sudo -n apt-get update && sudo -n apt-get install -y <packages>` runs first,
then the declared `[provision] command`; a failed install stops the sequence.
The box is single-user: the `dev` user has passwordless sudo
(`/etc/sudoers.d/dev`), so the apt preamble elevates through it while the
declared `[provision]` command still runs as `dev`, unprivileged. The host
stays rootless. With only `[tools]`, the install is the whole provision; with
only `[provision]`, behavior is unchanged. Anything apt cannot install —
`cargo install`, `npm install -g`, a repo script — stays in `[provision]`.

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
| `cache` | bool | Opt into a host-managed, reusable environment layer. Off by default. |
| `share_untrusted` | bool | Publish the cached layer to a secret-free scope untrusted work-item boxes may consume. Requires `cache`. |

A failed provision still boots the box, marked failed: the machine itself is
the debugging surface (`pluto status`, `pluto logs <box> --phase provision`).

### Reusable environment layers

With `cache = true`, the daemon fingerprints the declared setup — the base
image, `[tools]`, and `[provision]` — and content-addresses it by project,
setup, image, and trust class. The first box to provision a missing layer
publishes its scrubbed disk; later boxes with the same fingerprint clone that
layer and skip the full setup. A cached provision may not declare `[env]` or
`[provision].env`: those values would be captured in the shared layer, so the
contract is refused at parse time.

Layers are never shared across trust classes. A trusted box publishes and
consumes trusted layers; an untrusted work-item box (an unlabeled pull request)
resolves to a separate untrusted class and never publishes. A trusted box may
opt into the secret-free scope with `share_untrusted = true`, publishing its
scrubbed layer to the untrusted class so untrusted boxes can reuse it. The
scrub removes the worktree, agent state, logs, jobs, sessions, shell history,
and ssh identity before the layer becomes visible.

```toml
[provision]
command = "make setup"
cache = true
share_untrusted = true
```

A layer is published only after a successful provision on a cleanly stopped
box. The layer key is resolved once, when the box's disk is created, and frozen
on the box record, so editing the contract afterwards never moves an existing
disk's key. Coordination is host-local and in-memory: a concurrent miss waits
for the builder, and a build claim expires after a bounded lease so a builder
that never publishes cannot starve its peers.

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
with the box record, so they survive daemon restarts and host reboots. Each
cron occurrence creates a durable task and run, including warm-ups; missed
occurrences are materialized one at a time after restart. A busy box leaves
the run queued until it can execute. The occurrence timestamp is the
idempotency key, so a daemon retry cannot create a second task for that same
occurrence. Schedules never carry inline commands, and M1 has no timezone
field.

## `[events.push]`

Push policy is read from the repository's trusted default branch. It selects
one declared job, run when a commit lands on the registered box's branch:

```toml
[events.push]
job = "test"
```

The push is admitted only when its ref matches the registered box's branch and
it is not a deletion; a GitHub push with an `action` field is ignored. The
pushed commit is fetched on the host before the job is queued. The registered
branch box is advanced to the pushed commit before the job runs, preserving
local work: a dirty source worktree or a target that is not a fast-forward of
the box's current ref blocks the update visibly instead of running stale
content. The job receives the pushed ref and commit through the
`PLUTO_EVENT_*` environment variables described under `[events.generic]`.
GitHub `X-GitHub-Delivery` identifies a run for idempotency. The repository
branch has one durable `github` task; each new delivery appends a serialized
run to it. A repeated delivery returns the original run. Pluto checks that the
exact trusted default-branch contract revision remains approved both when it
admits the event and immediately before the job runs.

## `[events.pull_request]`

PR event policy is read from the repository's trusted default branch. It
selects one declared job and explicitly lists accepted GitHub pull request
actions. Job execution uses the contract from the triggering PR head. By
default, a PR receives no host-held credentials. A maintainer can mark a PR
with the configured label (default `pluto:trusted`); only then may the trusted
default-branch policy allow named host environment variables to that job:

```toml
[events.pull_request]
job = "check"
actions = ["opened", "synchronize"]
trusted_label = "pluto:trusted"
credentials = ["PRIVATE_PACKAGE_TOKEN"]
```

Credential values come from the daemon's environment when the job starts. The
queue stores only the allowlisted variable names, never their values. A
missing host value fails the job closed. The PR's own contract cannot add
credential names or change the trusted label or action policy.
One durable task belongs to each repository pull request. A new qualifying
delivery appends a serialized run; duplicate delivery IDs do not append one.
The event's trust decision remains separate: an unlabeled pull request stays
untrusted and receives no host-held credentials.

## `[events.issue]`

Issue event policy is read from the repository's trusted default branch. It
selects one declared job and explicitly lists accepted GitHub issue actions.
Issue boxes start from that default branch on a dedicated `pluto/issue-<number>`
branch and are reused for later accepted actions on the same issue. Named host
environment values can be provided to the declared job at execution time:

```toml
[events.issue]
job = "implement"
actions = ["opened", "labeled"]
credentials = ["ISSUE_AUTOMATION_TOKEN"]
```

Only the credential names are recorded in the durable queue. Values come from
the daemon environment and are never stored by Pluto.
One durable task belongs to each repository issue, and later qualifying
deliveries append serialized runs to it. Delivery IDs deduplicate retries.

## `[events.generic.<event-type>]`

Generic webhook policy is read from the registered repository's trusted
default branch. Each event type selects one declared job. `actions` is an
optional allowlist; omit it to accept any action for that event type:

```toml
[events.generic.build]
job = "test"
actions = ["completed"]
```

Configure a source with `pluto daemon --webhook-listen :8787
--generic-webhook ci,<box-id>,CI_WEBHOOK_SECRET`. Repeat `--generic-webhook`
for additional sources; each names the environment variable that holds that
source's shared secret. Send
JSON to `/generic/ci` with `X-Pluto-Event`, optional `X-Pluto-Action`, and
optional stable `X-Pluto-Event-ID` headers. Sign the exact body using HMAC-SHA256
with the shared secret over `<unix-timestamp>.<body>` and send
`X-Pluto-Timestamp` plus `X-Pluto-Signature-256: sha256=<hex-digest>`. Timestamps
must be within five minutes. Keep the endpoint behind a user-managed HTTPS
proxy. The queue retains the event payload and stable ID; the job receives the
payload as `PLUTO_EVENT_PAYLOAD`. A stable `X-Pluto-Event-ID` is required. Each
unique signed generic delivery creates one `webhook` task; its retry resolves
to the same run.

## Guided GitHub setup

Run `pluto setup github` inside the repository to select supported GitHub
events (`push`, `pull_request`, and `issues`) and map each selected event to a
declared job. The command writes the corresponding `[events.*]` sections to
`.pluto.toml`; it does not expose the task/control API. Configure the GitHub
webhook on the registered host listener as described in
[`webhooks.md`](webhooks.md). Approve the exact resulting contract revision
through the local daemon contract-trust API before unattended event work.

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
