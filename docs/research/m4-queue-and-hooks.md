# M4 Research: Queue/Scheduling/Host Capacity & Git Hook/Init Behavior

**Issues:** #88 (queue/scheduling/host capacity), #89 (Git hook/init behavior)
**Parent:** #83 (M4: event-driven and on-demand work orchestration)
**Date:** 2026-10-06

---

## Part 1: Queue, Scheduling, and Host Capacity (Issue #88)

### 1.1 Current State and Execution Boundaries

| Boundary | File | Key Types/Functions |
|---|---|---|
| HTTP API | `internal/daemon/daemon.go` | `Server`, `handleRun`, `handleCreate`, `handleUp`, `handlePause` |
| Scheduler loop | `internal/daemon/schedule.go` | `SchedulerLoop`, `fireDueSchedules`, `fireBoxSchedules`, `launchFire`, `fire`, `consumeOccurrence`, `beginFiring`/`endFiring` |
| Auto-pause loop | `internal/daemon/autopause.go` | `AutoPauseLoop`, `evaluateAutoPause`, `pauseIdle`, `sessionBusy` |
| State store | `internal/state/state.go` | `Store`, `Box`, `BoxState`, `CreateBox`, `Transition`, `mutate`, `writeFileAtomic` |
| Schedule records | `internal/state/schedule.go` | `Schedule`, `SetSchedules`, `AdvanceSchedule` |
| Job records | `internal/state/job.go` | `Job`, `JobState`, `BeginJob`, `SetJob`, `JobRunning`, `ErrJobRunning` |
| Runner (box lifecycle) | `internal/runner/runner.go` | `Runner`, `Up`, `Pause`, `RunJob`, `Reconcile`, `ReconcileAll` |
| Job execution | `internal/runner/job.go` | `RunJob`, `finishJob`, `failRunningJob` |
| Contract | `internal/contract/contract.go` | `Contract`, `Schedule`, `Job`, `Exec`, `Load` |
| CLI dispatch | `internal/cli/cli.go` | `Run`, command switch |
| CLI commands | `internal/cli/commands.go` | `runRun`, `runUp`, `ensureBox`, `createWorktreeBox` |
| Daemon startup | `internal/cli/host.go` | `runDaemon` (starts `SchedulerLoop` + `AutoPauseLoop`) |

### 1.2 What Exists Today

**Schedules (ADR 0003):**
- Schedules are per-box durable alarms stored in `box.Schedules` (`state.Schedule`: `Name`, `Cron`, `Job`, `ArmedAt`, `LastFired`).
- `SchedulerLoop` fires due schedules every 30s (`SchedulerInterval`). It evaluates once at startup (catching up missed firings) then on each tick.
- A schedule is due when cron has a matching minute strictly after `LastFired` (or `ArmedAt`) and at or before now.
- **Busy-box skip:** If `box.JobRunning()` is true, the occurrence is skipped — `consumeOccurrence` advances `LastFired` like a fire would. No queue, no concurrent run.
- **Missed-occurrence coalescing:** `occurrenceAfter` scans from `last` to `now` minute-by-minute; the first match ends the scan. A long outage costs one scan to its first missed minute, producing exactly one late run.
- **At-least-once delivery:** `launchFire` runs in a goroutine; `consumeOccurrence` advances the clock after the run completes. A crash between fire and consume can duplicate a run.
- **Warm-up:** A schedule with no `Job` calls `runner.Up` only — no job is recorded.

**Jobs:**
- `runner.RunJob` calls `store.BeginJob` (refuses with `ErrJobRunning` if a job is already running on the box), then `runner.Up`, then executes via the agent, then `store.SetJob` with the outcome.
- Job history is capped at `JobHistoryLimit = 20`, newest first.
- `ReconcileAll` (called at daemon startup) marks running jobs on dead boxes as failed.

**Box lifecycle (ADR 0002):**
- States: `created` → `running` → `paused` → `running` (etc.), plus `failed`.
- `Reconcile` aligns record with systemd unit state: running record + no unit → paused; active unit → running; failed unit → failed.
- Auto-pause: idle window (default 1h, per-box override, `"off"` disables). Busy = client attached OR job running OR session burning CPU/IO above noise floor. Unknown facts (no live view, unreadable cgroup) keep the box awake.

**State store (ADR 0004, ADR 0005):**
- Single-writer: daemon holds an exclusive `flock` on the state directory.
- All mutations are atomic file replaces (`writeFileAtomic`): temp file → write → sync → rename → dir sync.
- State version is `StateVersion = 1`; record schema is `RecordSchema = 1`.

### 1.3 What Does NOT Exist (Gaps for M4)

| Gap | Impact |
|---|---|
| **No durable host-wide queue** | Events cannot be accepted durably before acknowledgment; no queue ID returned to callers. |
| **No priority classes** | Explicit user requests, repository events, and scheduled work cannot be ordered against each other. |
| **No aging** | Lower-priority work can starve indefinitely under sustained load. |
| **No queue limits** | Overload is unbounded; no visible rejection reason. |
| **No running-box capacity limit** | Event bursts can start unlimited boxes, exceeding host resources. |
| **No deduplication** | Provider retries (webhook redelivery) can launch duplicate jobs. |
| **No event intake** | No webhook receiver, no hook adapter, no event filtering. |
| **No schedule queuing** | A due schedule on a busy box is skipped (ADR 0003), not queued. M4 changes this: queue instead of skip. |
| **No coalescing across schedules** | Each schedule coalesces independently; no host-wide "one pending occurrence per schedule" guarantee across daemon downtime. |

### 1.4 Crash/Restart Behavior at Each Stage

| Stage | Current Behavior | Risk |
|---|---|---|
| **Event acceptance** | No queue exists; no acceptance path. | N/A — must be built. Acknowledge only after durable queue write. |
| **Dequeue** | No dequeue exists. | N/A — must be built. Dequeue must be idempotent (mark as "starting" before launching). |
| **Box start** | `ReconcileAll` at startup marks running jobs on dead boxes as failed. `runner.Up` transitions to running. | A crash mid-start leaves the box in `failed` or `created`; `Reconcile` on next status/loop will realign. |
| **Job completion** | `SetJob` records terminal outcome. `ReconcileAll` fails running jobs on dead boxes. | A crash between `BeginJob` and `SetJob` leaves a `running` job; `ReconcileAll` will mark it failed on next daemon start. |
| **Schedule fire** | `launchFire` goroutine → `fire` → `consumeOccurrence`. | Crash between `fire` and `consumeOccurrence` → duplicate run on restart (at-least-once, ADR 0003). |
| **Schedule skip** | `consumeOccurrence` advances `LastFired` immediately on skip. | No risk — skip is recorded synchronously. |
| **Daemon restart** | `SchedulerLoop` evaluates at startup, coalescing missed firings. `ReconcileAll` fixes box/job state. | Accepted work (if queue existed) must survive; schedules coalesce to one late run. |

### 1.5 Queue Lifecycle and Idempotency Recommendations

**Proposed queue item lifecycle:**

```
pending → starting → running → done
                  ↓
                failed
                  ↓
           (recorded, no auto-retry)
```

- **pending:** Accepted into the durable store, not yet eligible for dequeue (waiting for capacity or higher-priority work).
- **starting:** Dequeued and claimed; the daemon is preparing the box. A crash here must allow recovery (re-dequeue or mark failed).
- **running:** The box is up and the job is executing.
- **done/failed:** Terminal. Recorded in the box's job history. No automatic retry in M4.

**Idempotency rules:**
1. **Deduplicate by source event ID** when available (GitHub delivery ID, generic webhook ID). Store the dedupe key on the queue item; reject duplicates at acceptance.
2. **At-least-once delivery** is the bias (ADR 0003). A crash window can duplicate a run; dedupe keys prevent provider retries from doing the same.
3. **Dequeue is a state transition:** `pending → starting` must be atomic (single-writer store makes this natural). A crashed `starting` item must be recoverable: either a startup sweep reclaims stale `starting` items, or the item carries a lease/epoch.
4. **Job execution is idempotent per queue item:** The queue item links to exactly one box and one job. If the box already has a running job, the queue item waits (one-job-per-box).

### 1.6 Priority, Aging, and Capacity Policy

**Priority classes (from #83):**
1. **Explicit user requests** (highest) — `pluto run`, `pluto up`, remote CLI.
2. **Repository events** — push, PR, issue, generic webhook.
3. **Scheduled work** (lowest) — cron schedules, warm-ups.

**FIFO within a class.** Aging promotes work one class at a time after a configured wait (e.g., a scheduled item waiting >N minutes is promoted to event priority). This prevents starvation without preempting running work.

**Running-box capacity:**
- A configurable host-wide maximum of running boxes, enforced without preemption.
- The limit interacts with declared box resources (`[box].resources`): the scheduler should track allocated CPUs/memory and refuse to start a new box when the declared resources would exceed host capacity. This requires a host-resource model (total CPUs/memory vs. sum of running box allocations).
- When capacity is unavailable, new work is queued (not rejected), except when the queue is full.

**Queue bounds:**
- At capacity, reject new automatic work with a recorded, visible reason.
- Explicit requests use available queue capacity and are never allowed to exceed the running-box limit.
- A new user request returns a queue ID and state immediately.

### 1.7 Schedule Coalescing and Busy-Box Behavior Change

**Current (ADR 0003):** A due schedule on a busy box is skipped; `LastFired` advances.

**M4 change:** A due schedule on a busy box (or when host capacity is unavailable) is **queued** instead of skipped. The schedule's `LastFired` does NOT advance — the occurrence remains pending until it can run.

**Preserving missed-occurrence coalescing:** Across daemon downtime, `occurrenceAfter` still coalesces to one late run. The queue item for a scheduled occurrence is created at fire time; if the daemon was down, the startup evaluation creates one queue item (not a backlog). The key insight: coalescing happens at the schedule-evaluation level (one pending occurrence per schedule), and the queue item is the materialization of that occurrence.

**Warm-up schedules** also use host capacity. A warm-up that cannot run immediately is queued like any other work.

### 1.8 Starvation and Overflow Edge Cases

| Edge Case | Mitigation |
|---|---|
| Scheduled work starves under continuous event load | Aging promotes aged scheduled items to event priority after configured wait. |
| Event burst fills the queue | Queue-full rejection with visible reason; explicit requests still accepted. |
| Host capacity exhausted by long-running boxes | Queue waits; no preemption. Aging ensures eventual execution. |
| Same PR/issue events arrive rapidly | Dedupe by event ID; one queue item per work-item box; later events queue another job on the same box. |
| Daemon crashes during `starting` | Startup sweep reclaims stale `starting` items (re-dequeue or mark failed). |
| Schedule fires while daemon down | Startup evaluation coalesces to one queue item. |

---

## Part 2: Git Hook and Init Behavior (Issue #89)

### 2.1 Current State

- **No `pluto init` command exists.** The CLI (`internal/cli/cli.go`) dispatches: `daemon`, `up`, `run`, `attach`, `pause`, `ls`, `status`, `jobs`, `logs`, `destroy`, `image`, `device`, `box`, `vsock`, `install`, `uninstall`, `version`, `help`.
- **No Git hook management.** No `post-commit` hook, no `core.hooksPath` support, no hook installation/removal.
- **`pluto install`** (`internal/cli/host.go`) installs the daemon as a systemd user service — unrelated to Git hooks.
- **Contract** is `.pluto.toml` in the worktree root (`contract.FileName`). It declares jobs, services, sessions, schedules, resources, auto-pause, env, tools, provision, wake.
- **Box creation** is per-worktree (`store.CreateBox` is idempotent per worktree path). A box is created without a machine (`StateCreated`); the runner moves it to running on first `up`.

### 2.2 Git Hook Behavior (Primary Documentation)

**From Git documentation (https://git-scm.com/docs/githooks):**

- **Hook location:** Git looks for hooks in `.git/hooks/` by default. Each hook is a script named after the hook event (e.g., `post-commit`).
- **`core.hooksPath`:** Overrides the hooks directory. When set, Git runs hooks from the specified path instead of `.git/hooks/`. This is the recommended mechanism for sharing hooks across a team or preserving existing hooks.
- **`post-commit`:** Runs after a commit is created. It takes no parameters. A non-zero exit code does not affect the commit (it is already created), but Git prints the hook's output. This makes `post-commit` safe for notification-only use.
- **Hook arguments:** Some hooks receive arguments via command-line or stdin. `post-commit` receives none.
- **Hooks are not copied on `git clone`:** Hooks must be installed via `core.hooksPath`, a template directory (`git config --global init.templateDir`), or a package manager. This is why `pluto init` must explicitly install hooks.
- **Chaining:** A hook script can chain to an existing hook by invoking it (e.g., `.git/hooks/post-commit` can call the Pluto hook). This preserves existing project hooks.

**From GitHub webhook documentation (https://docs.github.com/en/webhooks):**

- GitHub delivers webhook events via HTTP POST to a configured URL.
- `X-Hub-Signature-256` provides HMAC-SHA256 verification using a shared secret.
- The endpoint must be reachable by GitHub over HTTPS.
- GitHub retries deliveries; deduplication by delivery ID is the consumer's responsibility.

### 2.3 Hook Preservation and Availability Behavior

**Preserving existing hooks:**
- If `.git/hooks/post-commit` already exists, `pluto init` must NOT overwrite it. Instead, install the Pluto hook via `core.hooksPath` (pointing to a Pluto-managed directory) or chain to the existing hook.
- If `core.hooksPath` is already set, `pluto init` must respect it and install the Pluto hook into that directory (or chain).
- The existing `.pluto.toml` contract must be preserved. `pluto init` scaffolds a starter contract only when none exists.

**Availability behavior:**
- **Daemon unavailable:** The hook must fail silently (or with a non-fatal warning) without breaking the commit. `post-commit` exit code does not affect the commit, so the hook can exit 0 even if the daemon is unreachable.
- **Hook runs outside the daemon host:** The hook submits an event to the daemon via the unix socket. If the hook runs on a different machine (e.g., a laptop while the daemon is on a server), the local socket path won't work. The hook should detect this and either skip silently or use a configured remote socket path.
- **Hook runs during `pluto init`:** `pluto init` prepares the repo but does not create or register a box. The hook is installed but no box is started.

### 2.4 Recommended Install/Remove Model

**Install (`pluto init --with-hooks`):**
1. Check for existing `.pluto.toml`. If absent, scaffold a starter contract. If present, preserve it.
2. Check for existing `core.hooksPath`. If unset, set it to a Pluto-managed directory (e.g., `.pluto/hooks/`). If set, use the existing path.
3. Check for existing `post-commit` hook in the target directory. If absent, install the Pluto hook. If present, chain: rename the existing hook to `post-commit.pluto-chain` and install a `post-commit` that calls both.
4. The Pluto hook script submits an event to the daemon via the unix socket (HTTP POST to `/v1/events` or similar). It passes the worktree path, commit hash, and branch as context.
5. Do NOT create or register a box. Do NOT start a box.

**Remove (`pluto init --remove-hooks` or `pluto uninstall`):**
1. Remove the Pluto hook from the hooks directory.
2. If the Pluto hook was chained (renamed existing hook), restore the original.
3. If `core.hooksPath` was set by Pluto and no other hooks remain, unset it.
4. Preserve `.pluto.toml` and any existing hooks.

### 2.5 Hook Event Identity

The hook must identify:
- **Worktree:** The absolute path to the worktree root (resolved from the hook's working directory).
- **Commit:** The commit hash (from `git rev-parse HEAD` or the hook's environment).
- **Branch:** The current branch (from `git rev-parse --abbrev-ref HEAD`).
- **Box/project:** Resolved by the daemon from the worktree path (the daemon's `CreateBox` is idempotent per worktree).

The event payload sent to the daemon:
```json
{
  "source": "post-commit",
  "Worktree": "/path/to/worktree",
  "Commit": "abc123...",
  "Branch": "main",
  "Timestamp": "2026-10-06T12:00:00Z"
}
```

### 2.6 Smallest `pluto init` and Hook Setup Interaction for M4

**`pluto init` (no flags):**
- Scaffold `.pluto.toml` if absent (starter contract with a sample job and schedule).
- Preserve existing `.pluto.toml`.
- Do NOT install hooks.
- Do NOT create/register a box.
- Do NOT start a box.

**`pluto init --with-hooks`:**
- Everything `pluto init` does, plus:
- Install the `post-commit` hook via `core.hooksPath` (preserving existing hooks).
- The hook submits a `post-commit` event to the daemon (best-effort, non-blocking).

**Hook script (installed to `core.hooksPath/post-commit`):**
```sh
#!/bin/sh
# Pluto post-commit hook: notify the daemon of a new commit.
# Fails silently — a commit must never break because Pluto is unavailable.
PLUTO_SOCKET="${PLUTO_SOCKET:-${XDG_RUNTIME_DIR:-/tmp}/pluto/pluto.sock}"
WORKTREE="$(git rev-parse --show-toplevel)"
COMMIT="$(git rev-parse HEAD)"
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
curl -sf -X POST --unix-socket "$PLUTO_SOCKET" \
  -H "Content-Type: application/json" \
  -d "{\"source\":\"post-commit\",\"worktree\":\"$WORKTREE\",\"commit\":\"$COMMIT\",\"branch\":\"$BRANCH\"}" \
  http://pluto/v1/events >/dev/null 2>&1
exit 0
```

### 2.7 Daemon Unavailable and Off-Host Behavior

| Scenario | Behavior |
|---|---|
| Daemon not running | Hook exits 0 silently; commit succeeds. |
| Socket path not found | Hook exits 0 silently; commit succeeds. |
| Hook on different host than daemon | Hook cannot reach local socket; exits 0 silently. A future remote-hook path (via saved-device/SSH) is out of scope for M4. |
| Hook installed but `pluto init` not run | Hook is inert (no box, no event). |
| Daemon rejects event (queue full, filter mismatch) | Hook exits 0; the rejection is recorded daemon-side. |

---

## Part 3: ADR 0003 Implications and Update Path

ADR 0003 currently states:
> "An occurrence that comes due while the box already has a job running is skipped — no queue and no concurrent run — and the skip advances the schedule like a fire would, so the next occurrence proceeds normally."

**M4 changes this:** The occurrence is **queued** instead of skipped. The schedule's `LastFired` does NOT advance when the occurrence is queued. The queue item is the materialization of the occurrence; when it eventually runs, `LastFired` advances.

**What ADR 0003 preserves:**
- At-least-once delivery (crash can duplicate a run).
- Missed-occurrence coalescing across daemon downtime (one late run, not a backlog).
- "Trigger" as the umbrella for manual runs, schedules, and connection wakes.

**What ADR 0003 must add:**
- A queued occurrence is not a skip; it is pending work.
- The queue is the mechanism that decouples schedule firing from box availability.
- Priority classes and aging apply to queued work.
- The running-box capacity limit interacts with schedule queuing.

---

## Part 4: Summary of Recommendations

### Queue/Scheduling (#88)

1. **Build a durable host-wide queue** in the state store, with atomic transitions (`pending → starting → running → done/failed`).
2. **Acknowledge events only after the queue record is durable** (write-before-ack).
3. **Deduplicate by source event ID** when available; at-least-once otherwise.
4. **Implement priority classes** (user > event > scheduled) with FIFO within class and aging-based promotion.
5. **Enforce a configurable running-box limit** without preemption; track declared box resources against host capacity.
6. **Bound the queue**; reject automatic work with a visible reason when full; explicit requests always accepted.
7. **Change busy-box schedule behavior** from skip to queue; preserve missed-occurrence coalescing.
8. **Return a queue ID and state immediately** for user requests.
9. **No automatic retries** of failed jobs in M4.

### Git Hook/Init (#89)

1. **Add `pluto init`** that scaffolds a starter `.pluto.toml` only when none exists; preserves existing contracts.
2. **Make hook installation opt-in** (`pluto init --with-hooks`); never install by default.
3. **Use `core.hooksPath`** to preserve existing `.git/hooks/`; chain to existing `post-commit` hooks.
4. **The hook submits an event** to the daemon via the unix socket; it does NOT start a box.
5. **The hook fails silently** when the daemon is unavailable; `post-commit` exit code never breaks the commit.
6. **The hook identifies worktree, commit, and branch**; the daemon resolves the box/project from the worktree.
7. **`pluto init` does NOT create or register a box** — it only prepares the repo and optionally installs hooks.

---

## Appendix: Key Code Pointers

| Concern | Location |
|---|---|
| Queue does not exist | `internal/daemon/daemon.go` — `handleRun` calls `runner.RunJob` directly |
| Schedule skip behavior | `internal/daemon/schedule.go:73-77` — `if fresh.JobRunning() { skip }` |
| Schedule coalescing | `internal/daemon/schedule.go:129-142` — `occurrenceAfter` |
| At-least-once delivery | `internal/daemon/schedule.go:89-97` — `launchFire` goroutine |
| Job concurrency refusal | `internal/state/job.go:145-174` — `BeginJob` returns `ErrJobRunning` |
| Crash recovery | `internal/runner/runner.go:376-393` — `ReconcileAll` |
| Box lifecycle | `internal/state/state.go:41-66` — `transitions`, `CanTransition` |
| Atomic writes | `internal/state/state.go:541-574` — `writeFileAtomic` |
| Single-writer lock | `internal/state/state.go:529-539` — `lockDir` |
| Contract parsing | `internal/contract/contract.go:566-692` — `Parse`, `validate` |
| CLI command dispatch | `internal/cli/cli.go:60-116` — command switch |
| Daemon startup | `internal/cli/host.go:22-75` — `runDaemon` |
| Auto-install (systemd) | `internal/cli/host.go:77-96` — `runInstall` (NOT Git hooks) |
| ADR 0003 (triggers) | `docs/adr/0003-triggers-are-durable-alarms.md` |
| ADR 0002 (lifecycle) | `docs/adr/0002-box-lifecycle.md` |
| ADR 0007 (contract) | `docs/adr/0007-box-contract.md` |
| ADR 0004 (storage) | `docs/adr/0004-storage-and-lease-substrate.md` |
