# Triggers are durable alarms on the box record

A trigger is a durable activation request attached to a box: *ensure running → optionally run a command → record the outcome*. Schedules are declared in the box's `.pluto.toml` (ADR 0007) and stored as box records with the arm time that made them current and the latest due time materialized into host-queue work, so they survive daemon restarts and host reboots. A due occurrence is durably enqueued before the schedule clock advances. Repeated ticks coalesce against the active queue item identified by box and schedule; a daemon restart recovers that same item rather than creating a backlog. The host queue orders explicit user work before repository events and scheduled work, preserves FIFO within a class, and promotes aged work. It enforces the host running-box limit and a box still runs at most one job at a time. Firing lands in M1; fleet claims are deferred until a second host exists (ADR 0004).

## Considered options

- **Systemd timers as the source of truth**: free catch-up via `Persistent=`, but binds schedule state to one host's systemd and cannot travel with a box when export/import lands.
- **Daemon-memory timers**: lose schedules on restart.
- **Skip due work when a box is busy**: simple, but drops requested work. M4 stores due work in the durable host queue instead.

## Consequences

- Delivery is at-least-once: a crash window can duplicate a run — the right bias for build and agent work.
- A busy box leaves the occurrence pending in the durable host queue; it does not start a concurrent job. A firing missed while the daemon was down coalesces into exactly one late run on restart. There is no retry beyond that late run, and a failed run is recorded like any job.
- Warm-ups use the same queue and host running-box limit. Only one active queue item can exist for each box and schedule; further due times advance the schedule clock while coalescing into that item. Scheduled jobs use the lowest default priority, receive aging promotions, and record outcomes in the existing per-box job history.
- A paused box's schedule fires from its host; no other host exists in v1.
- Wake-on-connection is deferred (`attach` implies `up`, ADR 0006).
- "Trigger" is the umbrella for manual runs, schedules, and connection wakes.
