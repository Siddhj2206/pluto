# Triggers are durable alarms on the box record

A trigger is a durable activation request attached to a box: *ensure running → optionally run a command → record the outcome*. Schedules are declared in the box's `.pluto.toml` (ADR 0007) and stored as box records with the arm time that made them current and the time of their last consumed occurrence, so they survive daemon restarts and host reboots. The daemon materializes due times into timers it can fire; missed firings coalesce to one late run rather than replaying a backlog. An occurrence that comes due while the box already has a job running is skipped — no queue and no concurrent run — and the skip advances the schedule like a fire would, so the next occurrence proceeds normally. Firing lands in M1; fleet claims are deferred until a second host exists (ADR 0004).

## Considered options

- **Systemd timers as the source of truth**: free catch-up via `Persistent=`, but binds schedule state to one host's systemd and cannot travel with a box when export/import lands.
- **Daemon-memory timers**: lose schedules on restart.

## Consequences

- Delivery is at-least-once: a crash window can duplicate a run — the right bias for build and agent work.
- A busy box skips the occurrence (no queue, no concurrent run); a firing missed while the daemon was down coalesces into exactly one late run on restart. There is no retry beyond that late run, and a failed run is recorded like any job.
- A paused box's schedule fires from its host; no other host exists in v1.
- Wake-on-connection is deferred (`attach` implies `up`, ADR 0006).
- "Trigger" is the umbrella for manual runs, schedules, and connection wakes.
