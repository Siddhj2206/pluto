# Runs: records and retained logs

A **run** is one execution of a task, recorded in that task's history. A run is
legible after its process is gone: the host keeps a versioned structured record
and the run's per-step output on disk.

## Record

Each run is one JSON record at `<state>/runs/<run-id>.json`. The record is
versioned by `schema` (see `state.RunSchema`, currently `1`); a reader refuses a
record whose schema it does not understand rather than guessing.

| Field | Meaning |
|---|---|
| `schema` | run record schema version |
| `id` | run id (also the record file name) |
| `task` | the declared task this run belongs to |
| `box_id` | the box that ran the work |
| `workspace` | the workspace path the box belongs to |
| `trigger` | what started the run: `manual`, `schedule`, or `ci` (repository events later) |
| `trigger_context` | the detail behind the trigger: `fired_at`, and, for non-manual triggers, the schedule occurrence or repository event. A scheduled run also lists any `skipped_occurrences` that coalesced into it |
| `state` | `pending`, then `running`, then `done` or `failed` |
| `exit_code` | the run's exit code; the first non-zero step code |
| `error` | why the run failed before or during a step, when it did |
| `retained` | set on a failed CI run whose ephemeral box was kept for inspection, so the run's details name a working attach path |
| `steps` | one entry per step: name, command, state, exit code, error, timing, and `log` (a log reference) |
| `created_at`, `started_at`, `finished_at`, `duration_ms` | timing |

A task given as a single command has one implicit step named `run`.

## Schedules

A `pluto.yml` schedule names a declared task. Each distinct occurrence adds one
run to that task's history; redelivering the same occurrence reuses the accepted
run's identity, so a retry never duplicates history. A task keeps at most **one
pending run**: a later occurrence coalesces into it — the latest trigger time
wins — instead of queueing, and the occurrences it displaced are noted as
`skipped_occurrences` in the run's trigger context. A due run on a busy box
waits for the active work per the task's lock policy before it executes.

## CI runs

`pluto ci <task>` is the manual CI trigger. It runs the named task once, in a
**fresh ephemeral box** seeded from the workspace's provisioned package layer.
The box mounts no durable volumes, so a CI run can never read or mutate the
workspace's durable box volumes. The run is recorded like any other, with
`trigger` set to `ci`.

A successful CI run **discards** its box. A failed run **retains** it for
inspection: the run's record sets `retained`, `pluto show <run>` prints an
`attach:` line, and `pluto attach <box>` opens the environment that failed.

## Logs

Per-step output is written to disk **while the run is going**, not only streamed:

```
<state>/runs/<run-id>/0.log      # step 0
<state>/runs/<run-id>/1.log      # step 1, when a task declares several steps
```

`state.Step.Log` holds the state-root-relative reference
(`runs/<run-id>/<step>.log`), so a record is self-describing. Because the log is
written during the run, another client can read it before the run finishes
(`pluto logs <run> --follow`).

All of a run's output crosses one host-side boundary before it is stored or
streamed (`runOutputSink` in the daemon). Secret masking (#143) is applied
there, once, so a stored log and a live stream never diverge.

## CLI

```
pluto ls runs [--json]           # recorded runs, newest first
pluto show <run-id> [--json]     # one run: task, box, trigger, state, steps
pluto logs <run-id> [-f|--follow] [--lines N]   # retained step output
```

`pluto logs <run>` accepts a full run id or an unambiguous prefix. `-f` follows
a live run, printing output as it grows, until the run finishes. `--lines 0`
(the default) shows all retained output.

`pluto logs` keeps its earlier meaning for a box id or worktree: `--phase`,
`--service`, and `--job` still select a box's phase logs, a service journal, or
a recorded job's output.

### JSON

`pluto ls runs --json` and `pluto show --json` emit a versioned envelope whose
`schema_version` is the run record schema (`1`), so scripts and integrations can
pin one number:

```json
{"schema_version":1,"runs":[{...}]}
{"schema_version":1,"run":{...}}
```

## Retention

Run records and their logs share **one** configurable window, defaulting to
30 days (720h). A record and its logs are pruned together, never one without
the other; a run that never finished has no finish time and is not pruned.

Configure it on the daemon:

```
pluto daemon --run-retention 720h      # a Go duration; 0 keeps everything
PLUTO_RUN_RETENTION=720h pluto daemon  # the flag wins when both are set
```

The host sweeps expired runs once at startup and hourly after
(`daemon.RetentionLoop`). This is independent of the CI failed-box window,
which bounds boxes, not run history.

## Retained CI boxes and orphan GC

A retained failed CI box is evicted at the **earlier of 48 hours or 5 boxes**;
`pluto box pin <box>` exempts a box from eviction and `pluto box unpin` lifts
the exemption. Configure the bounds on the daemon:

```
pluto daemon --retained-max-age 48h --retained-max-count 5
```

An **orphaned durable box** — its workspace checkout is gone, it is stopped, it
is unpinned, and it has been unchanged past the grace window — is collected,
along with its dangling workspace record:

```
pluto daemon --orphan-grace 24h
```

The host sweeps boxes once at startup and hourly after
(`daemon.BoxRetentionLoop`).
