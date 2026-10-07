# Task and run API

The host daemon's versioned HTTP/JSON API accepts durable work at
`POST /v1/tasks`. A task is stable requested-work identity; each accepted
execution or follow-up is a run with its own stable ID. Task records and queue
items are stored by the host daemon, so a client can disconnect and later
read `GET /v1/tasks/{task_id}` or `GET /v1/tasks/{task_id}/runs/{run_id}`.

## Create work

`POST /v1/tasks` accepts:

```json
{
  "box_id": "<box-id>",
  "job": "agent",
  "prompt": "Fix the failing test",
  "isolate": false,
  "idempotency_key": "client-generated-key",
  "source": "manual",
  "project": "my-project",
  "ref": "main"
}
```

`box_id`, `job`, `prompt`, and `idempotency_key` are required. `isolate`
defaults to `false`, which reuses the selected branch box. When true, Pluto
creates a separate Git worktree and box from the selected box's current
commit; retries with the same idempotency key reuse that isolated box. Source,
project, and ref default to `manual` and the selected box's project and ref.
Attended work — `source: "manual"`, the CLI prompt path — is admitted without a
revision-specific contract approval and records `trust_decision: "attended"`.
Unattended sources (repository events and schedules) still require the exact
current contract revision to be approved; an unapproved revision returns `403`.
The recorded `contract_revision` is always the revision the run resolved
against.
The declared command receives `PLUTO_TASK_ID`, `PLUTO_RUN_ID`, and
`PLUTO_TASK_PROMPT` in its environment. Commands are ordinary declared jobs;
Pluto does not add agent-specific execution behavior. A successful request
returns `202` with the durable task and first run. Retrying the same request
with the same idempotency key and inputs returns the same IDs. Reusing that
key for different inputs returns `409`.

`POST /v1/tasks/{task_id}/runs` accepts `job`, `prompt`, and
`idempotency_key`. It adds a serialized follow-up run to the task. Run
idempotency keys are scoped to the task; repeating matching inputs returns
the original run, while changed inputs return `409`.

## Read work

- `GET /v1/tasks` lists durable tasks and their run history.
- `GET /v1/tasks/{task_id}` returns one task and all of its runs.
- `GET /v1/tasks/{task_id}/runs/{run_id}` returns one run.

HTTP `202 Accepted` means Pluto durably accepted the request.

## States

The durable lifecycle exposes these run states:

- `queued` — durably accepted and waiting for host/box capacity, or waiting
  behind an active run of the same task. There is no separate `waiting` state:
  the durable queue has no such transition, so a waiting run reports `queued`.
- `starting` — claimed by the dispatcher but not yet handed to the runner.
- `running` — the job is executing.
- `completed`, `failed`, `blocked`, `rejected` — terminal outcomes. `blocked`
  means a precondition failed (for example a contract trust change or a Git ref
  update conflict); `rejected` means the queue refused the work (for example a
  full queue). `reason` carries the actionable error.

At the task level, `accepted` means the task exists but no run has started yet;
`starting` means a run is claimed; `running` means a run is active; otherwise
the task reports its latest run's state. `accepted` is distinct from the run's
`queued` state and is the durable counterpart of the HTTP `202` acceptance. A
queued run starts when host and box capacity allow it. On daemon restart, a
queued run remains recoverable; if a run was already executing and its outcome
cannot be known, it is reported as failed with an actionable reason.

## Follow live progress

Clients that need to follow work after a disconnect poll
`GET /v1/tasks/{task_id}` (or `GET /v1/tasks/{task_id}/runs/{run_id}`) until
the run reaches a terminal state, reading `GET /v1/boxes/{box_id}/logs?job=`
for the recorded output. The CLI packages this as
`pluto task logs <task-id> --follow` and `pluto run logs <run-id> --follow`,
which print output as it arrives and exit when the run terminates.

Invalid requests return `400`, missing task or box returns `404`, conflicting
idempotency keys return `409`, and unavailable queue capacity returns `503`.
Errors use the standard `{"error":"..."}` response shape. Creation of work
against a missing box returns `404`; if a task's box is later unavailable,
adding a follow-up run returns `409` with the box error.
