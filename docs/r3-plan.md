# Review 3 plan: observable automation, durable onboarding, managed worktrees

**Status:** draft for discussion. Not a decision of record.
**Branch:** `demo/windows-client`
**Scope:** one implementation slice on this branch. No new milestone.

## 1. What review 3 has to prove

Review 2 showed a box and a job. It did not show the system thinking: why a
box woke, which trigger admitted work, where it ran, what it waited on, or why
it failed. The next review should show the automated side end to end:

1. A Windows client connects to a Linux host and can explain the connection.
2. From the laptop checkout, `pluto --device host up` creates or resumes the
   branch's worktree and box without a host path or box ID. `pause`, `run`,
   `status`, `attach`, and `logs` resolve the same target.
3. A schedule fires while the box is paused, wakes it, runs a job, and leaves a
   durable result.
4. A real GitHub issue event, delivered by webhook to the server, starts an
   agent job; the job opens a draft PR; a PR event runs tests and posts the
   result back as a PR comment. The client-to-server leg stays LAN-only.
5. Every step answers four questions: **what happened, where, when, and why**.
6. The same operations are visible from Windows, not only from a host shell.

A successful review is not "the terminal printed something". It is a
causal record a person can replay without asking the presenter what happened.

## 2. Current implementation audit

### 2.1 Observability today

What exists:

- `pluto status <box>`: state, image, phases, services, sessions, latest job,
  auto-pause, timestamps.
- `pluto jobs <box>`: last 20 job summaries.
- `pluto queue`: durable queue items with source, state, reason.
- `pluto task ls|show`, `pluto run ls|show`: durable tasks and runs.
- `pluto logs`: phase, service, or job output.
- `GET /v1/boxes/{id}/metrics`: raw Firecracker metrics JSON.
- Daemon notices (`Logf`) go to stderr, which systemd captures in the journal.

What is missing:

- **No host-level view.** There is no command that shows the daemon, all
  boxes, capacity, queue depth, active jobs, and upcoming schedules together.
- **No durable activity record.** Job and task records are state, not history.
  A pause, wake, schedule skip, webhook rejection, queue block, or auto-pause
  decision leaves no queryable trace. Most of these only reach journald as
  unstructured text.
- **No causal identity.** A job does not record whether it came from a manual
  run, a schedule, a webhook, or a task. A queue item knows its source, but the
  resulting job does not. There is no trace that joins delivery -> task -> run
  -> queue item -> job -> log.
- **No "where".** A job knows its box, but the event does not carry host,
  project, branch, worktree, task, run, schedule, or delivery in one record.
- **No client identity.** `--device` re-executes over SSH; the daemon sees
  only a unix-socket request. It cannot say which device asked.
- **No schedule surface.** Schedules are stored on the box record, but there
  is no `schedule ls`, no next-due time, no last outcome, no run-now.
- **No webhook surface.** Webhook sources are daemon flags. Rejected
  deliveries (bad signature, unapproved contract, ignored event, queue full)
  are HTTP errors with no durable record. `pluto setup github` tells the user
  to "approve this exact contract revision through the local daemon
  contract-trust API", but there is no CLI command for that.
- **No health/overview command.** `GET /v1/health` exists; the CLI never
  calls it. Daemon uptime, state directory, listener, and version are not
  shown together.
- **Metrics are not surfaced.** The file exists and the endpoint exists, but
  no CLI reads it, and there is no host or box resource view.

### 2.2 Device onboarding today

`pluto device add <nickname> <user@host>`:

1. Writes the nickname to `~/.config/pluto/devices.toml` or
   `%AppData%\pluto\devices.toml` *before* checking anything.
2. Runs `ssh` with `BatchMode=yes`, `ConnectTimeout`, and
   `StrictHostKeyChecking=accept-new`.
3. On an auth failure, offers a password prompt and installs the default
   public key through `ssh`, with the key on stdin.
4. Finds the host binary with `command -v pluto`, then `~/.local/bin/pluto`.
5. Saves the absolute path; later commands run that path over SSH.

Why this was fragile in practice:

- It ignores `~/.ssh/config`. A host alias, non-default port, identity file,
  `ProxyJump`, or `HostName` override is invisible to the current probe.
- It accepts only `user@host`. A target with a port must be forced into ssh
  syntax that the registry does not model.
- It saves a device even when the probe failed. `device ls` cannot distinguish
  "connected" from "saved but broken".
- It can fail on the password path: Windows OpenSSH, TTY availability, a
  disabled password login, an existing passphrase-protected key, or a
  `~/.ssh` permission problem all produce different errors with no diagnosis.
- It only looks in two places for `pluto`. A binary in `~/.cargo/bin`,
  `/usr/local/bin`, a Nix profile, or a custom path is reported missing.
- A raw `--device user@host` always runs `pluto` from `PATH`, not a learned
  path.
- There is no doctor, no version check, no daemon check, no state/image check,
  and no way to ask "why did this fail?".
- Every command pays a fresh SSH handshake. That is acceptable, but it makes
  transient network errors look like product errors.

### 2.3 Worktree and provisioning today

- `pluto up` resolves the current directory to a git worktree with
  `git rev-parse --show-toplevel`, then creates a box keyed by that path.
- `pluto up --repo URL` clones into
  `~/.local/state/pluto/projects/<name>-<digest>`, always on the remote's
  default branch, and creates one box. There is no `--branch`.
- `pluto task create --isolate` creates a worktree under
  `~/.local/state/pluto/projects/tasks/<key>`.
- There is no project registry, no managed worktree list, no fetch/update, no
  branch selection, and no cleanup.
- The host path leaks into every non-trivial flow. From Windows the user must
  know a host path or use a one-branch repo clone.
- `--device` re-execs in the SSH session's directory, which is the host home,
  so the host cannot see the laptop's repo or branch. A bare `pluto --device
  host up` therefore has no project context today.
- A box is identified by worktree path. Moving a worktree creates a second box
  unless the old record is found by path.
- `pause`, `run`, `status`, and `logs` require an explicit box id or host
  worktree path; there is no default from the client checkout.

### 2.4 Automated side today

Schedules:

- Stored on the box record with `armed_at` and `last_fired`.
- The daemon evaluates every 30 seconds, catches up missed occurrences, and
  materializes each as a durable task/run and queue item.
- A schedule with a job requires an approved contract revision.
- Firing wakes a paused box through the normal queue dispatch.
- There is no CLI to see the next occurrence, the last outcome, or a skip.

Webhooks:

- GitHub push, pull request, and issue deliveries are **already implemented**
  on a dedicated listener started with daemon flags: signature verification,
  event parsing, delivery-ID deduplication, trusted-contract checks, and
  task/queue admission all exist.
- Generic signed webhooks use HMAC over timestamp + body and are also already
  implemented.
- The gaps are operational: registration only through daemon flags, no
  `webhook ls`, no intake/rejection history, and `pluto setup github` writes
  `[events.*]` but does not approve the revision or register a source.
- A real GitHub delivery needs an inbound public TLS endpoint. The server has
  full inbound internet in the review-3 setup, so the existing webhook is the
  right source. Only the client-to-server leg stays LAN-only; GitHub talks to
  the server, not to the laptop.
- The missing pieces are operational: runtime source registration, a CLI to
  inspect sources and rejections, durable intake/decision events, and the
  `pluto trust` handoff. Do not rebuild the webhook, and do not add a poller
  for this review.

## 3. Proposed design

Three workstreams, one shared spine: **make every action leave a durable,
causal record, and make the first-run path deterministic.**

### 3.1 Observability: a durable activity ledger plus a live view

Do not build a metrics platform. Build the smallest thing that answers
"what/where/when/why" for every box, trigger, queue item, task, run, and job.

#### Event record

Append-only JSONL in the state directory, one event per line:

```json
{
  "schema": 1,
  "seq": 1842,
  "id": "evt_...",
  "at": "2026-10-22T14:31:02.184Z",
  "kind": "github.event.detected",
  "level": "info",
  "trace_id": "trc_...",
  "parent_id": "evt_...",
  "actor": {"type": "github", "id": "delivery-...", "client": "", "host": "neptuno"},
  "where": {
    "host": "neptuno",
    "box_id": "b75b12c9-...",
    "project": "cipher",
    "branch": "demo/pluto",
    "worktree": "/home/sid/.pluto/worktrees/cipher-demo-pluto",
    "task_id": "",
    "run_id": "",
    "job_id": "",
    "queue_id": "",
    "schedule": "",
    "event_source": "github",
    "delivery_id": "..."
  },
  "summary": "push delivery for Siddhj2206/cipher",
  "duration_ms": 0,
  "reason": "",
  "attrs": {"ref": "refs/heads/demo/pluto", "commit": "b1b2c07"}
}
```

Rules:

- `seq` is monotonic and assigned by the single writer. It is the stable
  ordering key; timestamps alone are not.
- `trace_id` is created at the first cause: a manual command, a schedule
  occurrence, a webhook delivery, or a queue acceptance. Every derived event
  carries it.
- `parent_id` points at the event that caused this one when a direct parent
  exists.
- `where` is a flat object so it is queryable without a schema join.
- `attrs` holds small, non-secret facts. Never store webhook secrets, tokens,
  payload bodies, or full environment values. Store payload size and hash if
  the payload matters.
- `actor.client` is best effort: the CLI sends `X-Pluto-Client` with a device
  nickname or hostname. The daemon never trusts it for authorization.

Event kinds (v1):

| Group | Kinds |
|---|---|
| daemon | `daemon.started`, `daemon.stopped`, `daemon.reloaded` |
| box | `box.created`, `box.wake.started`, `box.wake.done`, `box.pause.started`, `box.pause.done`, `box.failed`, `box.destroyed` |
| phase | `phase.provision.started`, `phase.provision.done`, `phase.wake.started`, `phase.wake.done`, `phase.service.changed` |
| session | `session.started`, `session.attached`, `session.detached`, `session.stopped` |
| job | `job.queued`, `job.started`, `job.done`, `job.failed`, `job.cancelled` |
| queue | `queue.accepted`, `queue.rejected`, `queue.dispatched`, `queue.blocked`, `queue.recovered` |
| task | `task.created`, `task.run.queued`, `task.run.started`, `task.run.done`, `task.run.failed`, `task.run.blocked` |
| schedule | `schedule.armed`, `schedule.fired`, `schedule.skipped`, `schedule.failed`, `schedule.caught_up` |
| event | `event.received`, `event.verified`, `event.rejected`, `event.admitted`, `event.deduped`, `event.ignored` |
| webhook | `webhook.received`, `webhook.verified`, `webhook.rejected`, `webhook.deduped`, `webhook.ignored`, `webhook.admitted` |
| contract | `contract.applied`, `contract.stale`, `contract.approved`, `contract.rejected` |
| host | `image.imported`, `provider.changed`, `daemon.error` |

Write points (minimum):

- daemon start/stop/reload and every `logf` call that represents a decision;
- box create, start, wake, pause, fail, destroy;
- provision/wake start and finish, with exit code and duration;
- service/session transitions already reported by the agent;
- queue accept, reject, dispatch, block, recover;
- task/run create and every state transition;
- schedule arm, fire, skip, catch-up, failure;
- webhook receipt before verification, verification result, admission,
  deduplication, rejection, and ignore;
- contract apply, staleness detection, approval, rejection;
- image import and provider changes;
- job start/finish with source and causal IDs.

#### Correlation and "where" in records

Add these fields to existing records (additive, zero values mean "unknown"):

- `state.Job`: `Source`, `TraceID`, `TaskID`, `RunID`, `QueueID`,
  `ScheduleName`, `EventSource`, `EventID`, `JobOrigin`.
- `state.Task` / `TaskRun`: `Actor`, `TraceID` (task already has source and
  event context).
- `state.Schedule`: `LastRunID`, `LastJobID`, `LastOutcome`, `LastFiredAt`,
  `NextDueAt` (computed response field, not persisted).
- `state.QueueItem`: already has source, task, run, event, schedule; add
  `TraceID` and `Actor`.
- `api.CreateBoxRequest` / `RunRequest` / `TaskRequest`: optional `Actor` and
  `TraceID`, supplied by the CLI. Never used for authorization.

`pluto status` then reports, for example:

```text
where:    host neptuno, box b75b12c9, cipher/demo/pluto
last:     job f58cd556 done in 7.1s (triggered by schedule nightly at 02:00)
next:     schedule nightly in 3h12m
queue:    1 pending (event github, received 14:31:02)
trace:    trc_...  pluto timeline trc_...
```

#### CLI surface

New:

```text
pluto overview [--json]
pluto events [--since 1h] [--limit 100] [--box ID] [--kind PREFIX]
             [--trace ID] [--follow] [--json]
pluto timeline <box|task|run|trace-id> [--json]
pluto watch [--interval 2s]
pluto health
pluto schedule ls [box]
pluto schedule show <box> <name>
pluto schedule run <box> <name>       # enqueue one occurrence now, explicitly
pluto webhook ls [--json]
pluto webhook add ...                 # persisted source config; see 3.4
pluto webhook rm <source>
pluto webhook test <source>           # signed local delivery, clearly labelled
pluto trust <box>                     # approve the exact current contract revision
```

`pluto watch` is the review centerpiece. One screen, refreshed in place:

```text
PLUTO  neptuno  daemon v0.1.5  up 3h12m  state ~/.local/state/pluto
boxes 2/4 running   queue 1 pending / 0 running
webhook :8787  github production  last delivery 8s ago

BOXES
ID        PROJECT/BRANCH       STATE    PHASE       LAST JOB              NEXT
b75b12c9  cipher/demo/pluto    paused   provision   test done 7.1s        02:00 in 3h
9904175e  foo/master           running  wake        -                     -

SCHEDULES
BOX       NAME      CRON        NEXT              LAST
b75b12c9  nightly   0 2 * * *   02:00 in 3h12m    done 6m ago (test)

LIVE
14:31:02  webhook  received   github production delivery=...
14:31:02  webhook  verified   hmac ok
14:31:02  webhook  admitted   issue #42 pluto:fix job=issue-fix trace=trc_...
14:31:02  queue    accepted   q=... task=... run=...
14:31:03  box      woke       b75b12c9 in 2.4s
14:31:05  job      started    issue-fix
14:31:12  job      done       exit 0 in 7.1s
```

No interactivity in v1. It is a readable, screenshot-friendly live view, not
a dashboard product. Colors are optional and degrade to plain text.

`pluto events --follow` is the raw stream for scripts and for the presenter
when the TUI is too much. `--json` is the machine contract.

#### Daemon API

- `GET /v1/overview`: daemon version/uptime, host, state dir, boxes
  running/max, queue counts, active jobs, schedules with next due, GitHub
  sources with last/next poll, optional webhook sources with redacted status,
  recent events.
- `GET /v1/events?since=&limit=&box=&kind=&trace=`: newest first by default.
- `GET /v1/events/stream?since=`: SSE, one event per `data:` frame, heartbeat.
- `GET /v1/schedules`: all box schedules with `next_due_at`, last outcome.
- `POST /v1/boxes/{id}/schedules/{name}/run`: materialize one explicit
  occurrence, with `source=manual` and the same trust checks.
- `GET /v1/github`: configured repos, interval, last/next poll, last detected
  event, cursor, token-present flag. `POST`/`DELETE` manage sources; `POST
  /v1/github/{project}/poll` runs one immediate poll.
- `GET /v1/webhooks`: optional public-ingress sources, endpoint, last
  delivery, counts, whether a secret is present. Never return the secret.
- `POST /v1/webhooks`, `DELETE /v1/webhooks/{source}`: runtime management,
  persisted in `state/webhooks.json`, loaded at daemon start. Env var names,
  not secret values.
- `POST /v1/boxes/{id}/contract-trust` already exists; add the CLI wrapper.

Implementation shape:

- New `internal/events` package: `Event`, `Store`, `Append`, `List`, `Follow`,
  rotation, retention, atomic append, sequence counter.
- `Server` gets an `events *events.Store`; `state.Store` stays the state
  authority. The event writer is called from daemon handlers, scheduler,
  dispatcher, runner callbacks, and a small `record` helper.
- Keep `logf`; make it also append a `daemon.notice` event when it describes a
  decision. Do not replace logs with events; logs remain the detailed evidence.
- Runner gets an optional `Observe` callback or a `JobOrigin` parameter so job
  events carry source and trace. The daemon is the only place that knows the
  trigger.

Retention and safety:

- Default: 30 days or 1,000,000 events, whichever comes first; rotate at
  64 MiB per segment. `PLUTO_EVENT_RETENTION` overrides.
- Append with `O_APPEND`, one JSON object per line; fsync is not required per
  event (state file writes remain durable), but a clean shutdown flushes.
- Redact by construction: no payload bodies, no env values, no secrets.
- Event reads are local-only over the unix socket. No network listener.

#### Metrics and health (small, not a platform)

- `pluto health` prints daemon status, version, state version, uptime, state
  dir, socket, boxes count.
- `pluto overview` includes host load and per-box CPU/memory/disk when
  available. Read cgroup `cpu.stat`, `memory.current`, and disk usage for a
  live box; report "unknown" rather than guessing when paused.
- `pluto metrics <box>` shows the latest Firecracker snapshot plus a small
  normalized summary (vCPU, memory, block, network, uptime) instead of raw
  JSON only.
- Do not add Prometheus, OpenTelemetry, Grafana, or a time-series database.
  The contribution is causal, local observability, not a monitoring stack.

### 3.2 Device onboarding: connection profiles, preflight, doctor

Keep SSH as the boundary. The key correction from the comparables research:
**do not store `user@host` as the identity of a device.** Store a pointer to
OpenSSH, resolve it with `ssh -G` for display and diagnostics, and let the
user's config remain the source of truth. VS Code, JetBrains Gateway, T3 Code,
Coder, DevPod, and Codespaces all converge on some version of this.

#### What the comparables do

| Tool | Connection model | Onboarding lesson |
|---|---|---|
| VS Code Remote-SSH | Remote server + localhost tunnel | Delegates user/port/key/ProxyJump/config to OpenSSH; recommends `ControlMaster`; has explicit server cleanup and a login terminal. |
| JetBrains Gateway | Remote backend + TCP forward | Explicit host/port/user/auth, optional `~/.ssh/config` parse, Test Connection, offline installer upload, strict host-key modes. |
| OpenCode | HTTP server + client; `ssh -L` or pairing | Clean server/client split and `/global/health` version handshake, but no native SSH config awareness. Desktop SSH installs a server, which Pluto rejects. |
| T3 Code | Desktop-managed SSH + remote server + port forward | The strongest onboarding: discovers `~/.ssh/config` and `known_hosts`, resolves with `ssh -G`, uses askpass for password auth, ordered diagnostic hops, keepalives, `ControlMaster=no`, managed vs external ownership. |
| Codespaces / `gh cs ssh` | API-managed key + SSH + local forward | One command, automatic key, optional generated SSH config; host-key checking is disabled only because the tunnel is the trust anchor. |
| DevPod / Coder | Remote agent + tunnel / `ProxyCommand` | Generated `Host` entries work with every SSH tool, but writing user config is security-sensitive: Coder had CVE-2026-55427 for config injection. |
| Tailscale SSH | Identity-based SSH | Removes key/TOFU work, but requires a tailnet and ACL administration. An optional transport, not the onboarding model. |
| Fly / Modal / E2B / Daytona | Vendor proxy, custom exec, tokens | Convenient but proprietary; not a fit for Pluto's "own the host, use standard SSH" boundary. |

Recurring patterns worth copying:

1. Profile points at OpenSSH; do not reimplement config parsing.
2. Onboarding is an ordered pipeline: ssh → config → DNS/TCP → host key →
   auth → shell → binary → version → daemon → image.
3. Discover the binary in the login shell too, then execute an absolute path.
4. Handshake a machine-readable version and capability set before use.
5. Remote execution stays remote-owned; the client only selects and verifies.
6. Loopback forwarding with readiness probes is the remote-UI pattern.
7. Doctor is ordered hops with one exact fix per failure, not one verbose blob.
8. Lifecycle state is idempotent, owned, and observable; Windows is a distinct
   code path, not a shell assumption.

Anti-patterns to avoid:

- Storing `user@host` as the device identity.
- Parsing `~/.ssh/config` by hand instead of using `ssh -G`.
- `BatchMode=yes` as the universal first-contact probe.
- `command -v` in a non-interactive shell as the only discovery.
- `StrictHostKeyChecking=no` for a real user host.
- Writing `~/.ssh/config` without escaping, markers, a diff, and `--dry-run`.
- Reimplementing `ssh-copy-id` or `authorized_keys` writes.
- Installing or updating the remote binary from the client.
- `-t` for non-interactive commands; use `-T`, and `-tt` only for `attach`.

#### Registry format (backward compatible)

Keep the existing `[device]` map working. Add a richer `[devices.<name>]`
table for new entries. Keep the machine-written cache out of the
human-edited file.

```toml
version = 1

[devices.neptuno]
# Preferred: a Host alias from ~/.ssh/config. OpenSSH resolves it.
destination = "neptuno"

# Raw destination is allowed when there is no alias:
# destination = "siddhant@home.example.net"

# Optional overrides. Only set when the user gave them; they become
# -p / -i / -J and therefore override ~/.ssh/config.
# port = 2222
# identity_file = "~/.ssh/id_ed25519"
# proxy_jump = "bastion"

remote_path = "auto"     # "auto" or an absolute host path
platform = "auto"        # "auto" or "linux"
auth = "auto"            # "auto", "key", or "interactive"
trust = "accept-new"     # "strict" or "accept-new"; explicit, not silent
control_master = false   # POSIX-only, opt-in
```

Cache, rewritten on every check, no secrets:

```text
~/.local/state/pluto/devices/<nickname>.json
%LocalAppData%\pluto\devices\<nickname>.json
```

```json
{
  "schema_version": 1,
  "nickname": "neptuno",
  "checked_at": "2026-10-22T12:00:00Z",
  "resolved": {"host": "home.example.net", "user": "siddhant", "port": 2222, "proxy_jump": "bastion"},
  "remote": {"os": "linux", "arch": "amd64", "shell": "/bin/sh", "path": "/home/siddhant/.local/bin/pluto", "path_source": "login-shell", "version": "0.1.5", "protocol": 1, "capabilities": ["json", "device", "tasks"]},
  "daemon": {"reachable": true},
  "last_ok": "2026-10-22T12:00:00Z",
  "last_error": null
}
```

Rules:

- `destination` is passed to `ssh` as written. Do not replace an alias with a
  resolved `user@host`; that would drop `ProxyJump`, `IdentityFile`,
  `HostKeyAlias`, and `Match` behavior.
- `port`, `identity_file`, and `proxy_jump` are escape hatches for users who
  do not want an SSH config entry. Warn when they override a resolved value.
- `remote_path = "auto"` uses the cache only if it still exists, is
  executable, and answers the expected version; otherwise rediscover.
- `platform = "linux"` fails fast on a non-Linux host. The daemon is
  Linux-only; Windows hosts should point at WSL2's sshd.
- `auth = "key"` uses `BatchMode=yes`; `auth = "interactive"` allows one
  prompt; `auto` tries key first and falls back only on a TTY.
- `trust = "accept-new"` is an explicit choice, visible in the profile. It
  still rejects a changed key. `strict` is the safe default for an existing
  entry.
- No secrets in the profile or cache.

#### `pluto device add`

Accepted forms:

```text
pluto device add <nickname>
pluto device add <nickname> <destination>
pluto device add <nickname> <host> --user U --port 2222
pluto device add <nickname> --destination sid@host --identity ~/.ssh/id_ed25519
pluto device add <nickname> <destination> --remote-path auto|/abs/path
pluto device add <nickname> <destination> --auth auto|key|interactive
pluto device add <nickname> <destination> --trust strict|accept-new
pluto device add <nickname> <destination> --dry-run --json
```

Behavior:

1. **Resolve without connecting.** Run `ssh -G -- <destination>` and show
   `neptuno -> sid@home.example.net:2222 via bastion`. A bare nickname uses
   the same name as the destination, so `pluto device add neptuno` works when
   `~/.ssh/config` already has `Host neptuno`. Do not parse config yourself.
2. **Preflight host key.** If the key is unknown, show the fingerprint and
   require an explicit `--trust accept-new` or an interactive `ssh` approval.
   A changed key is always a failure with `ssh-keygen -R` instructions.
3. **Preflight auth.** Try `BatchMode=yes` first. On an auth failure with a
   TTY, offer `ssh-copy-id` (the standard tool) or print the PowerShell
   equivalent on Windows. Do not reimplement `authorized_keys` writes. Never
   store a password.
4. **Probe the remote.** One `sh -c` probe returns OS, arch, shell, candidate
   paths, and `command -v`. If OS is not Linux, stop with the WSL2 hint.
5. **Discover `pluto`.** Order: explicit `--remote-path`, cached path,
   `command -v` in `sh -c`, `command -v` in `sh -lc`, then
   `$HOME/.local/bin`, `/usr/local/bin`, `/usr/bin`, `~/.cargo/bin`. Require
   an executable regular file; reject aliases/functions.
6. **Handshake.** Run `<path> version --json` and record version, protocol,
   and capabilities. Fall back to text `version` for legacy binaries and mark
   them legacy.
7. **Daemon check.** Run `<path> daemon status --json` (new read-only
   command) or `<path> ls --json` until it exists. A down daemon is a warning,
   not an add failure.
8. **Save and summarize.** Save the profile even when offline, with
   `last_error`. Print a connection card: resolved target, fingerprint, auth
   method, remote OS, pluto path/version, daemon status, next command.

`--dry-run` prints the resolved target, the exact ssh argv, and the checks it
would run; it writes nothing. `--no-probe` saves without contacting the host.

#### `pluto device doctor [nickname]`

Read-only; no installs, no config writes, no key changes. Ordered hops:

```text
local.ssh        ssh binary, version, Windows vs Git OpenSSH
config.resolve   ssh -G: host, user, port, proxy, identity
dns              effective host resolves
tcp              connect to host:port, bounded
proxyjump        jump host resolves/reaches
hostkey          known, unknown, or changed; fingerprint
auth             publickey / keyboard-interactive / password; agent detail
shell            sh runs; OS and arch
pluto.lookup     candidate path and how it was found
pluto.version    version, protocol, capabilities
protocol         client/server compatibility
daemon           socket/health
state            writable state dir
image            a base image is available
git              only when a project will be created
```

Each hop is `ok`, `warn`, `failed`, or `skipped`, with `detail`, `code`, and
`fix[]`. `--json` emits a stable schema. `--print-ssh` prints the exact argv
with secrets/paths redacted. `--offline` uses the cache and marks network hops
skipped. Exit `0` healthy, `1` unhealthy, `2` usage.

Example:

```text
neptuno  FAIL  siddhant@home.example.net:2222 via bastion
  ✓ local.ssh        OpenSSH_10.5p1
  ✓ config.resolve   neptuno -> siddhant@home.example.net:2222
  ✓ tcp              connected in 42ms
  ✓ hostkey          SHA256:...
  ✗ auth             Permission denied (publickey)
    next: ssh-add ~/.ssh/id_ed25519
    next: ssh -o BatchMode=yes neptuno true
  – shell            skipped
  – pluto.lookup     skipped
```

#### Error taxonomy

Stable codes, one `next:` per code:

| Code | Meaning | Next |
|---|---|---|
| `E_SSH_MISSING` | no `ssh`/`ssh.exe` | install OpenSSH; or set `PLUTO_SSH` |
| `E_SSH_CONFIG` | `ssh -G` failed | `ssh -G -- <destination>`; fix the config line |
| `E_DNS` | host does not resolve | check VPN/DNS/hostname |
| `E_TCP_REFUSED` | sshd not listening | check sshd/port/firewall |
| `E_TCP_TIMEOUT` | route blocked | check network/VPN; retry |
| `E_PROXY` | jump host failed | `ssh -J <jump> -v <destination>` |
| `E_HOSTKEY_UNKNOWN` | unknown key | show fingerprint; approve or `--trust accept-new` |
| `E_HOSTKEY_CHANGED` | key changed | verify out of band; `ssh-keygen -R` |
| `E_AUTH` | key refused | `ssh-add -l`; `ssh-copy-id`; `ssh -v` |
| `E_AUTH_INTERACTIVE` | password/2FA needed in batch mode | rerun with `--auth interactive` |
| `E_SHELL` | `sh` missing | use a supported Linux host |
| `E_PLUTO_MISSING` | no executable candidate | print host install commands |
| `E_PLUTO_VERSION` | missing protocol/capability | install matching release |
| `E_DAEMON` | daemon down | `pluto install` or `pluto daemon` |
| `E_PLATFORM` | not Linux | point at Linux/WSL2 sshd |
| `E_OFFLINE` | timed out, cache valid | retry; show `last ok` |

#### Transport rules

- Build one remote command string: `sh -c '<quoted path + argv>'`. The SSH
  exec request is a single string; do not rely on ssh joining words.
- Use `-T` for non-interactive commands; `-tt` only for `attach` and other
  interactive work. `-t` can inject CRLF and corrupt output.
- `BatchMode` follows the profile. Never use it for first-contact key install.
- Set `ConnectTimeout` (8-10s) and `ServerAliveInterval=15`,
  `ServerAliveCountMax=3` for long-running work.
- Host key: `StrictHostKeyChecking=yes` by default. `accept-new` only when the
  profile says so. Never `UserKnownHostsFile=/dev/null` for a real device.
- Pass `-p`, `-i`, `-J` only for explicit overrides; otherwise let
  `~/.ssh/config` win. Do not pass `-p 22` "for clarity".
- `ControlMaster` is opt-in and POSIX-only; Windows OpenSSH lacks the same
  multiplexing. Default off.
- Capture stderr separately, keep a redacted tail, show one line by default,
  full detail with `--verbose`. Preserve the remote exit code; treat ssh
  `255` as transport only when stderr matches a known ssh error.
- `--device` keeps the re-exec model. It passes `PLUTO_CLIENT=<nickname>` for
  event attribution only; never for authorization.

#### Windows specifics

- Find `ssh.exe` explicitly: `PLUTO_SSH`, `where.exe ssh.exe`,
  `%SystemRoot%\System32\OpenSSH\ssh.exe`, then Git for Windows.
- Spawn `ssh.exe` directly, never through `cmd.exe` or a PowerShell string.
- Use `os.UserHomeDir()`/`USERPROFILE`; `~/.ssh/config` and `known_hosts`
  live under `%USERPROFILE%\.ssh`.
- Windows OpenSSH uses the `ssh-agent` service, not `SSH_AUTH_SOCK`.
- No `ControlMaster`; `ProxyJump` is preferable to `ProxyCommand` because
  `nc` may not exist.
- `ssh-copy-id` may be absent; print the PowerShell `Add-Content` equivalent
  or point at Git Bash/WSL. Do not implement a half-correct writer.
- Remote payloads are POSIX because the host is Linux. Do not send CRLF.
- If OpenSSH is missing, print the exact `Add-WindowsCapability` command.
- If a password prompt is impossible, fail with manual key instructions
  instead of hanging.
- Keep the Windows CI fake-ssh tests; add doctor tests for each error class.

### 3.3 Managed projects and worktrees

Goal: from Windows, "run this repo on this branch" should not require a host
path. The host owns a workspace; the user names a project and optionally a
branch.

#### Workspace root

`PLUTO_HOME` defaults to `~/.pluto`.

```text
~/.pluto/
  projects.toml                         # registry
  repos/<project>/                      # normal clone, default branch
  worktrees/<project>-<branch>/         # one per branch, flat and predictable
```

State remains `~/.local/state/pluto`; boxes, disks, queue, tasks, and events
stay there. `~/.pluto` is the human-visible code workspace, not the daemon's
state directory. This avoids a state migration and keeps `rm -rf ~/.pluto`
from deleting box disks by accident.

The default worktree key is `<project>-<branch>`, with `/` replaced by `-`:
`cipher-demo-pluto`. The key is the stable target. The path is
`~/.pluto/worktrees/<key>`. `--worktree` overrides the key/path when the user
wants a different name or location. The same key is accepted as a target:

```text
pluto pause
pluto pause cipher-demo-pluto
pluto pause cipher@demo/pluto
```

`cipher@demo/pluto` is the unambiguous form; `cipher-demo-pluto` is the short
form for display and the default path. If two keys would collide after
sanitizing, the registry appends a short hash and reports it.

`projects.toml`:

```toml
[project.cipher]
source = "https://github.com/Siddhj2206/cipher.git"
default_branch = "master"
repo = "/home/sid/.pluto/repos/cipher"
created_at = "2026-10-22T14:00:00Z"
last_fetch = "2026-10-22T14:00:00Z"
```

#### Default target: no box IDs

The user should not type a box ID for normal work. From a laptop checkout:

```sh
pluto --device lab up          # create or resume cipher-demo-pluto
pluto --device lab pause       # pause the same box
pluto --device lab run test    # run in the same box
pluto --device lab status      # show the same box
pluto --device lab attach
```

How this works without changing every command's host-side cwd:

1. The client resolves its local git worktree: root, remote URL, project name,
   branch, HEAD, and whether the worktree is dirty.
2. `remotecli` sends that as a compact `PLUTO_CLIENT_CONTEXT` environment
   value on the remote command. It is metadata, never authorization.
3. The host CLI uses the context only when no explicit target was given.
   `up` uses it to create or resume the managed worktree and box. `pause`,
   `run`, `status`, `logs`, `attach`, `jobs`, `job ls`, and `task create`
   resolve the same `(project, branch)` box.
4. An explicit positional target or `--worktree` always wins.
5. Outside a git worktree, commands that need a default fail with
   `next: cd into the repo, or pass <project>@<branch>`.
6. `destroy` is the exception: it stays destructive and requires an explicit
   target or a confirmation, even when a context exists.

The context is also useful evidence: `status` and `events` can say
`client: laptop (cipher/demo/pluto)`, and the daemon can record it as
best-effort actor metadata.

#### CLI

```text
pluto project add <url|path> [--name NAME] [--branch BRANCH]
pluto project ls [--json]
pluto project show NAME
pluto project rm NAME [--force]
pluto worktree ls [project]
pluto worktree rm <key|path> [--force]

pluto up [project|key] [--branch BRANCH] [--worktree KEY|PATH]
pluto up --repo URL [--branch BRANCH]
```

Resolution rules for `up`:

1. `--worktree KEY|PATH` wins. A relative value is a key under
   `~/.pluto/worktrees`; an absolute value is a host path. On a client, this
   is a **host** worktree name/path, not the laptop's local path.
2. With no argument, use `PLUTO_CLIENT_CONTEXT` when present: project from the
   remote URL, branch from HEAD. This is the `pluto --device lab up` case.
3. Inside a host git worktree with no context, use the current worktree as
   today.
4. `--repo URL` registers the project if needed and resolves the requested
   branch (default: the project's default branch).
5. A positional that is a registered project name resolves the project;
   `--branch` selects the branch. A positional that matches a managed key
   resolves that worktree. A positional that is an existing path is treated
   as a host path, with a warning that managed targets are preferred.
6. No target and no context: fail with `pluto project ls` /
   `pluto up --repo` as next steps. Do not guess a "last used" project.

The client resolves the project from the tracked remote, preferring `origin`.
If the worktree has no remote, it cannot create a host worktree; fail with
`next: add a remote, or use --worktree <host path>`. If the remote is SSH and
the host has no agent, report that and suggest an HTTPS remote or a host-side
SSH agent. Multiple remotes are shown in the error so the choice is explicit.

Branch/worktree behavior:

- Fetch `origin` before creating a new branch worktree. If offline, use local
  refs and warn.
- If the branch exists locally in the managed repo, use it. If only
  `origin/<branch>` exists, create a tracking branch. If neither exists,
  create from the default branch's current `origin` tip.
- The default branch uses `~/.pluto/repos/<project>` directly. It is a normal
  clone, so a box on the default branch has a stable source worktree.
- Use a per-project lock (`flock` on `repos/<project>/.pluto.lock`) around
  fetch/worktree creation.
- Never copy uncommitted files into a box. The sync boundary is still
  git-once (ADR 0008). A managed worktree starts from a committed ref.
- `worktree rm` refuses a dirty worktree or one with a live box unless
  `--force`; `project rm` refuses while boxes exist unless `--force`.
- Box lookup prefers `(project, branch)` over path. Existing records keep
  their path; a moved worktree can be reattached by identity.
- If a box exists but its worktree is missing, fail loudly and offer
  `worktree ls` / `destroy`; do not silently create a fresh worktree that
  makes the old disk look current.

#### Local commits and the sync boundary

The host cannot see commits that exist only on the laptop. `up` from a local
branch therefore has two honest paths:

- **The branch is on the remote.** The host fetches it and creates the managed
  worktree. This is the review 3 path.
- **The branch is local-only or ahead.** `up` fails with
  `next: push the branch, or use --worktree <host path>`. It does not silently
  run an older commit.

A later slice may upload a git bundle of the committed branch over the same
SSH connection, still without a file watcher or bidirectional sync. That is
not part of this review unless the demo needs it.

Task isolation:

- New isolated task worktrees go under
  `~/.pluto/worktrees/<project>-task-<id>`, not the state directory.
- Existing task boxes remain readable; no migration is required.
- `task changes` already reports isolation, so the display does not change.

#### Why this is not a second sync mechanism

The managed workspace only decides **where a committed checkout lives**. The
box still receives a git bundle of committed refs, and still owns its live
copy. No file watcher, no bidirectional sync, no dirty-file copy. `up ./`
from Windows remains out of scope.

### 3.4 Automated side: make schedules and repository events operable

#### Contract approval

Add `pluto trust <box>`:

- Reads the box's current worktree contract.
- Shows the revision, the project, and the job/event policy that will be
  approved.
- Requires confirmation (or `--yes`).
- Calls `POST /v1/boxes/{id}/contract-trust` with the exact revision.
- Records `contract.approved` with the actor and revision.

`pluto setup github` ends with:

```text
next: approve the exact revision with 'pluto trust <box>'
next: register the source with 'pluto webhook add ...'
```

This closes the current gap where the setup flow tells the user to call an
API that has no CLI.

#### Schedule surface

- `pluto schedule ls [box]` shows box, name, cron, next due, last fired,
  last outcome, and last job/run.
- `pluto schedule show <box> <name>` shows the occurrence history and the
  queue/task/run chain.
- `pluto schedule run <box> <name>` enqueues one explicit occurrence now
  (`source=manual`) after the same contract trust check. It does not modify
  the cron clock.
- `status` and `watch` show the next due time and last outcome.
- A skipped schedule (invalid cron, trust failure, queue full) records a
  durable `schedule.skipped` or `schedule.failed` event with the reason.

#### GitHub event sources: use the existing webhook

The signed GitHub webhook and generic webhook intake already exist. This slice
does **not** rewrite signature verification, event parsing, deduplication,
trust checks, or task/queue admission. It adds the operational surface around
them: durable intake events, source status, runtime registration, and the
missing `pluto trust` / `pluto webhook add` handoff.

The server has full inbound internet. The client-to-server leg is LAN-only;
GitHub talks to the server, not to the laptop. That makes the existing webhook
the review-3 source. No poller, relay, or Tailscale is needed.

Webhook prerequisites:

- The listener must bind a reachable interface or sit behind a TLS reverse
  proxy. The current documentation uses `127.0.0.1:8787`, which is loopback;
  expose it through the chosen proxy, not by weakening the listener.
- GitHub needs a payload URL that ends at `/github/<source>` and a secret.
- The host needs `git`, `curl`, and either `gh` or a small API script in the
  box for the issue/PR jobs.

Operational surface:

```text
pluto webhook add github --source production --box <box> --secret-env PLUTO_GITHUB_WEBHOOK_SECRET
pluto webhook ls [--json]
pluto webhook rm <source>
pluto webhook test <source>       # signed local delivery, labelled as a test
```

- Persist sources in `state/webhooks.json`; the secret is an environment
  variable name, never a value in state.
- `pluto webhook add` registers at runtime, so the daemon unit does not need a
  restart or new flags. The existing daemon flags remain supported.
- `pluto webhook ls` shows source, endpoint, secret present, last delivery,
  accepted/rejected counts, and last rejection reason. Never return the secret.
- `pluto webhook test` sends a signed local delivery to exercise the path
  without GitHub. It is labelled `test`; do not present it as a GitHub
  delivery.
- `pluto setup github` ends with the exact `pluto trust` and
  `pluto webhook add` commands instead of naming an API with no CLI.
- Every intake attempt appends `webhook.received`, `webhook.verified`,
  `webhook.rejected`, `webhook.deduped`, `webhook.ignored`, or
  `webhook.admitted`, with delivery ID, repo, ref, object ID, size, and hash.
- Do not store payload bodies. Never store secrets, tokens, or environment
  values.

Boundary: the review must show a real GitHub delivery or say that GitHub was
unreachable. A local `pluto webhook test` is evidence for the handler, not for
GitHub reachability.

#### Review 3 GitHub loop: issue -> draft PR -> test comment

The target demo is a real GitHub workflow, not a synthetic event:

1. A labelled issue is opened on `cipher`.
2. GitHub delivers the `issues` event to the host's webhook listener.
3. Pluto verifies the delivery, admits the event, creates/reuses the issue
   box, and runs the `issue-fix` job with the issue payload.
4. The job uses an agent to make a focused change, commits it on
   `pluto/issue-<number>`, pushes the branch, and opens a **draft** PR linked
   to the issue.
5. The PR `opened`/`synchronize` delivery runs the `pr-test` job on the PR
   box.
6. The job runs the test suite and posts or updates one comment on the PR with
   the result, command, duration, and a link to the Pluto task/run.
7. `watch`, `events --trace`, `task show`, and `run logs` show the whole chain.

This exercises the product claim: a repository event becomes durable,
inspectable work on a machine that outlives the laptop.

Contract sketch:

```toml
[jobs.issue-fix]
description = "read a labelled issue, make a focused change, open a draft PR"
command = "scripts/pluto-issue-fix"

[jobs.pr-test]
description = "run tests on a pull request and comment the result"
command = "scripts/pluto-pr-test"

[events.issue]
job = "issue-fix"
actions = ["labeled"]            # label gate; see below
credentials = ["PLUTO_ISSUE_TOKEN"]

[events.pull_request]
job = "pr-test"
actions = ["opened", "synchronize", "reopened", "ready_for_review"]
trusted_label = "pluto:trusted"
credentials = ["PLUTO_PR_TOKEN"]
```

Why `actions = ["labeled"]` for issues: every opened issue should not start
an agent. The job (or a future `labels` filter in the policy) checks for
`pluto:fix`. A label is the human opt-in; the event is still durable and
visible even when the job ignores it.

Credentials:

- `PLUTO_ISSUE_TOKEN`: repository contents write and pull-request write, so
  the issue job can push a branch and open a draft PR. Least privilege, fine
  for one repository.
- `PLUTO_PR_TOKEN`: pull-request write and issue-comment write, so the test
  job can post the result. It must not be a contents-write token if the job
  only tests.
- Values live in the daemon environment, never in `.pluto.toml`, the queue,
  the event log, or job output. The contract stores only names.
- A PR from a fork or an unlabeled PR is untrusted and receives no host
  credentials. It may still run tests with no token, but it must not post a
  comment. A maintainer labels it `pluto:trusted` to opt in, or the issue job
  labels its own same-repository draft PR.
- The PR test job must not be able to approve or merge. Keep its token
  comment-only.

Idempotency and loop safety:

- Branch name is deterministic: `pluto/issue-<number>`. Before creating,
  check for an existing PR with that head; update/reuse it instead of opening
  duplicates.
- The comment carries a hidden marker, for example
  `<!-- pluto:test-report -->`. The job searches the PR comments, then
  updates the marked comment or creates one. Repeated `synchronize` events
  update the same comment instead of spamming.
- One durable task per issue/PR already exists. Later events append serialized
  runs; the job should treat a run as "recompute and update", not "append a
  new comment".
- The PR test job only comments. It does not push a fix on failure, so a
  failing test cannot create an infinite synchronize loop. A later "agent
  fixes failing test" job is a separate opt-in.

What must exist on the host:

- Inbound HTTPS from GitHub to the server's webhook endpoint. The listener is
  loopback by default; put it behind the TLS reverse proxy that terminates
  GitHub's request. The client-to-server SSH leg stays LAN-only.
- `PLUTO_GITHUB_WEBHOOK_SECRET` in the daemon environment and the same secret
  in GitHub.
- `git`, `curl`, and either `gh` or a small GitHub API script in the box. The
  job owns its tools through `[tools]`; the demo can use `curl` to avoid a
  `gh` install.
- The trusted default-branch contract approved with `pluto trust`.
- A repository where the job tokens can push branches, open draft PRs, and
  comment.

What the job scripts do (repo-owned, not Pluto-owned):

- `pluto-issue-fix`: read `PLUTO_EVENT_PAYLOAD`, verify the `pluto:fix` label,
  create the branch, run the agent, commit, push with the token without
  printing it, open a draft PR linked to the issue, and add `pluto:trusted`
  to that PR.
- `pluto-pr-test`: read the PR context, run the declared test command, capture
  the exit code and tail, then create or update the marked comment with a
  stable summary. Always comment, including on failure. Redact tokens and
  long logs; link to `pluto task show`/`run logs` for the full output.

Boundary: a real GitHub webhook delivery is the goal for review 3. If GitHub
is unreachable, fall back to `pluto webhook test` and say so; do not present a
local signed request as a GitHub delivery.

## 4. Review 3 demo script

Prepare a small contract with:

- a job that records `date`, `hostname`, `pwd`, and the trigger source;
- a schedule every 2 minutes (or a near-future cron) so the demo does not
  wait for 02:00;
- `[jobs.issue-fix]` and `[events.issue]` with the `issues` label gate;
- `[jobs.pr-test]` and `[events.pull_request]` with the trusted label;
- `auto_pause = "5m"` so the schedule wakes a paused box.
- daemon environment variables for `PLUTO_ISSUE_TOKEN`, `PLUTO_PR_TOKEN`,
  `PLUTO_GITHUB_WEBHOOK_SECRET`; no secrets in the contract.

Sequence:

1. **Windows onboarding on the LAN/hotspot**
   - Put the laptop and host on the same network; use the host's LAN or
     hotspot address, not a tailnet name.
   - `pluto.exe device add lab sid@192.168.x.x`
   - `pluto.exe device doctor lab` (all PASS)
   - Screenshot the connection card and doctor.
2. **Open the local checkout and create/resume by context**
   - From the `cipher` checkout on Windows:
     `pluto.exe --device lab up`
   - Pluto resolves `cipher/demo/pluto`, creates
     `~/.pluto/worktrees/cipher-demo-pluto`, and boots the box. A second
     `up` prints that it found and resumed the same box.
   - Screenshot the local `git status`/branch, `up` output, and `status`
     with the managed worktree path. No box ID or host path was typed.
3. **Box and contract without IDs**
   - `pluto.exe --device lab status`
   - `pluto.exe --device lab job ls`
   - `pluto.exe --device lab trust` (approve the exact revision)
4. **Automated schedule**
   - `pluto.exe --device lab pause` (no box ID)
   - `pluto.exe --device lab schedule ls` shows `next due in 1m`.
   - `pluto.exe --device lab watch` (or `events --follow`) shows:
     `schedule.fired -> queue.accepted -> box.wake -> phase.wake ->
     job.started -> job.done`.
   - Screenshot the watch screen and `schedule show`; then `job logs`.
5. **GitHub issue -> draft PR**
   - Open an issue on `cipher` and add `pluto:fix`.
   - `pluto webhook ls` shows the source, last delivery, and accepted count.
   - `watch` shows `webhook.received -> webhook.verified ->
     webhook.admitted -> queue.accepted -> job.started -> job.done`
     for the issue task.
   - Screenshot the issue, the webhook delivery accepted, and the draft PR
     created by the job.
6. **GitHub PR -> test comment**
   - The PR `opened`/`synchronize` delivery triggers `pr-test`.
   - `watch` shows the PR task/run; `task show` and `run logs` show the
     result.
   - Screenshot the PR comment with the test result and the `events --trace`
     chain. Push another commit to the PR and show the comment updated
     instead of duplicated.
7. **Durability**
   - `attach` and run `hostname`, `uname -a`, `pwd`; write a marker file.
   - `pause`, `up`, `attach`, `cat` the marker. No target is typed.
8. **Close with `overview`**
   - One screenshot with host, boxes, queue, schedules, webhook sources, and
     recent events. This is the answer to "what is going on, where, when".

Screenshots should be taken on Windows for the client story, and on the host
only for the `watch`/`overview` view if the terminal is the host's. Do not
claim the real Windows acceptance record (#162); it stays blank until the
manual run is actually recorded.

## 5. Implementation order

1. **Event ledger and API** (`internal/events`, daemon wiring, retention,
   `GET /v1/events`, `GET /v1/overview`, `pluto events`, `pluto health`).
2. **Instrumentation and correlation** (job origin, task/run/queue trace,
   schedule/webhook/box/phase events, status additions).
3. **Operable automation** (`schedule ls/show/run`, `webhook ls/add/rm/test`,
   runtime source registration, `pluto trust`, persisted source config,
   intake/decision/rejection events).
4. **`pluto watch` and `overview`** (the demo surface).
5. **Device profiles and doctor** (`ssh -G`, discovery, classification,
   registry extension, Windows checks).
6. **Managed projects and worktrees** (`project` registry, `up` resolution,
   `worktree ls/rm`, task isolation path, client context for no-target
   commands).
7. **GitHub loop** (issue label gate, draft-PR job, PR test/comment job,
   comment marker/idempotency, token allowlists, webhook docs).
8. **Docs and demo harness** (review script, sample contract, screenshots,
   Windows/Linux tests).

Each step is independently testable and leaves the branch runnable. Do not
start with a visual dashboard; the ledger is the source of truth, and the
watch view is a read of it.

## 6. Tests and acceptance

- Unit: event schema, sequence ordering, rotation/retention, redaction,
  correlation ID propagation, schedule next-due math, webhook rejection
  records, profile parsing/backward compatibility, `ssh -G` fallback,
  project/worktree resolution, branch collision and dirty-worktree refusal,
  `PLUTO_CLIENT_CONTEXT` resolution, and no-target command injection.
- Daemon seam tests: manual run -> job event chain; paused box + schedule ->
  wake + job; signed GitHub issue/PR delivery -> task/run -> queue -> job;
  queue full/rejected; contract trust change blocked before execution; issue
  event -> issue box; PR event -> PR box; duplicate delivery does not
  duplicate a task/run; rejection leaves a durable event.
- CLI tests: table output, `--json` schema, `next:` hints, stable exit codes,
  `pluto pause` with only a client context, and explicit targets overriding
  context.
- Windows CI: fake ssh for `device add`, `doctor`, `--device` re-exec, path
  handling, OpenSSH-missing message, one-password `ssh-copy-id` path.
- Host-only: real KVM box with a short schedule; signed GitHub webhook
  delivery against a test repository; issue -> draft PR -> PR comment;
  pause/wake persistence.
- Manual: the review 3 script above. The #162 acceptance record remains
  unfilled until a real Windows 11 x64 LAN/Tailscale run is recorded.

## 7. Non-goals and non-claims

- No Prometheus, OpenTelemetry, Grafana, or hosted telemetry.
- No public dashboard or remote API. `watch`, `events`, and `overview` are
  local reads over the unix socket. A browser UI can be a later, separate
  decision.
- No alerting, paging, or long-term metrics storage.
- No `up ./` local-folder sync from Windows; git-once remains the boundary.
- No GitHub API poller, relay, or Tailscale dependency for review 3. GitHub
  reaches the server's webhook endpoint over the internet; only the
  client-to-server leg is LAN-only.
- No public exposure of the Pluto control API or client. The webhook listener
  exposes only signed event intake.
- No automatic worktree deletion or garbage collection without an explicit
  command.
- No Windows daemon and no Windows-hosted boxes.
- No remote binary install or update from the client. `device add` verifies
  and prints exact host-side commands; it never copies, downloads, or updates
  `pluto` on the host.
- No claim that a signed local webhook test is a GitHub delivery.
- No claim that #162 is accepted.

## 8. Research sources

Primary docs and source used for the device model:

- VS Code Remote-SSH: <https://code.visualstudio.com/docs/remote/ssh> and
  <https://code.visualstudio.com/docs/remote/troubleshooting>
- JetBrains Gateway: <https://www.jetbrains.com/help/idea/remote-development-a.html>
  and <https://www.jetbrains.com/help/idea/remote-development-troubleshooting.html>
- OpenCode: <https://opencode.ai/docs/server/>,
  <https://opencode.ai/docs/cli/>, <https://opencode.ai/v2/docs/cli/web/>
- T3 Code remote access:
  <https://github.com/pingdotgg/t3code/blob/main/docs/user/remote-access.md>
  and <https://github.com/pingdotgg/t3code/blob/main/docs/internals/overview.md>
- GitHub Codespaces SSH:
  <https://cli.github.com/manual/gh_codespace_ssh>
- DevPod: <https://devpod.sh/docs/developing-in-workspaces/connect-to-a-workspace>
- Coder: <https://coder.com/docs/reference/cli/ssh>,
  <https://coder.com/docs/reference/cli/config-ssh>, and the
  `config-ssh` injection advisory GHSA-mcqq-fqgf-rxwm
- Tailscale SSH: <https://tailscale.com/kb/1193/tailscale-ssh>
- OpenSSH: `ssh(1)`, `ssh_config(5)`, `ssh-copy-id(1)`
- Fly: <https://fly.io/docs/flyctl/ssh-console/> and
  <https://fly.io/docs/flyctl/ssh-issue/>
- Daytona: <https://www.daytona.io/docs/en/ssh-access/>

## 9. Decisions

Already decided from the review discussion:

- **Surface:** terminal `pluto watch` first. A loopback browser dashboard is a
  later, separate decision.
- **Workspace root:** `~/.pluto` for managed repos and worktrees; daemon state
  stays at `~/.local/state/pluto`.
- **Device setup:** no host binary install. Setup means connection profile,
  preflight, doctor, and exact host-side commands when something is missing.
- **Device auth:** one-password setup. `device add` may offer `ssh-copy-id`
  once on a TTY; the password is used for that prompt and never stored.
- **Event journal:** local bounded JSONL, no Prometheus/OTel/Grafana.
- **Default target:** `pluto --device lab up`, `pause`, `run`, `status`,
  `attach`, `logs`, and `job ls` resolve the local repo + branch to the same
  managed worktree key. Explicit target/`--worktree` wins. No box IDs in the
  normal flow.
- **GitHub demo:** real GitHub webhook deliveries to the server, which has
  full inbound internet. The client-to-server leg is LAN-only; GitHub never
  talks to the laptop. No poller, relay, tunnel, or Tailscale is required.
- **GitHub workflow:** issue label `pluto:fix` -> agent/draft PR -> PR
  tests -> one marked comment. A signed local delivery is only a fallback,
  labelled as such.
- **Unpushed commits:** `up` requires the branch on the remote. If the local
  branch is ahead or local-only, it fails with
  `next: git push -u origin <branch>`. No silent stale code; no bundle upload
  in this slice.
- **Issue gate:** only issues labelled `pluto:fix` trigger the issue job.

Still open:

1. **Issue label filter:** add a declarative `labels` field to
   `[events.issue]`, or keep the label check inside the job script?
   Recommendation: add `labels` if the contract change is small; otherwise
   the job owns the gate.
2. **Worktree key collisions:** `<project>-<branch>` is readable but can
   collide. Recommendation: append a short hash only on collision, and always
   accept `project@branch` as the exact form.
3. **PR test comment scope:** comment-only token. Confirm the PR job should
   never push a fix on failure (recommended), or add a separate opt-in
   fix job later.
