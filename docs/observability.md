# Observability

Pluto records what it decided and why in one durable, host-local activity
ledger. The same records feed `pluto events`, `pluto timeline`, `pluto watch`,
`pluto overview`, and the schedule and webhook views. This page is the
reference for those surfaces. They read the daemon over the unix socket; none
of them adds a listener, a poller, or a dashboard.

## The event ledger

Every event has a sequence number, timestamp, kind, level, summary, optional
reason, trace id, actor, and a `where` object (host, box, project, branch,
worktree, task, run, queue item, job, schedule, event source, delivery id).
Events never contain payload bodies, tokens, or environment values.

Representative kinds:

| Area | Kinds |
| --- | --- |
| daemon | `daemon.started`, `daemon.stopped`, `daemon.reloaded`, `daemon.error` |
| box | `box.created`, `box.wake.started`, `box.wake.done`, `box.pause.started`, `box.pause.done`, `box.failed`, `box.destroyed` |
| phases | `phase.provision.started/done/failed`, `phase.wake.started/done/failed`, `phase.service.changed` |
| queue | `queue.accepted`, `queue.rejected`, `queue.dispatched`, `queue.blocked`, `queue.recovered`, `queue.done`, `queue.failed` |
| task | `task.created`, `task.run.queued`, `task.run.started`, `task.run.done`, `task.run.failed`, `task.run.blocked` |
| job | `job.queued`, `job.started`, `job.done`, `job.failed`, `job.cancelled` |
| schedule | `schedule.armed`, `schedule.fired`, `schedule.caught_up`, `schedule.skipped`, `schedule.failed` |
| event intake | `event.received`, `event.verified`, `event.rejected`, `event.admitted`, `event.deduped`, `event.ignored`, and the matching `webhook.*` kinds |
| contract | `contract.applied`, `contract.stale`, `contract.approved`, `contract.rejected` |
| host | `image.imported`, `provider.changed` |

`contract.applied` records the revision a box's handoff applied.
`contract.stale` records a queued item whose admitted revision is no longer the
revision on disk. `image.imported` names the version. `provider.changed` names
the provider and the action (`install`, `enable`, `disable`, `remove`,
`route.add`, `route.remove`). None of them carries a secret.

### `pluto events`

```text
pluto events [--box ID] [--kind PREFIX] [--trace ID] [--since WHEN] [--limit N] [--follow] [--json]
```

- `--box` accepts a box id, an id prefix, or a worktree path.
- `--kind` matches a prefix (`job.`, `schedule.`, `webhook.`).
- `--trace` selects one causal chain.
- `--since` accepts a sequence number (events after it), a duration such as
  `1h`, or an RFC3339 time.
- `--limit` reads at most 1000 events before following. A larger value is a
  usage error.
- `--follow` keeps the stream open and prints new events as they arrive.
- `--json` prints `schema_version: 1` and the event list; with `--follow` it
  prints one JSON object per line.

Human output is chronological (oldest first) so a trace reads top to bottom.
The JSON list keeps the daemon's newest-first order.

### Retention

The ledger defaults to:

- 30 days, or 1,000,000 events, whichever comes first;
- 64 MiB per segment, rotated before the next append;
- redaction by construction: no payload bodies, no environment values, no
  secrets.

`PLUTO_EVENT_RETENTION` overrides the bounds with a comma-separated list:

```sh
PLUTO_EVENT_RETENTION=age=720h,count=1000000 pluto daemon
```

`age=0` or `count=0` disables that bound. Unknown keys are an error at daemon
start, so a typo does not silently keep everything.

## `pluto timeline`

```text
pluto timeline <box|task|run|trace-id> [--json]
```

Reads one chain in sequence order. A trace id (`trc_...`) selects the exact
causal chain; a box selects its history; a task or run selects the chain for
that work, including runs recorded before trace ids existed. Each line carries
the recorded reason. `--json` adds the resolved target identity and the events
in sequence order.

## `pluto watch` and `pluto overview`

```text
pluto watch [--interval 2s] [--once]
pluto overview [--json]
```

`overview` is one static read: daemon identity and uptime, state directory,
boxes running/max, queue depth, active jobs, schedules with next due, webhook
sources, and a recent-event tail. Failed schedules, rejected webhooks, blocked
queue items, and failed jobs are visually distinct in plain text.

`watch` renders the same frame in place until interrupted. It is
non-interactive and dependency-free: plain text always, color optional, no
alternate screen, no keys, no browser. `--once` prints exactly one frame for a
screenshot or a script. `PLUTO_WATCH_WIDTH` sets the fixed frame width
(default 120; clamped to 100–240). A daemon that goes away between refreshes
leaves the last frame on screen and prints a `warning:` line on stderr.

## `pluto health`

```text
pluto health
```

Prints daemon status, version, state version, uptime, socket, state directory,
and box count. It is a static read and changes nothing.

## `pluto trust`

```text
pluto trust [box-id|worktree] [--yes]
```

Shows the box's current contract revision, its declared jobs, and its
`[events.*]` policy, then approves that exact revision for unattended work.
With no target, the client checkout's project and branch resolve the box, the
same default `status` and `pause` use; an explicit target wins. `--yes` skips
the confirmation prompt. A later contract change makes the approval stale and
unattended event work blocks until the new revision is approved.

## `pluto schedule`

```text
pluto schedule ls [box-id|worktree] [--json]
pluto schedule show <box-id|worktree> <name> [--json]
pluto schedule run <box-id|worktree> <name> [--json]
```

`ls` shows each schedule's cron, next due time, last cron slot fired, last
outcome (`done`, `warm`, `failed`, `blocked`, `rejected`, `skipped`), and the
run/job it produced. `show` adds the occurrence history and the recorded
reason. `run` materializes one explicit occurrence now without moving the cron
clock; a retried request is deduped by its idempotency key. A schedule that
names a job still requires an approved contract revision.

A repeated identical skip is recorded once. A new reason with the same outcome
(for example queue full, then an unapproved contract) is a new decision and
appears as a new `schedule.skipped` event.

## `pluto webhook`

```text
pluto webhook add <github|generic> --source NAME --box BOX --secret-env ENV
pluto webhook ls [--json]
pluto webhook rm <source>
pluto webhook test <source>
```

`add` persists a source in the daemon's state directory and registers it at
runtime; the daemon still needs `--webhook-listen` to accept deliveries. The
secret is an environment variable name in the daemon, never a value in state
or output. `ls` shows the endpoint, whether the secret is present, the last
delivery, and accepted/rejected counts. `rm` removes the source.

`webhook test` sends one signed delivery through the local handler and records
`webhook.ignored`; it is a handler and secret check, not a GitHub delivery and
not proof that the TLS proxy or GitHub is reachable. See
[webhooks.md](webhooks.md) for the delivery and admission rules.

## Related

- [Review 3 plan](r3-plan.md) — the design and the event model.
- [GitHub events](webhooks.md) — signatures, admission, and the issue/PR loop.
- [Task API](task-api.md) — durable tasks and runs.
- [CLI conventions](adr/0009-cli-conventions.md) — why `next:` lines appear
  only on failures.
