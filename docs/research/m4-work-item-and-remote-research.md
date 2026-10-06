# M4 Research: Work-Item Boxes, Remote Requests, Remote Repo Identity

Research notes for issues #87 (PR/issue box lifecycle), #90 (remote client
requests over SSH), and #86 (remote repo identity/contract loading), against
parent spec #83 (M4: event-driven and on-demand work orchestration).

Scope: current code, ADRs, and primary docs. No repo files modified; no issues
closed.

---

## 1. Current state (what exists today)

### 1.1 Box lifecycle and identity

- **Lifecycle states** (`internal/state/state.go:34-46`): `created → running → paused → failed`, with a transition table. `Transition` validates and persists; a state change resets the idle clock.
- **Box identity is the worktree path** (`internal/state/state.go:393-440`): `CreateBox(project, branch, worktree)` is idempotent per worktree path — one primary box per worktree. `Project` is `filepath.Base(root)`; `Branch` comes from `git symbolic-ref` (`internal/cli/git.go:10-25`, `internal/cli/commands.go:280-290`).
- **No PR/issue/work-item box concept exists.** There is no second box key, no ref field, no work-item identity. The only box key is the worktree path.
- **Auto-pause** (`internal/daemon/autopause.go`): a box pauses when no client is attached and no job is running for the idle window. Busy signals: live ssh client count, running job, session CPU/IO delta. Unknown readings keep the box awake (never guess).
- **Destroy** is explicit and never automatic (`internal/cli/commands.go:552-609`); ADR 0002 confirms no auto-destroy.

### 1.2 Git sync and ref handling

- **Sync is a git bundle, once at creation** (ADR 0008, `internal/runner/handoff.go:31-90`): on first boot the daemon makes a `git bundle create --all` from the host worktree, streams it over vsock, and the box clones it. Only committed state travels. The host does **not** re-sync on wake.
- **The box's copy is the live one afterwards** — git (commit/push from inside the box) is the floor. No ref advancement, no fetch-from-host, no dirty-worktree handling exists.
- **Remotes are mirrored at first sync** (`internal/runner/handoff.go:182-213`, `internal/state/remote.go`): name, fetch URL, distinct push URLs. The branch tracks `origin` (or the sole remote); ambiguous multi-remote is left untracked with a warning. SSH remotes are mirrored but pushing over SSH is out of scope until M4 (ADR 0008).
- **No ref safety logic exists.** There is no `git fetch`, no `reset --hard`, no stash, no dirty-check anywhere in the codebase. The box is created from a commit, never a dirty worktree (ADR 0008 consequence).

### 1.3 Contract loading

- **Contract is read from the worktree on the host** (`internal/contract/contract.go:338-352`): `contract.Load(worktree)` reads `<worktree>/.pluto.toml`. The daemon parses it; the box never parses TOML.
- **Contract hash is recorded at handoff** (`internal/runner/handoff.go:85-87`): `SetContractHash` stores the applied hash; `ContractStale` (`handoff.go:112-121`) compares a fresh load against it for `pluto status`.
- **No remote contract loading exists.** There is no `git show remote:path`, no bare-clone, no ref-specific contract read. The contract always comes from the host worktree's current checkout.

### 1.4 Remote client requests (SSH path)

- **`pluto --device` re-executes the CLI over SSH** (ADR 0006, `internal/cli/remote.go:18-37`): resolves a saved nickname (or raw `user@host`) to an ssh target, then runs `pluto <argv>` there with stdio streamed and exit code passed through. The daemon knows nothing about devices; the registry is a client-side TOML file (`internal/devices/devices.go`).
- **The daemon speaks HTTP+JSON over a unix socket** (`internal/daemon/daemon.go`, `internal/api/api.go`, `internal/client/client.go`). Routes: `/v1/health`, `/v1/boxes`, `/v1/boxes/{id}/up|pause|attach|run|logs|metrics`, `/v1/images`.
- **`run` streams NDJSON and blocks** (`internal/daemon/daemon.go:313-352`, `internal/client/client.go:224-268`): the response head is held until the first event; output streams as `RunOutput` events; the job ends with a `RunExit` event carrying the recorded outcome. A refused run (concurrent job) returns HTTP 409.
- **No queue ID, no async request, no deduplication.** A `run` call is synchronous: it either streams to completion or errors. There is no "return now, inspect later" path. The only durability is the job record and the box's disk.
- **Connection loss does not kill the job** (`internal/client/client.go:222-224` comment: "The daemon keeps the run going even if this client goes away"). But there is no way to re-attach to a running job's stream except `pluto logs <box> --job <id>`.

### 1.5 Queue and scheduling

- **Schedules are durable alarms on the box record** (ADR 0003, `internal/state/schedule.go`, `internal/daemon/schedule.go`): `[[schedule]]` entries stored with arm time and last-fired clock. The daemon fires due occurrences on a 30s loop.
- **A busy box skips the occurrence** (`internal/daemon/schedule.go:73-77`): no queue, no concurrent run; the skip consumes the occurrence like a fire would. #83 changes this to queue-instead-of-skip.
- **No host-wide work queue exists.** There is no priority class, no aging, no running-box limit, no dedupe by event ID. The only concurrency control is one-job-per-box (`BeginJob` refuses with `ErrJobRunning`, `internal/state/job.go:145-174`).

---

## 2. Issue #87 — PR and issue box lifecycle

### 2.1 What the spec asks (#83 decisions)

- A PR or issue has **one durable event box**, keyed by repository + work-item identity (PR number / issue number). Later events reuse the box and queue another job.
- **PR boxes start from the PR head ref; issue boxes start from the default branch with a dedicated issue branch.**
- Pluto advances the ref **only when it can preserve local work**; otherwise it surfaces the blocked update rather than discarding changes.
- Event boxes follow normal auto-pause and explicit-destroy behavior.

### 2.2 Gap analysis

| Spec requirement | Current state | Gap |
|---|---|---|
| Work-item box keyed by repo + PR/issue identity | Box keyed by worktree path only | Need a second identity dimension (repo + work-item) and a box record that can be created without a host worktree |
| PR box starts at PR head ref | No ref concept; box created from worktree HEAD | Need to create a box from an arbitrary remote ref (PR head), not a local worktree |
| Issue box starts at default branch + issue branch | No issue branch concept | Need to create a dedicated branch from the default branch and a box for it |
| Advance ref preserving local work | No ref advancement at all | Need fetch + merge/rebase with dirty-worktree detection |
| Running job blocks ref update | `BeginJob` refuses concurrent jobs (409) | Need to queue the ref update until the job finishes, or surface "blocked" |
| Dirty worktree handling | No dirty concept (box created from commit) | Need to detect uncommitted changes in the box and preserve them |
| Repeated/out-of-order events | No event concept | Need dedupe by event ID and ordering semantics |
| Blocked ref update state + user resolution | No such state | Need an explicit "blocked" transition and a user-recoverable path |

### 2.3 Ref safety recommendations

The core tension: the box's worktree is the live one (ADR 0008), and an agent may have uncommitted work there. Advancing the ref (to pick up a new PR push) must not discard that work.

**Recommended approach — explicit blocked transition:**

1. **Detect dirty state before advancing.** Before any ref update, check `git status --porcelain` in the box's worktree. If dirty, do not advance; mark the work-item box record as `blocked` (a new `BoxState` or a `Blocked` field on the record) with the reason and the target ref.
2. **Advance only when clean.** When the worktree is clean (or the job has finished and the agent has pushed), fetch the new ref and fast-forward or reset to it. The box's disk is durable, so a reset is safe when there is nothing to lose.
3. **Surface the blocked update.** `pluto status` and the queue inspection show the blocked ref and the reason. The user resolves it by either committing/pushing the agent's work (then retrying) or discarding it (then advancing).
4. **Running job blocks the update.** If `JobRunning()` is true, the ref update waits. This is the existing `ErrJobRunning` semantics extended to ref updates: queue the update as a follow-up job, or mark blocked until the job finishes.

**Out-of-order events:** deduplicate by source event ID (GitHub delivery ID). If a later event arrives while an earlier one is still queued, the queue holds both; the ref advance is serialized through the box's single-job-at-a-time constraint. A ref update that would move the ref backwards (out-of-order) is rejected as blocked rather than silently applied.

### 2.4 Per-repository/work-item box identity and retention

- **Identity:** extend `Box` with a `WorkItemType` (`branch` | `pr` | `issue`) and `WorkItemID` (PR number / issue number) and `RepoURL`. The existing worktree-keyed box becomes the `branch` type. PR/issue boxes are keyed by `(RepoURL, WorkItemType, WorkItemID)`.
- **Retention:** event boxes follow normal auto-pause and explicit-destroy (ADR 0002). They are **not** auto-destroyed (#83 decision). A PR box is reused for every PR event; an issue box for every issue event. Destroy stays explicit.
- **Correction to confirm:** the spec's "one durable event box per PR/issue" is sound and consistent with ADR 0002's durability model. The only correction: the box record needs a ref field (the PR head or issue branch tip) so the daemon knows what to advance to, and a `ContractHash` that can be read from the triggering ref (see #86).

---

## 3. Issue #90 — Remote client requests over SSH

### 3.1 What the spec asks (#83 decisions + #90 acceptance criteria)

- Verify the existing saved-device/SSH path can submit an explicit box or job request to the host daemon, **return a queue ID immediately**, and inspect state after the client disconnects.
- Identify connection-loss and retry cases; recommend deduplication.
- Recommend whether the existing SSH/device path is sufficient for M4.
- Record the phone/app API question as deferred.

### 3.2 Current SSH request/response lifecycle

```
client machine                          host machine
─────────────                          ────────────
pluto --device neptuno run mybox -- pnpm test
  │
  ├─ resolveDevice("neptuno") ──► devices.toml lookup ──► siddhant@neptuno
  │
  ├─ execRemote: ssh -- siddhant@neptuno pluto run mybox -- pnpm test
  │     │
  │     ├─ (ssh connects, remote pluto runs)
  │     │
  │     ▼
  │   remote pluto (client mode)
  │     ├─ HTTP POST /v1/boxes/{id}/run over unix socket
  │     │     │
  │     │     ▼
  │     │   daemon handleRun
  │     │     ├─ resolveRun (contract.Load from worktree)
  │     │     ├─ runner.RunJob ──► agent streams NDJSON
  │     │     └─ daemon relays events to response
  │     │
  │     ▼
  │   NDJSON streams back over ssh ──► client stdout
  │
  └─ exit code passes through ssh ──► client exit code
```

**Key properties:**
- The SSH connection is held for the **entire** job duration. The client blocks until the job finishes.
- Output streams live (NDJSON + flush). Exit code passes through ssh.
- If the client disconnects, the daemon keeps the job running (`client.go:222`), but the client has no way to re-attach to the stream.
- There is no queue ID, no async submission, no state inspection after disconnect.

### 3.3 Gap analysis

| Spec requirement | Current state | Gap |
|---|---|---|
| Return a queue ID immediately | `run` blocks until completion | Need an async request path: accept the request, return a queue item ID, and let the client disconnect |
| Inspect state after disconnect | `pluto status`/`pluto jobs` show box/job state, but no queue item state | Need a queue inspection surface (by ID, by source, by state) |
| Connection-loss handling | Job survives client disconnect, but no re-attach to stream | Need either a re-attachable stream or a polling model |
| Request deduplication | None | Need dedupe by client request ID (or idempotency key) |
| SSH path sufficient for M4? | SSH re-exec is the only remote path | See recommendation below |

### 3.4 Recommendation: SSH/device path is sufficient for M4

The existing `pluto --device` path **can** deliver M4 remote requests with a daemon-side change, without a new transport:

1. **Add a `POST /v1/queue` (or `POST /v1/boxes/{id}/run` async variant)** to the daemon. The request body carries the box/job spec and an optional client idempotency key. The daemon writes a durable queue record and returns `202 Accepted` with a queue item ID immediately.
2. **The client (`pluto --device ... run`)** gets a new `--async` flag (or a `pluto request` noun manager in M5). It submits the request, prints the queue ID, and exits 0. The job runs on the host regardless of the client's connection.
3. **State inspection** uses the existing `pluto --device ... status` / `jobs` / `logs` surface, extended to show queue item state. A `pluto queue ls` (or `pluto job ls` in M5 grammar) lists queued/running/done items.
4. **Deduplication** is by client-supplied idempotency key (or a hash of the request). The daemon stores it on the queue record and rejects duplicates with the existing queue item ID.
5. **Connection loss** is a non-issue for the request path: the queue record is durable before the response is sent, so a client that disconnects after submission still has its work accepted. The client can reconnect and poll by queue ID.

**Why not a new API/MCP route:** #90's acceptance criteria ask to compare with a new API/MCP route "only to confirm why it remains deferred." The answer: the daemon already has the HTTP+JSON surface and the SSH transport already reaches it. A new public API or MCP surface would add an auth surface (ADR 0006: SSH keys + unix socket permissions are the auth) and a second transport to maintain, for no capability the SSH path lacks. #83 explicitly defers a phone/app API and MCP surface to M5+.

**Phone/app API:** deferred. No current path supports it without a new auth surface (the daemon opens no ports; SSH keys are the only auth). Revive trigger: browser/phone clients are actually wanted (DEFERRED.md relay row).

### 4.5 SSH request/response lifecycle (proposed M4 extension)

```
client machine                          host machine
─────────────                          ────────────
pluto --device neptuno run mybox --async -- pnpm test
  │
  ├─ ssh -- neptuno pluto run mybox --async -- pnpm test
  │     │
  │     ▼
  │   remote pluto (client mode)
  │     ├─ HTTP POST /v1/boxes/{id}/run (async)
  │     │     ├─ daemon writes durable queue record
  │     │     └─ returns 202 {queue_id: "q-abc123"}
  │     │
  │     ▼
  │   client prints "queued: q-abc123" and exits 0
  │     (ssh closes)
  │
  │   ── daemon runs the job in the background ──
  │     ├─ runner.RunJob streams to job log
  │     └─ queue record advances: queued → running → done
  │
  └─ later: pluto --device neptuno queue ls
        └─ HTTP GET /v1/queue ──► shows q-abc123 done
```

---

## 4. Issue #86 — Remote repo identity and contract loading

### 4.1 What the spec asks (#83 decisions + #86 acceptance criteria)

- Register a repo by its primary URL even when the host has **no local worktree**.
- Resolve primary remote identity, default branch discovery, trusted event policy from the default branch, job definitions from the triggering ref.
- Private repo authentication without Pluto storing Git credentials.
- Compatibility with the current first-boot Git bundle and remote model.

### 4.2 Current state

- **Box identity is local:** `Project = filepath.Base(root)`, `Branch` from local git (`commands.go:280-290`). The box is created from a local worktree path.
- **Contract is local:** `contract.Load(worktree)` reads the host worktree's `.pluto.toml` (`contract.go:338`). No remote read.
- **Remotes are local:** `hostRemotes(worktree)` shells out to `git remote` in the worktree (`handoff.go:182-213`). No remote discovery.
- **Private repo path:** ADR 0008 + `contract.md:185-204`: the sanctioned path is an HTTPS remote with a token supplied through `[env]` and wired to git by provision (e.g. `url.insteadOf`). Pluto stores no credentials. SSH remotes are mirrored but pushing over SSH is out of scope until M4.
- **First-boot bundle:** `makeBundle` runs `git bundle create --all` in the worktree (`handoff.go:170-176`). This requires a local worktree with the refs.

### 4.3 Gap analysis

| Spec requirement | Current state | Gap |
|---|---|---|
| Register repo URL without local worktree | Box requires a local worktree path | Need a registration path that takes a URL and creates a box record without a worktree |
| Default branch discovery | Branch from local `git symbolic-ref` | Need to discover the default branch from the remote (e.g. `git ls-remote --symref` or GitHub API) |
| Trusted event policy from default branch | Contract read from local worktree | Need to read `.pluto.toml` from the default branch of the remote |
| Job definitions from triggering ref | Contract read from local worktree | Need to read `.pluto.toml` from the PR head ref |
| Private repo credentials | `[env]` token + provision wiring | This path exists; need to reuse it for remote fetch |
| Compatibility with first-boot bundle | Bundle from local worktree | Need to fetch refs from remote into a local bare repo, then bundle |

### 4.4 Recommendation: M4 registration and provenance model

**Registration:** add a `pluto repo register <url>` (or `pluto init --remote <url>`) command that:
1. Resolves the primary remote identity from the URL (normalize `git@github.com:owner/repo.git` → `https://github.com/owner/repo`).
2. Discovers the default branch (`git ls-remote --symref <url> HEAD` or the GitHub API via `gh`).
3. Creates a **bare clone** (or `git init` + `git fetch`) of the default branch into a host-side cache dir under the state root (e.g. `<state>/repos/<normalized-url>/`). This is the host's read-only view of the remote — not a worktree.
4. Reads the trusted event policy (`.pluto.toml`) from the default branch of that bare clone.
5. Creates a box record keyed by `(repo_url, worktree_path="")` — a repo-level box with no local worktree, or defers box creation until the first event.

**Contract resolution by event type:**
- **Push event:** read the contract from the pushed ref in the bare clone. The box (if any) for that branch is advanced to the new ref.
- **PR event:** read the job definition from the PR head ref in the bare clone. The trusted policy (from the default branch) decides whether the PR may trigger and which job. The PR box is created at the PR head.
- **Issue event:** read the contract from the default branch. The issue box is created at the default branch with a dedicated issue branch.

**Private repo credential path (no Pluto-stored credentials):**
- Reuse the existing `[env]` token path (ADR 0008, `contract.md:185-204`). The user supplies a token through the contract's `[env]` (or a host-side environment variable / credential helper).
- For the **host-side bare clone**, the credential is needed on the host, not in the box. Options:
  - **Host git credential helper** (e.g. `git credential fill`, or `gh auth token`): the host's git resolves the credential; Pluto never sees or stores it. This is the cleanest path and matches "Pluto stores no Git credentials."
  - **`url.insteadOf` on the host**: rewrite `https://github.com/` → `https://x-access-token:${TOKEN}@github.com/` using a host-side env var. The token lives in the host environment, not in any Pluto-managed file.
  - **SSH remote**: ADR 0008 says SSH remotes are mirrored but pushing over SSH is out of scope until M4. For M4, SSH fetch from the host is viable if the host has SSH keys configured (the user's existing ssh auth). This avoids tokens entirely.
- The key invariant: **Pluto never writes a credential to disk.** The token is either in the host environment, in the user's git credential store, or in the user's ssh agent. Pluto invokes git and lets git authenticate.

**Compatibility with first-boot bundle:**
- The existing `makeBundle` works on a worktree. For a remote-registered repo with no worktree, the host needs a local ref to bundle. The bare clone provides that: `git bundle create --all` works on a bare repo too.
- When a work-item box is first created (PR/issue), the host fetches the triggering ref into the bare clone, then bundles from there. The box's first boot is identical to the current path (bundle → clone → mirror remotes), except the bundle comes from the bare clone instead of a worktree.
- The box still mirrors the remote (ADR 0008) so `git push` works from inside the box. The box's remote is the same URL the user registered.

**Local-only and non-Git projects:** unchanged. A worktree with no remotes is local-only (ADR 0008). A non-Git project has no repo URL and uses manual/scheduled work only (#83 out-of-scope: Git events for non-Git projects).

**Multiple remotes:** the remote that initialized the box is the primary identity (#83 decision, user story 23). Other remotes remain ordinary Git remotes inside the box. The primary identity is the normalized URL used at registration.

---

## 5. Cross-cutting: dirty / running / out-of-order behavior

These three cases appear in #87 and #83. Summary of recommended behavior:

| Case | Current behavior | Recommended M4 behavior |
|---|---|---|
| **Dirty worktree** (uncommitted changes in box) | No concept; box created from commit only | Before ref advance: check `git status --porcelain`; if dirty, mark blocked, surface to user, do not advance |
| **Running job** | `BeginJob` refuses concurrent job (409) | Ref update waits or is queued; the running job is never interrupted; the ref update is a follow-up |
| **Out-of-order events** | No event concept | Dedupe by source event ID; serialize ref updates through the box's single-job constraint; reject backwards ref moves as blocked |
| **Repeated events** | No event concept | Dedupe by source event ID; if no ID, at-least-once (ADR 0003 bias) |
| **Queue full** | No queue | Reject new automatic work with a recorded, visible reason (#83 decision); explicit requests use available capacity |

---

## 6. ADR pointers

- **ADR 0002** (`docs/adr/0002-box-lifecycle.md`): box lifecycle, durability model, auto-pause rule, no auto-destroy. Work-item boxes inherit this.
- **ADR 0003** (`docs/adr/0003-triggers-are-durable-alarms.md`): schedule semantics, at-least-once delivery, busy-skip (changes to queue in M4).
- **ADR 0004** (`docs/adr/0004-storage-and-lease-substrate.md`): local state, versioned records, single host. Queue records follow this.
- **ADR 0005** (`docs/adr/0005-language-and-process-model.md`): one daemon, per-box systemd units, daemon restart re-adopts. Queue must survive restart.
- **ADR 0006** (`docs/adr/0006-access-is-ssh.md`): SSH is the only transport, no relay, no control plane. Remote requests go through SSH.
- **ADR 0007** (`docs/adr/0007-box-contract.md`): contract lives in `.pluto.toml`, parsed on host, hash for staleness. Trusted policy and triggering-ref jobs extend this.
- **ADR 0008** (`docs/adr/0008-sync-is-git-once.md`): bundle sync, remote mirroring, `[env]` token path, no stored credentials, SSH push out of scope until M4.
- **ADR 0009** (`docs/adr/0009-cli-conventions.md`): CLI voice, exit codes, hints. Queue inspection commands follow this.
- **ADR 0011** (`docs/adr/0011-cli-grammar.md`): verb-first grammar, one target, prefix ids. Queue/registration commands fit here.

---

## 7. Code pointers

| Concern | File |
|---|---|
| Box record, states, transitions | `internal/state/state.go` |
| Box creation (worktree-keyed) | `internal/state/state.go:393-440` (`CreateBox`) |
| Job record, history, concurrency | `internal/state/job.go` |
| Schedule record, coalescing | `internal/state/schedule.go` |
| Remote record, tracked remote | `internal/state/remote.go` |
| Contract load/parse/hash | `internal/contract/contract.go` |
| Handoff (bundle, sync, contract apply) | `internal/runner/handoff.go` |
| Host remote reader | `internal/runner/handoff.go:182-213` |
| Runner lifecycle (Up/Pause/Destroy) | `internal/runner/runner.go` |
| Daemon HTTP routes | `internal/daemon/daemon.go` |
| Scheduler loop | `internal/daemon/schedule.go` |
| Auto-pause loop | `internal/daemon/autopause.go` |
| CLI `--device` remote re-exec | `internal/cli/remote.go` |
| Device registry | `internal/devices/devices.go` |
| SSH invocation | `internal/devices/ssh.go` |
| CLI commands (up/run/status/etc.) | `internal/cli/commands.go` |
| CLI entry, `--device` dispatch | `internal/cli/cli.go` |
| API wire types | `internal/api/api.go` |
| HTTP client | `internal/client/client.go` |
| Contract reference doc | `docs/contract.md` |
| Remote access recipes | `docs/remote-access.md` |
| M5 CLI overhaul brief | `docs/m5-cli-overhaul.md` |

---

## 8. Recommendations summary

1. **#87 (work-item boxes):** Extend `Box` with work-item identity (`WorkItemType`, `WorkItemID`, `RepoURL`, `Ref`). Add a `blocked` state (or field) for ref updates that cannot preserve local work. Advance refs only when the worktree is clean; surface blocked updates for user resolution. Reuse the box for all events on the same PR/issue. No auto-destroy.

2. **#90 (remote requests):** The existing SSH/device path is sufficient for M4. Add a daemon-side async request endpoint that returns a queue ID immediately and a queue inspection surface. Deduplicate by client idempotency key. The job survives client disconnect (already true). Defer phone/app API and MCP to M5+ (new auth surface needed).

3. **#86 (remote repo identity):** Add repo registration by URL with a host-side bare clone as the read-only remote view. Discover the default branch via `git ls-remote --symref` or `gh`. Read the trusted policy from the default branch and job definitions from the triggering ref. Reuse the `[env]` token / host git credential / ssh agent path for private repos — Pluto never stores credentials. The first-boot bundle works from the bare clone.

4. **Cross-cutting:** Deduplicate events by source event ID. Serialize ref updates through the box's single-job constraint. Reject backwards/out-of-order ref moves as blocked. Queue (don't skip) when capacity is unavailable (changes ADR 0003 busy-skip). Keep all new state in versioned local records (ADR 0004) so it survives daemon restarts.
