# Auto-pause signals: busy vs idle in a pluto box

Facts gathered 2026-10-04 for the auto-pause design grilling. pluto pauses disk-only (kernel + all
processes die, filesystem survives). A host daemon cannot read guest internals directly across the
microVM boundary, so every "guest" signal below is queryable by a **guest agent** (or by the host
daemon over vsock into such an agent). Signals are grouped by reliability/authority, not by
implementation.

Baseline from issue #8 / `docs/research/agent-servers-in-boxes.md`: processes and PTYs die on pause;
sessions/transcripts live in SQLite/JSONL on disk. Nothing in the guest currently *reports* busy —
every signal below is either observed (kernel/systemd/tmux) or opt-in (inhibitor locks, agent APIs).

---

## 1. Interactive shell

### utmp / wtmp (`who`, `w`, `last`)
- OpenSSH writes utmp/wtmp/lastlog itself when a **PTY** session starts; the comment on
  `do_exec_pty()` says it forks "when we have a tty ... updating wtmp, utmp, lastlog"
  ([session.c](https://github.com/openssh/openssh-portable/blob/master/session.c), `record_logout`
  at line 2244; utmp length settable with `sshd -u`, default `HOST_NAME_MAX+1`).
- Consequence: interactive `ssh box` appears in `who`; `ssh box cmd` (no PTY) does **not** create a
  utmp entry. `w` derives the IDLE column from tty activity. `who -a` / `last` also show the remote
  host. This is a per-login-entry signal, not a "user is typing now" signal.
- Queries: `who`, `w`, `last -n 20`, `lastlog`.

### systemd-logind sessions (`loginctl`)
- `pam_systemd` creates a logind session per login; visible as
  `SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE` in `loginctl list-sessions`.
  Per-session properties: `Remote` (true for SSH), `Type`, `Class`, `Active`, `State`,
  `IdleHint`, `IdleSinceHint`, `IdleSinceHintMonotonic`
  (man [org.freedesktop.login1](https://www.freedesktop.org/software/systemd/man/latest/org.freedesktop.login1.html)).
- `IdleHint` is a hint *set by the session* (compositor/desktop); a headless box has nothing that
  sets it, so it can read `no` (active) forever. Verified locally: a live Wayland session reports
  `IdleHint=no`, `IdleSinceHint=0`; unset hints default to "not idle". Treat as unreliable in a box.
- Each session gets a system scope: `systemctl list-units 'session-*.scope'` →
  `session-2.scope` etc.; `systemctl show session-<id>.scope -p ActiveState -p ActiveEnterTimestamp
  -p ControlGroup`. Commands run by that login live in its cgroup. Verified locally.
- Also `loginctl show-user <user>`: `IdleHint`, `Linger`, `State`, with `UserStopDelayUSec` /
  `StopIdleSessionUSec` in logind.conf. Linger=true means a user manager exists with no login —
  looks "active" without a user.

### tmux
- `tmux list-clients -F '#{client_name} #{client_activity} #{client_pid} #{client_tty} #{client_session}'`
  — `client_activity` is "Time client last had activity" (Unix epoch).
- `tmux list-sessions -F '#{session_name} attached=#{session_attached} activity=#{session_activity}'`
  — `session_activity` is "Time of session last activity", `session_attached` counts clients; also
  `session_active` (1/0), `session_attached_list`, per-window `window_activity`
  ([tmux(1) FORMATS](https://man7.org/linux/man-pages/man1/tmux.1.html)). Verified live: values are
  Unix epoch seconds.
- Caveat: `session_activity` tracks *input/interaction*, not pane output; a detached tmux pane
  running a 20-minute build will not necessarily refresh it. Use cgroup counters for builds.
  tmux persists nothing on pause; the tmux server process is the only owner of this signal.

### mosh
- `mosh-server` is a normal user process for the life of a connection; no mosh daemon.
  `pgrep -a mosh-server` works, but it carries no busy/idle metadata.
- mosh *does* write a utmp entry when built `--with-utempter` (libutempter):
  `utempter_add_record(master, "mosh [<pid>]")` around its `forkpty`, removed on exit
  ([mosh-server.cc](https://github.com/mobile-shell/mosh/blob/master/src/frontend/mosh-server.cc)).
  Without that build flag, no entry (`configure.ac` warns).
- mosh does not go through PAM, so it creates **no logind session** — `loginctl`/`systemctl
  session-*.scope` will not see it even though `who` may. Mosh itself syncs only visible terminal
  state; it has no idle metric.

---

## 2. Commands / builds / scopes / cgroups

- **Unit/scope inventory**: `systemctl --user list-units --type=service --state=running`;
  `systemctl list-units 'session-*.scope'`; transient builds launched with `systemd-run --scope`
  (or `systemd-run --unit=` for services) appear while running. Each unit exposes
  `ActiveState`, `ActiveEnterTimestamp`, `ControlGroup`, `MainPID`.
  `systemd-cgtop -1 -b --order=cpu` gives a one-shot snapshot sorted by CPU.
- **cgroup v2 counters** (this is the actual "is something burning CPU/IO" primitive; kernel docs
  [cgroup-v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)):
  - `/sys/fs/cgroup/<cg>/cpu.stat` → `usage_usec` (monotonic; delta over an interval = busy time;
    also `user_usec`, `system_usec`, `nr_throttled`).
  - `/sys/fs/cgroup/<cg>/io.stat` → per-device `rbytes wbytes rios wios` (monotonic; delta = IO).
  - `/sys/fs/cgroup/<cg>/cpu.pressure`, `io.pressure`, `memory.pressure` → PSI
    `some avg10=… avg60=… avg300=… total=…` / `full …`. PSI directly answers "was anything
    stalled/running recently", no sampling loop strictly required.
  - `/sys/fs/cgroup/<cg>/cgroup.events` → `populated 1|0` (any processes?), `frozen 1|0`;
    `/sys/fs/cgroup/<cg>/cgroup.freeze` can freeze a subtree.
  - Verified locally: `session-c4.scope` has a full v2 control file set and `populated 1`;
    `user.slice/cpu.stat` and `cpu.pressure` read fine unprivileged.
- **Layout in a systemd guest**: login shells → `user.slice/user-<uid>.slice/session-N.scope`;
  user services (agent servers, tmux) → `user.slice/user-<uid>.slice/user@<uid>.service/…`;
  system services under `system.slice`. A guest agent can sum deltas across `user.slice` and/or the
  union of `session-*.scope` to distinguish "someone is typing/compiling" from "a daemon idles".
- CPUAccounting/IOAccounting are on by default for managed units on cgroup v2 kernels, so the
  counters exist for free. Thresholds (e.g. ">5% CPU over 60s") are a policy choice, not a
  measurement problem. Watch idle noise: journald, tailscaled, sshd keepalives, agent-server event
  loops and LSP reindexing will show small nonzero deltas forever.

---

## 3. Agent servers

### opencode (v2; source commit `907b3bc518fa48e90e8ec24dd327d13eee71c36c`)
Authoritative, queryable turn state. From the generated OpenAPI spec
(`packages/sdk/openapi.json` in the repo) and `packages/schema/src/session-status-event.ts`:

- `GET /session/status` — "Retrieve the current status of all sessions"; returns
  `{ [sessionID]: SessionStatus }` where `SessionStatus` =
  `{type:"idle"}` | `{type:"retry", attempt, message, next, action?}` | `{type:"busy"}`.
- `GET /api/session/active` — map `{ "ses…": {type:"running"} }` for currently running sessions.
- `POST /api/session/{sessionID}/wait` — "Wait for a session agent loop to become idle."
- SSE streams `/event` and `/global/event`, plus `/api/event`, emit `session.status` events
  (`{id:"evt_…", type:"session.status", properties:{sessionID, status}}`) and the deprecated
  `session.idle`. Plugins can subscribe to `session.idle` / `session.status`
  ([plugins docs](https://opencode.ai/docs/plugins/)).
- Auth: `OPENCODE_SERVER_PASSWORD` basic auth (`/docs/server/`). Verified against the live v2
  server spawned by T3 on this machine: `/api/session`, `/api/event`, `/openapi.json` return
  `401 application/json` without credentials; a same-user guest agent can read the password/token
  from the service environment or `~/.local/share/opencode`.
- So: a guest agent can ask opencode directly, no heuristics. Caveats: the API is per running
  server; the busy signal dies with the process, and "retry" (provider backoff) is arguably still
  busy.

### T3 Code (source commit `d60a71ef6e371d8b345ecb1f7575117d675187c7`)
Two relevant layers:

- **Thread run state** (server-owned projection): thread shells have
  `status = "idle" | "preparing" | "queued" | "starting" | "running" | "waiting" | "completed" |
  "interrupted" | "failed" | "cancelled" | "rolled_back"` and
  `activityRunStatus = null | "preparing" | "starting" | "running" | "waiting"` with
  `activityRunStartedAt` (`packages/contracts/src/orchestrationV2.ts`,
  `apps/server/src/orchestration-v2/ProjectionStore.ts`). `ThreadSettlementService` uses
  `activityRunStatus != null` to mean a live run. This is the closest thing to "a turn is in
  flight"; there is no documented busy HTTP endpoint, it is exposed via authenticated RPC.
- **Background policy**: RPC `serverGetBackgroundPolicy` (no payload) returns a
  `BackgroundPolicySnapshot`, and `subscribeBackgroundPolicy` is a change stream
  (`packages/contracts/src/rpc.ts`). Snapshot = `{hostPower, leases[], activeForegroundLeaseCount,
  activeScopeKeys[], shouldRunOpportunisticWork, updatedAt}`
  (`packages/contracts/src/background.ts`). Leases are client activity reports
  (`visible`, `focused`, `recentlyInteracted`, `appState`, `scopes` incl. `{type:"thread",threadId}`)
  with a **default 45 s TTL (max 120 s)**; "foreground" = visible && (focused || recentlyInteracted);
  `HostPowerMonitor` tracks host `idle`, `idleSeconds`, `locked`, `suspended`, `onBattery`,
  `thermalState` (`apps/server/src/background/{BackgroundPolicy,HostPowerMonitor}.ts`).
- Access requires environment auth (`EnvironmentAuthorizationError`); the running local server
  answers HTTP 200 with the web app but RPC needs pairing. A guest agent would need the
  environment's token/SessionStore or its own paired client. Note the local machine shows T3
  already takes a systemd **sleep** inhibitor on the host ("Application cleanup before suspend"),
  not an idle inhibitor tied to turn busyness.

---

## 4. Explicit inhibition (the clean opt-in signal)

Primary source: [systemd.io/INHIBITOR_LOCKS](https://systemd.io/INHIBITOR_LOCKS/) and
[systemd-inhibit(1)](https://www.freedesktop.org/software/systemd/man/latest/systemd-inhibit.html).

- `idle` lock: "inhibits that the system goes into idle mode, possibly resulting in **automatic**
  system suspend or shutdown depending on configuration." Other locks: `sleep`, `shutdown`,
  `handle-power-key/suspend-key/hibernate-key/lid-switch`. Modes: `block` (default, indefinite),
  `block-weak` (not enforced against root/owner), `delay` (time-bounded; only `sleep`/`shutdown`).
- Taking a lock is one call: `Inhibit(what, who, why, mode) → fd`; the lock is released when the fd
  closes **or the holder dies** (kernel closes it) — exactly the semantics a crash-prone agent
  server wants. `ListInhibitors()` returns `(what, who, why, mode, uid, pid)`.
- Queryable state: `systemd-inhibit --list` (filters `--what`, `--who`, …), and machine-readable
  `systemd-inhibit --list --json=short`; DBus properties `BlockInhibited`, `BlockWeakInhibited`,
  `DelayInhibited` on the logind Manager are the union of active `what` per mode.
- Live probe (this machine, unprivileged): `systemd-inhibit --what=idle --who=pluto-probe
  --why='…' --mode=block sleep 3` was visible while running as
  `{"who":"pluto-probe","uid":1000,"pid":…,"comm":"systemd-inhibit","what":"idle","why":"…","mode":"block"}`
  and gone after exit. So an unprivileged user can take `idle` and anyone can list it.
  (Blocking locks are polkit-gated per action, `org.freedesktop.login1.inhibit-block-idle`; assumed
  allowed for one's own session, but a minimal guest should verify polkit behavior.)
- Design fit: the auto-pause daemon defines "a process holding an `idle` block inhibitor ⇒ busy".
  But opencode and T3 do **not** take such a lock while a turn runs today; it would come from a
  wrapper/plugin/supervisor, or the daemon could *also* look at agent APIs. `idle` locks were made
  for auto-suspend decisions, not for arbitrary daemons; the daemon must define its own meaning.
- Note `systemd-inhibit --list` covers all users and system services (it is the logind-wide list),
  so it also shows unrelated sleep-delay locks; filter by `--what=idle` and/or `who`.

---

## 5. Comparable systems (primary sources)

- **E2B** — auto-pause is wall-clock, not activity detection: `onTimeout: 'pause'` pauses (memory
  or filesystem-only) when the create/set timeout expires; `setTimeout()` explicitly resets it;
  `Sandbox.connect()` extends to the later of current expiry and now+timeout (default 5 min) but
  never shortens. Docs recommend "periodically call set timeout every time user interacts".
  ([persistence](https://e2b.dev/docs/sandbox/persistence), [lifecycle](https://e2b.dev/docs/sandbox)).
  Also precedent for refusing a pause under load: HTTP 503 `ServiceBusyError`, "sandbox keeps
  running with its state intact"; memory auto-pause falls back to filesystem-only after ~2 min of
  refusals.
- **GitHub Codespaces** — idle timeout default 30 min, configurable 5–240 min, max lifetime 12 h.
  "Inactivity is defined as the absence of activity indicative of a user's presence. Personal
  interaction … typing or using the mouse resets the idle timeout. **Terminal activity, either
  input or output, also resets** the idle timeout period." Port traffic alone does not
  ([docs](https://docs.github.com/en/codespaces/setting-your-user-preferences/setting-your-timeout-period-for-github-codespaces)).
- **Gitpod (now Ona)** — default stop after 30 min without user input (keystrokes or terminal input
  commands); "All inactivity timeouts are dependent on an active editor or IDE connection. Closing
  your Gitpod connected editor or IDE will reduce the workspace timeout to **5 minutes** unless an
  explicit timeout is set via preference or `gp timeout set`." Max workspace lifetime 8 h free /
  36 h paid ([workspace-lifecycle](https://www.gitpod.io/docs/configure/workspaces/workspace-lifecycle)).
- **Coder** — autostop after N hours, extended by an **activity bump** (default 1 h). "A workspace
  is considered active when Coder detects one or more active sessions": VS Code, JetBrains, web
  terminal, SSH (`coder ssh`/config SSH), **and "AI agent task status: when a coding agent reports
  'working' status, the workspace deadline is extended"**. Explicitly *not* activity: viewing
  dashboard/settings/logs, direct port URLs without a session, "background agent statistics
  reporting". Autostop requirement can force stops ignoring connections; dormancy deletes unused
  workspaces ([activity detection](https://coder.com/docs/user-guides/workspace-scheduling.md#activity-detection),
  [schedule](https://coder.com/docs/admin/templates/managing-templates/schedule.md)).
- **celld** (`denoland/celld`) — `CELLD_IDLE_EVICT_S` ("Idle-cell eviction age (disabled unless
  set)"). Idle = a **resident** cell with no work: eviction candidate when
  `now - cell.last_used_mono_ms >= idle_ms`; `last_used_mono_ms` is refreshed by local request
  arrival and by request completion (`crates/logic/lib.rs` `evict_idle`, `record_local_request`;
  docs/README: "removes an idle cell from memory after … seconds without work"). Under memory
  pressure it "does not shed a cell with active work or a live host WebSocket"; ordinary
  hibernatable WebSocket clients do not prevent idle eviction — the client stays connected across
  hibernation. No default: without the env var, cells stay resident until pressure.
  ([README](https://github.com/denoland/celld), [docs/README](https://github.com/denoland/celld/blob/main/docs/README.md)).

The comparable pattern: user-presence signals (input, terminal I/O) + an explicit/agent-reported
"working" state; wall-clock timeout as the backstop; forced-stop policies deliberately ignore
connections.

---

## Query cookbook (all read-only, from inside the guest)

```sh
# shell presence
who; w; last -n 20
loginctl list-sessions
loginctl show-session <id> -p IdleHint -p Remote -p Type -p Active -p State -p TTY
systemctl list-units 'session-*.scope'
pgrep -a mosh-server

# tmux
tmux list-clients  -F '#{client_name} #{client_tty} #{client_activity} #{client_session}'
tmux list-sessions -F '#{session_name} #{session_attached} #{session_activity} #{session_active}'

# builds / workloads
systemctl --user list-units --type=service --state=running
systemctl show <unit> -p ActiveState -p ActiveEnterTimestamp -p ControlGroup -p MainPID
systemd-cgtop -1 -b --order=cpu
cg=/sys/fs/cgroup$(systemctl show <unit> -p ControlGroup --value)
cat $cg/cpu.stat; cat $cg/io.stat; cat $cg/cpu.pressure; cat $cg/io.pressure; cat $cg/cgroup.events

# explicit busy
systemd-inhibit --list --what=idle --no-pager
systemd-inhibit --list --json=short | jq -c '.[] | select(.what|split(":")|index("idle"))'
# take one: systemd-inhibit --what=idle --who=<name> --why=<reason> --mode=block <cmd>

# agent servers
curl -su ":$OPENCODE_SERVER_PASSWORD" http://127.0.0.1:4096/session/status   # {ses…: idle|retry|busy}
curl -su ":$OPENCODE_SERVER_PASSWORD" http://127.0.0.1:4096/api/session/active
# T3: authenticated RPC serverGetBackgroundPolicy / subscribeBackgroundPolicy;
#     thread shells carry status + activityRunStatus in the v2 projection.
```

## Design choices still open

1. Where the decision runs: a guest agent exporting a "busy" bit over vsock, versus the host daemon
   scraping VMM-level stats (which cannot see guest cgroups/utmp at all).
2. Definition of idle: pure wall-clock since last input (Codespaces/Gitpod), CPU/IO threshold
   (cgroups), explicit inhibitor (systemd), agent-reported turn state (opencode/T3/Coder), or a
   combination with priority ordering.
3. Whether opencode/T3 should be wrapped to take an `idle` inhibitor on turn start and release on
   `session.idle`/run settlement (opencode plugin; T3 background-policy hook), instead of the
   daemon polling APIs.
4. "Waiting for user input" semantics: permission prompts, T3 `waiting`, opencode `retry` — busy
   or idle? (T3/opencode report these distinctly.)
5. Noise floor: journald, sshd, tailscaled, LSP/indexers and agent SSE loops keep CPU nonzero
   forever; thresholds and minimum-idle windows are policy.
6. Interactive-but-quiet: an attached tmux client with no keystrokes for an hour (Gitpod calls the
   IDE-connection case 5 min; Codespaces counts terminal output). Does an open SSH/SSE connection
   itself count as presence?
7. Mosh coverage: utmp entry only with libutempter builds; no logind session. Is mosh worth a
   dedicated signal or is "mosh-server process exists" enough?
8. Session-less activity: builds started by cron/CI inside the box, or background agent subagents —
   cgroups catch them, user-presence signals do not.
9. Pause-refusal behavior under load: E2B returns 503 and retries for ~2 min before degrading to
   filesystem-only; pluto needs its own contract (retry vs force pause vs notify).
10. Trust/auth from daemon to guest agents: opencode basic-auth secret, T3 pairing token, or an
    agent-specific unix socket; plus what happens when the agent process is wedged.
11. Hysteresis and debounce: idle must be sustained for N minutes; one CPU sample or one
    `session.status` event should not flip the state.
12. Idle hints: logind `IdleHint` is compositor-driven and unset on headless boxes; do not rely on it.

## Sources

- OpenSSH `session.c` / `sshd-session.c` (utmp, only with PTY):
  https://github.com/openssh/openssh-portable/blob/master/session.c
- logind API: https://www.freedesktop.org/software/systemd/man/latest/org.freedesktop.login1.html
- pam_systemd: https://www.freedesktop.org/software/systemd/man/latest/pam_systemd.html
- tmux(1) FORMATS: https://man7.org/linux/man-pages/man1/tmux.1.html
- mosh-server utmp: https://github.com/mobile-shell/mosh/blob/master/src/frontend/mosh-server.cc
- cgroup v2: https://docs.kernel.org/admin-guide/cgroup-v2.html
- systemd inhibitor locks: https://systemd.io/INHIBITOR_LOCKS/ and
  https://www.freedesktop.org/software/systemd/man/latest/systemd-inhibit.html
- opencode server docs: https://opencode.ai/docs/server/ ; plugins/events:
  https://opencode.ai/docs/plugins/ ; source: anomalyco/opencode commit
  `907b3bc518fa48e90e8ec24dd327d13eee71c36c` (`packages/schema/src/session-status-event.ts`,
  `packages/sdk/openapi.json`)
- T3 Code source: pingdotgg/t3code commit `d60a71ef6e371d8b345ecb1f7575117d675187c7`
  (`packages/contracts/src/{background,orchestrationV2,rpc}.ts`,
  `apps/server/src/background/{BackgroundPolicy,HostPowerMonitor}.ts`,
  `apps/server/src/orchestration-v2/ProjectionStore.ts`)
- E2B: https://e2b.dev/docs/sandbox/persistence , https://e2b.dev/docs/sandbox
- Codespaces idle timeout:
  https://docs.github.com/en/codespaces/setting-your-user-preferences/setting-your-timeout-period-for-github-codespaces
- Gitpod workspace lifecycle:
  https://www.gitpod.io/docs/configure/workspaces/workspace-lifecycle
- Coder activity detection: https://coder.com/docs/user-guides/workspace-scheduling.md#activity-detection
  and https://coder.com/docs/admin/templates/managing-templates/schedule.md
- celld: https://github.com/denoland/celld (`docs/README.md`, `crates/logic/lib.rs`, README)
