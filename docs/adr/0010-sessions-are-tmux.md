# Sessions are tmux-owned; pause kills them, wake restarts them

A **session** is a declared long-lived interactive process in a box — an agent, a TUI, or any long-lived command — written as `[sessions.<name>]` in the box contract. It runs under tmux, which owns the PTY, so a client can detach and reattach without ending the process. tmux is the boring choice (ADR 0005), and the deferred agent-aware pane work (herdr) stays parked.

Durability is disk-only (ADR 0002): a pause kills the session's process. On wake the daemon restarts every declared session, and the program's own on-disk state — an agent's session database — is what resumes the conversation. pluto stores no agent state of its own. `pluto attach <box> --session <name>` enters a session; `pluto status` lists declared sessions and whether one is attached.

Auto-pause keeps its rule — no client attached and no job running for the idle window — and adds a workload signal: a box is busy while a client is attached, a job is running, or a session's cgroup has burned CPU/IO over the window (cgroup v2 counters). The rule is agent-agnostic: pluto never calls an agent's API. Thresholds and hysteresis are policy, tuned from the parked auto-pause-signals research.

Sessions are agent-agnostic by construction: an agent is whatever command a session runs, just as ADR 0007 keeps agents out of the contract's vocabulary.

## Considered options

- **Agent APIs (opencode/T3 turn status)**: authoritative busy, but a per-agent integration that contradicts the agent-agnostic stance.
- **A pluto-owned PTY/session daemon (herdr)**: more control over panes, but a new subsystem; deferred.
- **Never pause while a session exists**: sessions are long-lived, so the box would never sleep.
- **`[agents.<name>]` as the noun**: makes pluto own an "agent" concept it deliberately does not; `session` stays honest about what is new.

## Consequences

- Sessions are attachable from any machine that can run `pluto --device`, not just the host.
- A turn in flight when a pause wins is lost; only the program's on-disk state survives. The busy signal minimizes but cannot eliminate this.
- The base image must ship tmux, so this decision forces a targeted image rebuild.
- A session counts against the box's one-client/no-job auto-pause rule as a first-class busy signal.
